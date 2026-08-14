package prefs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"daemonlord.ygg/madplayer/internal/queue"
)

func items(titles ...string) []*queue.Item {
	out := make([]*queue.Item, len(titles))
	for i, t := range titles {
		out[i] = &queue.Item{Title: t, Path: "/music/" + t + ".flac", Artist: "Somebody"}
	}
	return out
}

func TestTheQueueSurvivesARoundTrip(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	want := QueueState{
		Items:    items("one", "two", "three"),
		Original: items("three", "one", "two"),
		Index:    1,
		Shuffled: true,
		Repeat:   int(queue.RepeatAll),
		Position: 42.5,
	}
	if err := s.SaveQueue(want); err != nil {
		t.Fatal(err)
	}

	got, err := s.LoadQueue()
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 3 || got.Items[0].Title != "one" {
		t.Errorf("items = %v", got.Items)
	}
	if len(got.Original) != 3 || got.Original[0].Title != "three" {
		t.Errorf("the un-shuffled order was lost: %v", got.Original)
	}
	if got.Index != 1 || !got.Shuffled || got.Repeat != int(queue.RepeatAll) || got.Position != 42.5 {
		t.Errorf("state = %+v", got)
	}
}

// A first run has no file, and that is not an error to report to anybody.
func TestNoSavedQueueIsAFirstRun(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	got, err := s.LoadQueue()
	if err != nil {
		t.Fatalf("a missing queue file was an error: %v", err)
	}
	if len(got.Items) != 0 {
		t.Errorf("items = %v, want none", got.Items)
	}
}

// Clearing the queue is something people do to be rid of it. Leaving the last
// state on disk to be found at the next launch is not what they asked for.
func TestClearingTheQueueRemovesTheFile(t *testing.T) {
	dir := t.TempDir()
	s := &Store{Dir: dir}
	if err := s.SaveQueue(QueueState{Items: items("one")}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveQueue(QueueState{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "queue.json")); !os.IsNotExist(err) {
		t.Errorf("the queue file survived being cleared (err=%v)", err)
	}
	// And removing what is already gone is not an error either.
	if err := s.SaveQueue(QueueState{}); err != nil {
		t.Errorf("clearing an already-empty queue returned %v", err)
	}
}

// The file can be hand-edited, or written by another build. An index that names
// no track must not come back as one.
func TestAnImpossibleIndexIsClamped(t *testing.T) {
	dir := t.TempDir()
	s := &Store{Dir: dir}
	if err := os.WriteFile(filepath.Join(dir, "queue.json"),
		[]byte(`{"items":[{"title":"one","path":"/a.flac"}],"index":7,"position":-3}`), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := s.LoadQueue()
	if err != nil {
		t.Fatal(err)
	}
	if got.Index != 0 {
		t.Errorf("index = %d, want a clamp to 0", got.Index)
	}
	if got.Position != 0 {
		t.Errorf("position = %v, want a clamp to 0", got.Position)
	}
}

// Junk is reported, and the caller's answer is an empty queue rather than a
// program that will not start.
func TestAnUnreadableQueueIsAnError(t *testing.T) {
	dir := t.TempDir()
	s := &Store{Dir: dir}
	if err := os.WriteFile(filepath.Join(dir, "queue.json"), []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadQueue()
	if err == nil {
		t.Fatal("a corrupt queue file was accepted")
	}
	if len(got.Items) != 0 {
		t.Errorf("items = %v, want none", got.Items)
	}
}

// The queue is written every few seconds while music plays. Sharing a file with
// the API tokens would rewrite the credential file at that rate, and a corrupt
// queue would cost the sign-ins.
func TestTheQueueIsNotInTheSettingsFile(t *testing.T) {
	dir := t.TempDir()
	s := &Store{Dir: dir}
	if err := s.Save(Config{Volume: 1, Servers: []Server{{Base: "https://x", Token: "secret"}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveQueue(QueueState{Items: items("one")}); err != nil {
		t.Fatal(err)
	}

	cfg, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(cfg), "\"items\"") {
		t.Error("the queue was written into config.json")
	}
	q, err := os.ReadFile(filepath.Join(dir, "queue.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(q), "secret") {
		t.Error("an API token was written into the queue file")
	}
}
