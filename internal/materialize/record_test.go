package materialize

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func touch(t *testing.T, root, rel string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestTheRecordSurvivesARoundTrip(t *testing.T) {
	data, root := t.TempDir(), "/music/madplayer"

	rec, err := LoadRecord(data, root)
	if err != nil {
		t.Fatal(err)
	}
	rec.Add("Artist/Album/01 - One.flac", "aaa111")
	rec.Add("Artist/Album/02 - Two.flac", "bbb222")
	if err := rec.Save(data); err != nil {
		t.Fatal(err)
	}

	back, err := LoadRecord(data, root)
	if err != nil {
		t.Fatal(err)
	}
	if back.Len() != 2 {
		t.Fatalf("record holds %d entries, want 2", back.Len())
	}
	if h, ok := back.HashAt("Artist/Album/01 - One.flac"); !ok || h != "aaa111" {
		t.Errorf("hash = %q ok = %v", h, ok)
	}
}

// A record describing a DIFFERENT folder comes back empty: the setting was
// changed, and those files are somewhere this program no longer manages.
func TestChangingTheFolderStartsAFreshRecord(t *testing.T) {
	data := t.TempDir()
	rec, _ := LoadRecord(data, "/music/madplayer")
	rec.Add("a.flac", "aaa")
	if err := rec.Save(data); err != nil {
		t.Fatal(err)
	}

	moved, err := LoadRecord(data, "/mnt/card/madplayer")
	if err != nil {
		t.Fatal(err)
	}
	if moved.Len() != 0 {
		t.Errorf("the new folder inherited %d entries from the old one", moved.Len())
	}
	if moved.Root != "/mnt/card/madplayer" {
		t.Errorf("root = %q", moved.Root)
	}
}

// A first run has no record, and that is not an error to report to anybody.
func TestNoRecordIsAFirstRun(t *testing.T) {
	rec, err := LoadRecord(t.TempDir(), "/music/madplayer")
	if err != nil {
		t.Fatalf("a missing record was an error: %v", err)
	}
	if rec.Len() != 0 {
		t.Errorf("record holds %d entries", rec.Len())
	}
}

// The same audio asked for twice is the same audio, even if the tags were
// edited in between and the second name would differ. This is what makes
// Materialize idempotent, which matters because Materialize all gets pressed
// twice.
func TestHoldsFindsTheAudioUnderAnyName(t *testing.T) {
	rec, _ := LoadRecord(t.TempDir(), "/root")
	rec.Add("Old Artist/Old Album/01 - Old Title.flac", "d40036")

	rel, ok := rec.Holds("d40036")
	if !ok || rel != "Old Artist/Old Album/01 - Old Title.flac" {
		t.Errorf("Holds = %q %v, want the path it was written at", rel, ok)
	}
	if _, ok := rec.Holds("nothing"); ok {
		t.Error("Holds found audio that was never written")
	}
	if _, ok := rec.Holds(""); ok {
		t.Error("an empty hash matched something")
	}
}

// The survey is what tells our files from somebody else's, which is the whole
// reason the record exists.
func TestTheSurveySortsOursFromStraysFromGone(t *testing.T) {
	data, root := t.TempDir(), t.TempDir()

	rec, _ := LoadRecord(data, root)
	rec.Add("Artist/Album/01 - Ours.flac", "aaa")
	rec.Add("Artist/Album/02 - Deleted.flac", "bbb")

	touch(t, root, "Artist/Album/01 - Ours.flac")
	touch(t, root, "Somebody Elses Song.mp3")   // a person dropped this in
	touch(t, root, "Artist/Album/cover.jpg")    // not audio: not a stray
	touch(t, root, "Artist/Album/.hidden.flac") // a file manager's leavings

	got, err := rec.Survey(root)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"Artist/Album/01 - Ours.flac"}; !reflect.DeepEqual(got.Ours, want) {
		t.Errorf("ours = %v, want %v", got.Ours, want)
	}
	if want := []string{"Somebody Elses Song.mp3"}; !reflect.DeepEqual(got.Strays, want) {
		t.Errorf("strays = %v, want %v", got.Strays, want)
	}
	if want := []string{"Artist/Album/02 - Deleted.flac"}; !reflect.DeepEqual(got.Gone, want) {
		t.Errorf("gone = %v, want %v", got.Gone, want)
	}
}

// The ordinary state before anything has been materialized is a folder that is
// not there, and the answer is "nothing in it" rather than an error.
func TestSurveyingAFolderThatIsNotThere(t *testing.T) {
	rec, _ := LoadRecord(t.TempDir(), "/nope")
	got, err := rec.Survey("/nope/does/not/exist")
	if err != nil {
		t.Fatalf("a missing folder was an error: %v", err)
	}
	if len(got.Ours)+len(got.Strays)+len(got.Gone) != 0 {
		t.Errorf("found %+v in a folder that does not exist", got)
	}
}

// When the record itself is lost, everything looks like somebody else's — so
// the program warns instead of silently adopting a stranger's music. The
// conservative direction is the one the design asks for.
func TestALostRecordMakesEverythingAStray(t *testing.T) {
	root := t.TempDir()
	touch(t, root, "Artist/Album/01 - Was Ours.flac")

	empty, _ := LoadRecord(t.TempDir(), root)
	got, err := empty.Survey(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Ours) != 0 {
		t.Errorf("ours = %v with no record, want none", got.Ours)
	}
	if len(got.Strays) != 1 {
		t.Errorf("strays = %v, want the one file", got.Strays)
	}
}

// Forgetting a file that has left the folder is how the record stays true.
func TestRemoveForgetsAPath(t *testing.T) {
	rec, _ := LoadRecord(t.TempDir(), "/root")
	rec.Add("a/b/c.flac", "aaa")
	rec.Remove("a/b/c.flac")
	if _, ok := rec.HashAt("a/b/c.flac"); ok {
		t.Error("the path was still ours after being removed")
	}
}

// Junk is reported rather than swallowed, and the caller carries on with an
// empty record — which makes every file a stray, which is the safe answer.
func TestAnUnreadableRecordIsAnError(t *testing.T) {
	data := t.TempDir()
	if err := os.WriteFile(filepath.Join(data, RecordFile), []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec, err := LoadRecord(data, "/root")
	if err == nil {
		t.Fatal("a corrupt record was accepted")
	}
	if rec == nil || rec.Len() != 0 {
		t.Error("a corrupt record did not come back empty and usable")
	}
}
