package materialize

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// What this program put in the managed folder.
//
// The folder is madplayer's, and the two rules that follow from that both need
// the same fact — which files are ours:
//
//   - Entries can be lost while the bytes remain (a deleted database, an
//     interrupted write). Those files are re-registered rather than left as
//     music on disk that nothing can play.
//   - A file a PERSON puts there is IGNORED, with a warning. Not adopted, and
//     not moved somewhere else on their behalf: moving somebody's file is worse
//     than refusing it, and adopting it would make "managed" mean nothing.
//
// Nothing on disk distinguishes the two, so the record is kept — a list of what
// was written, in madplayer's own data directory rather than in the music
// folder. That placement is the point: it survives the library database being
// thrown away, which is exactly the case the first rule exists for. And when the
// record itself is lost, everything looks like somebody else's, so the program
// warns instead of silently adopting a stranger's music. The conservative
// direction is the one the design asks for.

// RecordFile is the record's name inside the data directory.
const RecordFile = "materialized.json"

// Record maps each file this program wrote, relative to the managed folder, to
// the content hash it was written from.
type Record struct {
	mu sync.Mutex
	// Root is the folder this record describes. Keeping it means a changed
	// setting cannot make the old folder's entries look like the new one's.
	Root  string            `json:"root"`
	Files map[string]string `json:"files"`
}

// LoadRecord reads the record for a root, or returns an empty one.
//
// A record describing a DIFFERENT root comes back empty: the setting was
// changed, and the files it lists are somewhere this program no longer manages.
// They are left exactly where they are — music already in the library stays in
// the library — the folder simply stops being ours.
func LoadRecord(dataDir, root string) (*Record, error) {
	rec := &Record{Root: root, Files: map[string]string{}}

	b, err := os.ReadFile(filepath.Join(dataDir, RecordFile))
	if errors.Is(err, fs.ErrNotExist) {
		return rec, nil
	}
	if err != nil {
		return rec, err
	}

	var stored struct {
		Root  string            `json:"root"`
		Files map[string]string `json:"files"`
	}
	if err := json.Unmarshal(b, &stored); err != nil {
		return rec, err
	}
	if stored.Root != root {
		return rec, nil
	}
	if stored.Files != nil {
		rec.Files = stored.Files
	}
	return rec, nil
}

// Save writes the record, atomically — it is rewritten on every materialize, so
// a half-written one is the shape most likely to exist after a crash.
func (r *Record) Save(dataDir string) error {
	r.mu.Lock()
	b, err := json.Marshal(struct {
		Root  string            `json:"root"`
		Files map[string]string `json:"files"`
	}{r.Root, r.Files})
	r.mu.Unlock()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dataDir, RecordFile)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Add records a file this program wrote.
func (r *Record) Add(rel, hash string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.Files == nil {
		r.Files = map[string]string{}
	}
	r.Files[filepath.ToSlash(rel)] = hash
}

// Remove forgets one, which is what a file that has left the folder gets.
func (r *Record) Remove(rel string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.Files, filepath.ToSlash(rel))
}

// HashAt is the content hash written at a relative path, and whether the path is
// ours at all. Both answers matter: the hash decides whether a request is
// already satisfied, and the second return decides whether a file is a stray.
func (r *Record) HashAt(rel string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	h, ok := r.Files[filepath.ToSlash(rel)]
	return h, ok
}

// Holds reports whether this content hash is already somewhere in the folder,
// under any name.
//
// This is what makes materialize idempotent regardless of naming: the same audio
// asked for twice is the same audio, even if the tags were edited between the
// two requests and the second name would differ.
func (r *Record) Holds(hash string) (string, bool) {
	if hash == "" {
		return "", false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for rel, h := range r.Files {
		if h == hash {
			return rel, true
		}
	}
	return "", false
}

// Len is how many files this program has put in the folder.
func (r *Record) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.Files)
}

// Survey is what a walk of the managed folder found.
type Survey struct {
	// Ours are files this program wrote that are still there, relative to the
	// root, sorted.
	Ours []string
	// Strays are audio files somebody else put there. They are reported so the
	// UI can say so, and otherwise left alone.
	Strays []string
	// Gone are paths the record claims that are no longer on disk — somebody
	// deleted or moved them, which is their right.
	Gone []string
}

// Survey walks the managed folder and sorts what is in it into those three.
//
// A missing folder is not an error: it is the ordinary state before anything has
// been materialized, and the answer is that there is nothing in it.
func (r *Record) Survey(root string) (Survey, error) {
	var s Survey
	if root == "" {
		return s, nil
	}

	seen := map[string]bool{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// An unreadable subdirectory is a reason to skip it, not to abandon
			// the walk and report nothing about the rest of the folder.
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if _, ours := r.HashAt(rel); ours {
			seen[rel] = true
			s.Ours = append(s.Ours, rel)
			return nil
		}
		// Only audio counts as a stray. A cover.jpg somebody dropped in, or the
		// .DS_Store their file manager left, is not something to warn about.
		if IsAudio(d.Name()) && !strings.HasPrefix(d.Name(), ".") {
			s.Strays = append(s.Strays, rel)
		}
		return nil
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return s, err
	}

	r.mu.Lock()
	for rel := range r.Files {
		if !seen[rel] {
			s.Gone = append(s.Gone, rel)
		}
	}
	r.mu.Unlock()

	sort.Strings(s.Ours)
	sort.Strings(s.Strays)
	sort.Strings(s.Gone)
	return s, nil
}
