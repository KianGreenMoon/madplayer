package materialize

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"daemonlord.ygg/madplayer/internal/queue"
)

// Fetcher makes a remote track's bytes local. It is the same interface the
// player uses, and deliberately so: keeping a track and playing it need the
// identical thing, so a track already downloaded costs nothing to keep.
type Fetcher interface {
	Local(ctx context.Context, item *queue.Item) (string, error)
}

// Registrar is the library, as this package needs it.
//
// Register is a RESCAN, not a per-file call, and that is the point: the scanner
// already adds what is new and skips content it has already linked, so "index
// only the files the library is missing" is a property that falls out of the
// existing machinery rather than a rule reimplemented here. (It decides that by
// hashing, not by size and mtime — a rescan of a big managed folder is not free,
// which is an argument for keeping a whole album in one pass rather than a
// reason to reimplement the skip here.)
type Registrar interface {
	// EnsureFolder makes the managed folder a data source, adding it when the
	// library does not have it — which is also how a thrown-away database
	// recovers files that are still on disk.
	EnsureFolder(ctx context.Context, root string) (added bool, err error)
	// Register indexes what is new in it.
	Register(ctx context.Context, root string) error
	// Describe tells the library what a track IS, for everything its bytes do
	// not say. It runs after the indexing above, and it is not an optional
	// nicety: the scanner can only read the file, while the library this track
	// came FROM knew more than the file does — an album artist somebody set in a
	// web UI is in no blob anywhere, and a WAV carries no tags at all. Without
	// this, keeping such an album files it under "Unknown artist".
	Describe(ctx context.Context, tr Track) error
}

// Keeper copies network music into the managed folder.
type Keeper struct {
	// mu serialises keeps. They share the record and the scanner, and the
	// scanner answers a second concurrent request with an error rather than
	// queueing it — so one at a time is the shape, not a precaution.
	mu        sync.Mutex
	dataDir   string
	root      string
	technical bool
	// explicit records that the folder was CHOSEN rather than defaulted, which
	// decides what happens when it turns out not to be writable: a chosen folder
	// is refused and reported, a defaulted one falls back.
	explicit bool
	// checked memoises the writability probe, and note carries what to say when
	// the fallback was taken.
	checked bool
	note    string
	rec     *Record
	fetch   Fetcher
	reg     Registrar
}

// NewKeeper loads the record for a root and returns a keeper over it.
//
// A record that will not parse is reported and the keeper works anyway, with an
// empty one: the cost is that existing files look like strays, which is the safe
// direction.
func NewKeeper(dataDir, root string, explicit, technical bool, f Fetcher, r Registrar) (*Keeper, error) {
	rec, err := LoadRecord(dataDir, root)
	return &Keeper{
		dataDir: dataDir, root: root, explicit: explicit, technical: technical,
		rec: rec, fetch: f, reg: r,
	}, err
}

// Note is what to say about the folder, or "".
//
// It is empty until something has been kept, because nothing is probed — and so
// nothing is known — before then.
func (k *Keeper) Note() string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.note
}

// ensureRoot probes the folder the first time it is needed, and takes the
// fallback if it may.
//
// A folder somebody CHOSE is refused and reported when it cannot be written:
// they named a place, and quietly writing somewhere else is worse than saying
// no. A DEFAULTED folder falls back into the app's own data directory, which on
// a phone is the ordinary case rather than the exception — with the sentence
// that says the music will not be visible to a file manager there, because that
// breaks the promise the whole design makes.
func (k *Keeper) ensureRoot() error {
	if k.checked {
		if k.note != "" && k.explicit {
			return errors.New(k.note)
		}
		return nil
	}
	k.checked = true

	if err := Writable(k.root); err == nil {
		return nil
	} else if k.explicit {
		k.note = fmt.Sprintf("%s cannot be written to: %v", k.root, err)
		return errors.New(k.note)
	}

	fallback := filepath.Join(k.dataDir, DirName)
	if err := Writable(fallback); err != nil {
		k.note = fmt.Sprintf("neither %s nor %s can be written to: %v", k.root, fallback, err)
		return errors.New(k.note)
	}
	k.note = fmt.Sprintf("Your music folder cannot be written to, so network music is kept in %s — where a file manager will not find it. Set a folder in Settings to change that.", fallback)
	k.root = fallback
	// The record belongs to a root, so the fallback gets its own. Nothing has
	// been written yet, so there is nothing to carry over.
	rec, err := LoadRecord(k.dataDir, fallback)
	if err == nil {
		k.rec = rec
	}
	return nil
}

