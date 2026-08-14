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
// already adds what is new and skips what is unchanged by size and mtime, so
// "index only the files the library is missing" is a property that falls out of
// the existing machinery rather than a rule reimplemented here.
type Registrar interface {
	// EnsureFolder makes the managed folder a data source, adding it when the
	// library does not have it — which is also how a thrown-away database
	// recovers files that are still on disk.
	EnsureFolder(ctx context.Context, root string) (added bool, err error)
	// Register indexes what is new in it.
	Register(ctx context.Context, root string) error
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
	rec       *Record
	fetch     Fetcher
	reg       Registrar
}

// NewKeeper loads the record for a root and returns a keeper over it.
//
// A record that will not parse is reported and the keeper works anyway, with an
// empty one: the cost is that existing files look like strays, which is the safe
// direction.
func NewKeeper(dataDir, root string, technical bool, f Fetcher, r Registrar) (*Keeper, error) {
	rec, err := LoadRecord(dataDir, root)
	return &Keeper{dataDir: dataDir, root: root, technical: technical, rec: rec, fetch: f, reg: r}, err
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
	if added {
		// Adding a folder scans it. Asking for a second scan on top would be
		// asking the scanner to run twice at once, which it answers with an
		// error rather than a queue.
		return Result{Path: abs}, nil
	}
	if err := k.reg.Register(ctx, k.root); err != nil {
		return Result{Path: abs}, fmt.Errorf("saved %s, but it could not be indexed: %w", rel, err)
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
