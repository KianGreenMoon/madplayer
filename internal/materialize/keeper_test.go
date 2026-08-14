package materialize

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"daemonlord.ygg/madplayer/internal/queue"
)

// fakeFetch hands back a file that already exists, the way a download that has
// already landed in the cache does.
type fakeFetch struct {
	path string
	err  error
	// calls counts downloads, which is how "already there costs nothing" is
	// checked: an idempotent second Keep must not fetch again.
	calls int
}

func (f *fakeFetch) Local(context.Context, *queue.Item) (string, error) {
	f.calls++
	return f.path, f.err
}

type fakeReg struct {
	ensured, registered int
	added               bool
	err                 error
}

func (r *fakeReg) EnsureFolder(context.Context, string) (bool, error) {
	r.ensured++
	return r.added, r.err
}

func (r *fakeReg) Register(context.Context, string) error {
	r.registered++
	return r.err
}

func keeper(t *testing.T, technical bool) (*Keeper, *fakeFetch, *fakeReg, string) {
	t.Helper()
	data, root := t.TempDir(), filepath.Join(t.TempDir(), "madplayer")

	source := filepath.Join(t.TempDir(), "downloaded.flac")
	if err := os.WriteFile(source, []byte("the audio"), 0o644); err != nil {
		t.Fatal(err)
	}

	f, r := &fakeFetch{path: source}, &fakeReg{}
	k, err := NewKeeper(data, root, technical, f, r)
	if err != nil {
		t.Fatal(err)
	}
	return k, f, r, root
}

func remote(hash string) *queue.Item {
	return &queue.Item{URL: "https://elsewhere/" + hash, Hash: hash, Title: "Song"}
}

func track(hash string) Track {
	return Track{Artist: "Dakh Daughters", Album: "Air", Title: "Inshe Misto", Number: 3, Hash: hash, Ext: ".flac"}
}

func TestKeepWritesTheFileAndHandsItToTheLibrary(t *testing.T) {
	k, fetch, reg, root := keeper(t, false)

	got, err := k.Keep(context.Background(), track("aaa111"), remote("aaa111"))
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "Dakh Daughters", "Air", "03 - Inshe Misto.flac")
	if got.Path != want {
		t.Errorf("wrote %q, want %q", got.Path, want)
	}
	if got.Already {
		t.Error("a first keep reported the file as already there")
	}
	if b, err := os.ReadFile(want); err != nil || string(b) != "the audio" {
		t.Errorf("file = %q err = %v", b, err)
	}
	if fetch.calls != 1 || reg.ensured != 1 || reg.registered != 1 {
		t.Errorf("fetch=%d ensure=%d register=%d, want 1 each", fetch.calls, reg.ensured, reg.registered)
	}
}

// Materialize all exists, and it gets pressed twice.
func TestKeepingTheSameAudioTwiceIsANoOp(t *testing.T) {
	k, fetch, _, _ := keeper(t, false)
	ctx := context.Background()

	first, err := k.Keep(ctx, track("aaa111"), remote("aaa111"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := k.Keep(ctx, track("aaa111"), remote("aaa111"))
	if err != nil {
		t.Fatal(err)
	}
	if !second.Already || second.Path != first.Path {
		t.Errorf("second keep = %+v, want the first file reported as already there", second)
	}
	if fetch.calls != 1 {
		t.Errorf("downloaded %d times, want once — the second request had nothing to do", fetch.calls)
	}
}

// The same audio is the same audio even if the tags were edited in between, so
// the second request is recognised by content hash and not by name.
func TestRetaggedAudioIsStillRecognised(t *testing.T) {
	k, fetch, _, _ := keeper(t, false)
	ctx := context.Background()

	if _, err := k.Keep(ctx, track("aaa111"), remote("aaa111")); err != nil {
		t.Fatal(err)
	}

	retagged := track("aaa111")
	retagged.Artist, retagged.Title = "Дах Дотерс", "Інше Місто"
	got, err := k.Keep(ctx, retagged, remote("aaa111"))
	if err != nil {
		t.Fatal(err)
	}
	if !got.Already {
		t.Error("the same audio under new tags was written a second time")
	}
	if fetch.calls != 1 {
		t.Errorf("downloaded %d times, want once", fetch.calls)
	}
}

// Different audio landing on the same name takes the hash suffix — and only
// then, so the ordinary file keeps the ordinary name.
func TestACollisionWithDifferentAudioTakesTheHashSuffix(t *testing.T) {
	k, _, _, root := keeper(t, false)
	ctx := context.Background()

	if _, err := k.Keep(ctx, track("aaa111"), remote("aaa111")); err != nil {
		t.Fatal(err)
	}
	got, err := k.Keep(ctx, track("bbb222"), remote("bbb222"))
	if err != nil {
		t.Fatal(err)
	}

	plain := filepath.Join(root, "Dakh Daughters", "Air", "03 - Inshe Misto.flac")
	tagged := filepath.Join(root, "Dakh Daughters", "Air", "03 - Inshe Misto [bbb222].flac")
	if got.Path != tagged {
		t.Errorf("second file = %q, want %q", got.Path, tagged)
	}
	for _, p := range []string{plain, tagged} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s is missing: %v", p, err)
		}
	}
}