// Root is the managed folder.
func (k *Keeper) Root() string { return k.root }

// Kept is how many files this program has put there.
func (k *Keeper) Kept() int { return k.rec.Len() }

// Result says what happened, because "already there" is a different sentence
// from "downloaded and saved" and the UI says both.
type Result struct {
	// Path is the file, absolute.
	Path string
	// Already reports that the audio was in the folder before this ran and
	// nothing was written. Materialize is idempotent — Materialize all exists,
	// and it gets pressed twice.
	Already bool
}

// ErrNotRemote is returned for a track this machine already holds. Keeping it
// would copy somebody's own music into a folder the program manages, which is
// the opposite of the point.
var ErrNotRemote = errors.New("this track is already on this device")

// Keep saves a network track into the managed folder and hands it to the
// library.
//
// Filesystem failures REFUSE AND REPORT. There is no retry under a second name:
// a write that failed is a fact about the disk, and inventing a way around it is
// how a music folder ends up with files nobody meant.
func (k *Keeper) Keep(ctx context.Context, tr Track, item *queue.Item) (Result, error) {
	if item == nil {
		return Result{}, errors.New("nothing to keep")
	}
	if item.Path != "" {
		return Result{}, ErrNotRemote
	}
	if k.root == "" {
		return Result{}, errors.New("no folder is set for keeping network music")
	}

	k.mu.Lock()
	defer k.mu.Unlock()

	if err := k.ensureRoot(); err != nil {
		return Result{}, err
	}

	// Already ours, under any name. Matching by content hash rather than by path
	// is what makes this survive the tags having been edited since.
	if rel, ok := k.rec.Holds(tr.Hash); ok {
		abs := filepath.Join(k.root, filepath.FromSlash(rel))
		if _, err := os.Stat(abs); err == nil {
			return Result{Path: abs, Already: true}, nil
		}
		// Recorded but gone: somebody deleted it, which is their right. Forget
		// it and write it again.
		k.rec.Remove(rel)
	}

	rel, err := tr.Name(k.technical)
	if err != nil {
		return Result{}, err
	}
	rel, err = k.resolve(tr, rel)
	if err != nil {
		return Result{}, err
	}

	src, err := k.fetch.Local(ctx, item)
	if err != nil {
		return Result{}, err
	}

	abs := filepath.Join(k.root, filepath.FromSlash(rel))
	if err := copyInto(src, abs); err != nil {
		return Result{}, err
	}

	k.rec.Add(rel, tr.Hash)
	if err := k.rec.Save(k.dataDir); err != nil {
		// The bytes are on disk and that is what matters; a record that did not
		// save means the file reads as a stray later, which is visible and
		// recoverable. Report it rather than undoing a good copy.
		return Result{Path: abs}, fmt.Errorf("saved %s, but the record of it could not be written: %w", rel, err)
	}

	added, err := k.reg.EnsureFolder(ctx, k.root)
	if err != nil {
		return Result{Path: abs}, fmt.Errorf("saved %s, but the folder could not be added to your library: %w", rel, err)
	}
	if !added {
		// Adding a folder already scans it. Asking for a second scan on top
		// would be asking the scanner to run twice at once, which it answers
		// with an error rather than a queue.
		if err := k.reg.Register(ctx, k.root); err != nil {
			return Result{Path: abs}, fmt.Errorf("saved %s, but it could not be indexed: %w", rel, err)
		}
	}
	if err := k.reg.Describe(ctx, tr); err != nil {
		// The track is in the library, playable, under whatever its own tags
		// say — which for an untagged file is the Unknown-artist bucket. Worth
		// saying out loud rather than swallowing: the file is fine and the row
		// is wrong, and those look nothing alike to somebody browsing.
		return Result{Path: abs}, fmt.Errorf("saved %s, but your library could not be told what it is: %w", rel, err)
	}
	return Result{Path: abs}, nil
}

