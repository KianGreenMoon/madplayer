package materialize

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func name(t *testing.T, tr Track, technical bool) string {
	t.Helper()
	got, err := tr.Name(technical)
	if err != nil {
		t.Fatalf("Name: %v", err)
	}
	return got
}

// The layout the design fixes: Artist/Album/NN - Title.ext.
func TestTheOrdinaryName(t *testing.T) {
	got := name(t, Track{
		Artist: "Jean Michel Jarre",
		Album:  "Metamorphoses",
		Title:  "Rendez-Vous",
		Number: 5,
		Ext:    ".flac",
	}, false)
	want := filepath.Join("Jean Michel Jarre", "Metamorphoses", "05 - Rendez-Vous.flac")
	if got != want {
		t.Errorf("name = %q, want %q", got, want)
	}
}

// A missing track number drops the prefix rather than inventing a position: the
// row's place in a list is a fact about the list, not about the file.
func TestNoTrackNumberMeansNoPrefix(t *testing.T) {
	got := name(t, Track{Artist: "A", Album: "B", Title: "Solo", Ext: ".mp3"}, false)
	if filepath.Base(got) != "Solo.mp3" {
		t.Errorf("name = %q, want no number prefix", got)
	}
}

// The empty cases are the server's own buckets, so the folder on disk and the
// row in the library agree about what an untagged track is called.
func TestEmptyTagsUseTheServersBuckets(t *testing.T) {
	got := name(t, Track{Title: "Nameless", Ext: ".ogg"}, false)
	want := filepath.Join("Unknown artist", "Other", "Nameless.ogg")
	if got != want {
		t.Errorf("name = %q, want %q", got, want)
	}
}

// A title that sanitises away entirely still has to be a file.
func TestATitleOfNothingButPunctuationStillGetsAName(t *testing.T) {
	got := name(t, Track{Artist: "?", Album: "///", Title: `"*"`, Ext: ".mp3"}, false)
	for i, part := range strings.Split(got, string(filepath.Separator)) {
		if part == "" || strings.Trim(part, "_. ") == "" {
			t.Errorf("component %d of %q is empty or all filler", i, got)
		}
	}
}