// A track this machine already holds is not network music, and copying somebody
// else's own file into a folder the program manages is the opposite of the point.
func TestALocalTrackIsRefused(t *testing.T) {
	k, fetch, _, _ := keeper(t, false)
	_, err := k.Keep(context.Background(), track("aaa"), &queue.Item{Path: "/music/mine.flac"})
	if !errors.Is(err, ErrNotRemote) {
		t.Errorf("err = %v, want ErrNotRemote", err)
	}
	if fetch.calls != 0 {
		t.Error("a local track was downloaded")
	}
}

// A write that failed is a fact about the disk. Refuse and report — no retry
// under a second name, because working around a broken disk is how a music
// folder gains files nobody meant.
func TestAFilesystemFailureRefusesAndReports(t *testing.T) {
	k, _, reg, root := keeper(t, false)

	// A read-only album directory: the NAME is free, so nothing about the
	// collision rule applies — the write itself is what cannot happen.
	dir := filepath.Join(root, "Dakh Daughters", "Air")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	_, err := k.Keep(context.Background(), track("aaa111"), remote("aaa111"))
	if err == nil {
		t.Fatal("a folder that cannot be written to was accepted")
	}
	if k.Kept() != 0 {
		t.Error("a failed keep was recorded as ours")
	}
	if reg.registered != 0 {
		t.Error("a failed keep still asked the library to index it")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("a failed keep left %d file(s) behind", len(entries))
	}
}

// Something else occupying the NAME is a collision, whatever it is — a file, a
// directory somebody made, a dangling symlink. The distinction that matters is
// name-taken (retry under the hash) versus write-failed (refuse), not what kind
// of thing is in the way.
func TestADirectoryInTheWayIsACollisionAndNotAFailure(t *testing.T) {
	k, _, _, root := keeper(t, false)

	blocked := filepath.Join(root, "Dakh Daughters", "Air", "03 - Inshe Misto.flac")
	if err := os.MkdirAll(blocked, 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := k.Keep(context.Background(), track("aaa111"), remote("aaa111"))
	if err != nil {
		t.Fatalf("a taken name was treated as a disk failure: %v", err)
	}
	if want := filepath.Join(root, "Dakh Daughters", "Air", "03 - Inshe Misto [aaa111].flac"); got.Path != want {
		t.Errorf("wrote %q, want %q", got.Path, want)
	}
}

// Technical names are the escape hatch for a filesystem that cannot take the
// human ones, so nothing about the tags may reach the path.
func TestTechnicalNamesWriteHashPaths(t *testing.T) {
	k, _, _, root := keeper(t, true)

	got, err := k.Keep(context.Background(), track("d40036abc"), remote("d40036abc"))
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "d4", "d40036abc.flac")
	if got.Path != want {
		t.Errorf("wrote %q, want %q", got.Path, want)
	}
	if strings.Contains(got.Path, "Dakh") {
		t.Errorf("a technical name carries tag text: %q", got.Path)
	}
}

// Somebody deleting a file they own is their right; the next request writes it
// again rather than reporting it as already there.
func TestADeletedFileIsWrittenAgain(t *testing.T) {
	k, fetch, _, _ := keeper(t, false)
	ctx := context.Background()

	first, err := k.Keep(ctx, track("aaa111"), remote("aaa111"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(first.Path); err != nil {
		t.Fatal(err)
	}

	again, err := k.Keep(ctx, track("aaa111"), remote("aaa111"))
	if err != nil {
		t.Fatal(err)
	}
	if again.Already {
		t.Error("a deleted file was reported as still there")
	}
	if fetch.calls != 2 {
		t.Errorf("downloaded %d times, want twice", fetch.calls)
	}
}

// An interrupted copy must not read as somebody else's music: the temporary
// name is not audio, so the survey never sees it.
func TestAnInterruptedCopyIsNotAStray(t *testing.T) {
	k, _, _, root := keeper(t, false)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "half.flac.part"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	survey, err := k.Reconcile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(survey.Strays) != 0 {
		t.Errorf("strays = %v, want none — a .part file is this program's own leavings", survey.Strays)
	}
}

// Reconcile forgets what has left the folder, and reports what somebody else
// put in it.
func TestReconcileForgetsTheGoneAndReportsTheStrays(t *testing.T) {
	k, _, reg, root := keeper(t, false)
	ctx := context.Background()

	kept, err := k.Keep(ctx, track("aaa111"), remote("aaa111"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(kept.Path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Somebody Elses.mp3"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	survey, err := k.Reconcile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(survey.Strays) != 1 || survey.Strays[0] != "Somebody Elses.mp3" {
		t.Errorf("strays = %v", survey.Strays)
	}
	if k.Kept() != 0 {
		t.Errorf("the record still claims %d file(s) that are gone", k.Kept())
	}
	// Nothing is ours any more, so there is no folder worth handing to the
	// library — a data source describing nothing is worse than none.
	if reg.ensured != 1 {
		t.Errorf("EnsureFolder called %d times during reconcile, want only the one from the keep", reg.ensured)
	}
}

// Reconcile before anything has been kept is the ordinary first run.
func TestReconcileOnAnEmptyFolder(t *testing.T) {
	k, _, reg, _ := keeper(t, false)
	survey, err := k.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("reconciling a folder that does not exist was an error: %v", err)
	}
	if len(survey.Ours)+len(survey.Strays)+len(survey.Gone) != 0 {
		t.Errorf("survey = %+v", survey)
	}
	if reg.ensured != 0 {
		t.Error("an empty folder was added to the library")
	}
}