// resolve turns a wanted name into one that is free, or refuses.
//
// A collision with DIFFERENT audio takes the content-hash suffix, and only then:
// the ordinary file keeps the ordinary name.
func (k *Keeper) resolve(tr Track, rel string) (string, error) {
	free, err := k.vacant(rel)
	if err != nil || free {
		return rel, err
	}

	tagged := tr.WithHashTag(rel)
	if tagged == rel {
		// No hash to distinguish them with, and something else is already there.
		return "", fmt.Errorf("%s is taken by other audio, and this track has no content hash to tell them apart", rel)
	}
	free, err = k.vacant(tagged)
	if err != nil {
		return "", err
	}
	if !free {
		return "", fmt.Errorf("%s is taken by other audio", tagged)
	}
	return tagged, nil
}

// vacant reports whether a relative path can be written to.
//
// Anything already at that path is in the way, including a copy of this very
// audio — but Keep has answered that case before this runs, by content hash, so
// what reaches here is genuinely somebody else's name. Lstat rather than Stat:
// a dangling symlink occupies the name just as firmly as a file does.
func (k *Keeper) vacant(rel string) (bool, error) {
	abs := filepath.Join(k.root, filepath.FromSlash(rel))
	_, err := os.Lstat(abs)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return true, nil
	case err != nil:
		return false, err
	}
	return false, nil
}

// Reconcile brings the managed folder and the library back into agreement, and
// reports what it found.
//
// It is deliberately cheap enough to run at startup. The expensive half — index
// the files the library is missing — is the scanner's own skip-by-size-and-mtime
// pass, and it only runs when the library has forgotten the folder entirely,
// which is the case this exists for: a database thrown away while the music is
// still on disk.
func (k *Keeper) Reconcile(ctx context.Context) (Survey, error) {
	k.mu.Lock()
	defer k.mu.Unlock()

	if k.root == "" {
		return Survey{}, nil
	}
	survey, err := k.rec.Survey(k.root)
	if err != nil {
		return survey, err
	}

	// A file somebody deleted is forgotten rather than chased.
	if len(survey.Gone) > 0 {
		for _, rel := range survey.Gone {
			k.rec.Remove(rel)
		}
		if err := k.rec.Save(k.dataDir); err != nil {
			return survey, err
		}
	}

	if k.rec.Len() == 0 {
		// Nothing has ever been kept here. Adding an empty folder to the library
		// would be a data source that describes nothing.
		return survey, nil
	}

	added, err := k.reg.EnsureFolder(ctx, k.root)
	if err != nil {
		return survey, err
	}
	if added {
		// The library had forgotten the folder. Adding it scans it, which is
		// exactly the re-registration this is for.
		return survey, nil
	}
	return survey, nil
}

// copyInto writes src to dst through a temporary file in the same directory.
//
// The rename is what makes a half-copied track impossible to see: the scanner,
// the survey and a file manager all only ever meet the finished file. The
// temporary name ends in .part, which IsAudio does not match, so an interrupted
// copy is not reported as somebody else's music either.
func copyInto(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	tmp := dst + ".part"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	// Flushed before the rename: a rename that beats the data to the disk is how
	// a power cut leaves a file of the right name and the wrong length.
	if err := out.Sync(); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