// The refused set is FAT's, not ext4's, because the reason this runs on a phone
// is the card in it — and a name that works on the laptop and not on the card
// fails where it is hardest to debug.
func TestTheStrictCharacterSetIsRefused(t *testing.T) {
	got := name(t, Track{
		Artist: `AC/DC`,
		Album:  `Back: In "Black" <1980>`,
		Title:  `Who|What?*Where\Why`,
		Ext:    ".mp3",
	}, false)
	for _, bad := range []string{"/DC", ":", `"`, "<", ">", "|", "?", "*", `\`} {
		// The separator itself is fine; the point is that none survived INSIDE a
		// component, which is what splitting on it proves.
		for _, part := range strings.Split(got, string(filepath.Separator)) {
			if strings.Contains(part, bad) {
				t.Errorf("component %q still carries %q", part, bad)
			}
		}
	}
	if strings.Contains(got, "__") {
		t.Errorf("name = %q — runs of the replacement were not collapsed", got)
	}
}

// A leading dot hides the folder on every unix desktop, which would hide
// somebody's music from them. A trailing dot or space is what FAT refuses.
func TestNamesDoNotStartOrEndWithDotsOrSpaces(t *testing.T) {
	got := name(t, Track{Artist: ".hidden", Album: "trailing. ", Title: " spaced ", Ext: ".mp3"}, false)
	for _, part := range strings.Split(got, string(filepath.Separator)) {
		if strings.HasPrefix(part, ".") || strings.HasPrefix(part, " ") {
			t.Errorf("component %q starts with a dot or space", part)
		}
		stem := strings.TrimSuffix(part, filepath.Ext(part))
		if strings.HasSuffix(stem, ".") || strings.HasSuffix(stem, " ") {
			t.Errorf("component %q ends with a dot or space", part)
		}
	}
}

// The limit that bites is bytes, not characters, and a truncated multi-byte rune
// is invalid UTF-8 — which every file manager renders as garbage.
func TestLongNamesAreCutOnACharacterBoundary(t *testing.T) {
	long := strings.Repeat("Хозяин леса ", 40) // Cyrillic: two bytes a letter
	got := name(t, Track{Artist: long, Album: long, Title: long, Ext: ".flac"}, false)

	for _, part := range strings.Split(got, string(filepath.Separator)) {
		if len(part) > maxComponent {
			t.Errorf("component is %d bytes, over the %d-byte budget", len(part), maxComponent)
		}
		if !utf8Valid(part) {
			t.Errorf("component %q is not valid UTF-8 — a rune was cut in half", part)
		}
	}
}

func utf8Valid(s string) bool { return strings.ToValidUTF8(s, "�") == s }

// Windows still refuses the DOS device names. A track called "Con" is not
// common and is not impossible.
func TestReservedNamesFallBackToTheBucket(t *testing.T) {
	got := name(t, Track{Artist: "NUL", Album: "COM1", Title: "fine", Ext: ".mp3"}, false)
	parts := strings.Split(got, string(filepath.Separator))
	if parts[0] != "Unknown artist" || parts[1] != "Other" {
		t.Errorf("name = %q, want the reserved components replaced by the buckets", got)
	}
}

// Technical names are the escape hatch for a filesystem that cannot take the
// human ones, so they must contain no tag text at all.
func TestTechnicalNamesCarryNoTagText(t *testing.T) {
	hash := "d40036200102abcdef"
	got := name(t, Track{Artist: "AC/DC", Album: "Back", Title: "Who?", Hash: hash, Ext: ".mp3"}, true)

	if want := filepath.Join("d4", hash+".mp3"); got != want {
		t.Errorf("name = %q, want %q", got, want)
	}
	for _, tag := range []string{"AC", "DC", "Back", "Who"} {
		if strings.Contains(got, tag) {
			t.Errorf("technical name %q still carries the tag %q", got, tag)
		}
	}
}

// The decoders pick by extension, so a file without one is unplayable —
// refusing beats writing something nothing can open.
func TestNoExtensionIsRefused(t *testing.T) {
	if _, err := (Track{Title: "x"}).Name(false); err == nil {
		t.Error("a track with no extension was given a name")
	}
	if _, err := (Track{Title: "x", Hash: "ab", Ext: "."}).Name(true); err == nil {
		t.Error("a bare dot was accepted as an extension")
	}
	// An extension without its dot is somebody being reasonable, not an error.
	got, err := (Track{Title: "x", Ext: "MP3"}).Name(false)
	if err != nil || filepath.Ext(got) != ".mp3" {
		t.Errorf("name = %q err = %v, want a normalised .mp3", got, err)
	}
}

// Only on collision: the ordinary file keeps the ordinary name.
func TestTheHashTagGoesBeforeTheExtension(t *testing.T) {
	tr := Track{Hash: "a1b2c3d4e5", Title: "x", Ext: ".flac"}
	got := tr.WithHashTag(filepath.Join("Artist", "Album", "05 - Title.flac"))
	want := filepath.Join("Artist", "Album", "05 - Title [a1b2c3].flac")
	if got != want {
		t.Errorf("tagged = %q, want %q", got, want)
	}
	// Nothing to tag with is not a crash.
	if bare := (Track{}).WithHashTag("x.mp3"); bare != "x.mp3" {
		t.Errorf("tagged = %q with no hash, want the name unchanged", bare)
	}
}

// The music directory is LOCALISED. This program was written on a machine whose
// music lives in ~/Musik, so hardcoding ~/Music would have been wrong on the
// developer's own laptop.
func TestTheMusicDirectoryIsReadFromTheDesktop(t *testing.T) {
	home := t.TempDir()
	cfg := filepath.Join(home, ".config")
	if err := os.MkdirAll(cfg, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "XDG_DESKTOP_DIR=\"$HOME/Schreibtisch\"\nXDG_MUSIC_DIR=\"$HOME/Musik\"\n"
	if err := os.WriteFile(filepath.Join(cfg, "user-dirs.dirs"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	if got, want := xdgMusicDir(home), filepath.Join(home, "Musik"); got != want {
		t.Errorf("music dir = %q, want %q", got, want)
	}

	// An absolute path is taken as it is; anything else is treated as absent
	// rather than guessed at, because this file is shell-ish and is not shell.
	if err := os.WriteFile(filepath.Join(cfg, "user-dirs.dirs"), []byte(`XDG_MUSIC_DIR="/mnt/media/music"`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := xdgMusicDir(home); got != "/mnt/media/music" {
		t.Errorf("music dir = %q, want the absolute path", got)
	}

	if err := os.WriteFile(filepath.Join(cfg, "user-dirs.dirs"), []byte(`XDG_MUSIC_DIR="$(rm -rf /)"`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := xdgMusicDir(home); got != "" {
		t.Errorf("music dir = %q, want nothing for an expansion we do not do", got)
	}
}

// Permissions on a phone depend on grants no mode bit describes, so the only
// honest test is to try.
func TestWritableProbesRatherThanReasons(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "madplayer")
	if err := Writable(dir); err != nil {
		t.Fatalf("a writable folder was refused: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("the folder was not created: %v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("the probe left %d file(s) behind", len(entries))
	}

	if err := Writable(""); err == nil {
		t.Error("an empty folder was accepted")
	}

	readonly := filepath.Join(t.TempDir(), "ro")
	if err := os.Mkdir(readonly, 0o500); err != nil {
		t.Fatal(err)
	}
	if err := Writable(filepath.Join(readonly, "sub")); err == nil {
		t.Error("a folder that cannot be created was accepted")
	}
}
