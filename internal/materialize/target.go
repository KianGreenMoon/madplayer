// Package materialize decides where music pulled off the network is kept, and
// what it is called there.
//
// The rule it implements is docs/ui/madplayer.md §"Where the bytes live",
// settled 2026-08-15: the destination is a folder madplayer MANAGES —
// `<music dir>/madplayer` by default — laid out `Artist/Album/NN - Title.ext`
// from the tags. That answers "which of the scanned folders does it go in?" by
// not asking it, and it keeps the music inside the music directory rather than
// hiding it in application storage, which is the whole point of that section: a
// track you pulled off the network should sit next to the music you already had,
// browsable, backup-able, indistinguishable from the rest.
//
// Nothing here writes anything or knows what a library is. It answers two
// questions — where is the folder, and what is this file called inside it — so
// both can be tested against a filesystem that does not exist.
package materialize

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// DirName is the managed folder's name inside the music directory. It is the
// program's name on purpose: somebody browsing their music should be able to
// tell at a glance which folder is theirs and which one this program writes to.
const DirName = "madplayer"

// maxComponent bounds one path component, in BYTES.
//
// The limit that bites is the filesystem's per-name limit — 255 bytes on ext4
// and most others, and a *byte* limit rather than a character one, so a Cyrillic
// or CJK title reaches it in a third of the characters an English one does. 120
// leaves room for a hash suffix and an extension without ever coming close.
const maxComponent = 120

// hashTag is how many hex characters of the content hash go into a name — long
// enough that two different recordings colliding is not a thing that happens,
// short enough to read.
const hashTag = 6

// MusicDir is the platform's music directory.
//
// On freedesktop it is XDG_MUSIC_DIR out of ~/.config/user-dirs.dirs, which is
// LOCALISED: this program was written on a machine whose music lives in ~/Musik,
// so hardcoding ~/Music would have been wrong on the developer's own laptop.
// The fallback is ~/Music, which is right where the file is absent and harmless
// where it is not, since the folder is created on demand either way.
func MusicDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	if dir := xdgMusicDir(home); dir != "" {
		return dir
	}
	return filepath.Join(home, "Music")
}

// xdgMusicDir reads XDG_MUSIC_DIR out of the user-dirs file, or "".
//
// The file is shell-ish (`XDG_MUSIC_DIR="$HOME/Musik"`) but it is not shell, and
// running it would be absurd — so exactly the one expansion it uses is handled,
// and anything else is treated as absent rather than guessed at.
func xdgMusicDir(home string) string {
	f, err := os.Open(filepath.Join(home, ".config", "user-dirs.dirs"))
	if err != nil {
		return ""
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		rest, ok := strings.CutPrefix(line, "XDG_MUSIC_DIR=")
		if !ok {
			continue
		}
		rest = strings.Trim(rest, `"`)
		switch {
		case strings.HasPrefix(rest, "$HOME/"):
			return filepath.Join(home, strings.TrimPrefix(rest, "$HOME/"))
		case strings.HasPrefix(rest, "/"):
			return rest
		}
		return ""
	}
	return ""
}

// DefaultRoot is where materialized music goes unless a setting says otherwise.
func DefaultRoot() string {
	if dir := MusicDir(); dir != "" {
		return filepath.Join(dir, DirName)
	}
	return ""
}

// Resolve names the managed folder: the setting, or `<music dir>/madplayer`, or
// — when there is no music directory to speak of — one inside the app's own data
// directory. The second result reports that the folder was CHOSEN rather than
// defaulted, which decides what happens if it turns out not to be writable.
//
// It touches no disk. That is deliberate and it is not a micro-optimisation: an
// earlier version probed here, and probing means creating, which meant every
// launch — and every test run — left an empty madplayer folder in somebody's
// music directory whether or not they ever kept a single track. Nothing is
// created until something is actually kept.
func Resolve(setting, dataDir string) (root string, explicit bool) {
	if setting = strings.TrimSpace(setting); setting != "" {
		return setting, true
	}
	if def := DefaultRoot(); def != "" {
		return def, false
	}
	return filepath.Join(dataDir, DirName), false
}

// Track is the little a name needs to know. It is a plain struct rather than a
// library row so this package can be tested with no library.
type Track struct {
	Artist string
	Album  string
	Title  string
	// Number is the track's tag, or 0 when it has none. A missing number drops
	// the "NN - " prefix rather than inventing a position: the row's position in
	// a list is a fact about the list, not about the file.
	Number int
	// Hash is the content hash. It names the file under technical names, and
	// breaks a collision under human ones.
	Hash string
	// Ext is the container's extension, WITH the dot. The decoders pick by it,
	// so a file without one is unplayable and Name refuses rather than writing it.
	Ext string
}

// Name is the path of this track relative to the managed folder.
//
// technical switches to hash-names — the escape hatch for a filesystem that
// cannot take the human ones. It is a setting rather than an automatic fallback
// because a program that silently changed its own layout halfway through a
// collection would be worse than one that asked.
func (t Track) Name(technical bool) (string, error) {
	ext := strings.ToLower(strings.TrimSpace(t.Ext))
	if ext != "" && !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}
	if ext == "" || ext == "." {
		return "", fmt.Errorf("%q has no file extension, and the decoders pick by one", t.Title)
	}

	if technical {
		if len(t.Hash) < 2 {
			return "", fmt.Errorf("technical names need a content hash, and %q has none", t.Title)
		}
		// Sharded by the first two characters. A flat directory of ten thousand
		// files is fine on ext4 and miserable on the FAT card this option exists
		// for, and the shard costs nothing on either.
		return filepath.Join(t.Hash[:2], t.Hash+ext), nil
	}

	artist := component(t.Artist, defaultArtist)
	album := component(t.Album, defaultAlbum)
	title := component(t.Title, "Untitled")
	if t.Number > 0 {
		title = fmt.Sprintf("%02d - %s", t.Number, title)
	}
	return filepath.Join(artist, album, truncate(title, maxComponent-len(ext))+ext), nil
}

// WithHashTag is the same name with the content hash worked in, which is what a
// collision with DIFFERENT audio gets. Only on collision: the ordinary file
// keeps the ordinary name.
func (t Track) WithHashTag(name string) string {
	if len(t.Hash) < hashTag {
		return name
	}
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	return stem + " [" + t.Hash[:hashTag] + "]" + ext
}

// The empty cases are the server's own buckets, not placeholders invented here —
// otherwise the folder on disk and the row in the library would disagree about
// what an untagged track is called. They mirror database.DefaultArtistName and
// DefaultAlbumTitle; the doc that fixes them is
// docs/architecture/artist-album-model.md.
const (
	defaultArtist = "Unknown artist"
	defaultAlbum  = "Other"
)

// component turns one piece of tag text into one path component.
//
// The character set it refuses is the STRICT one — what FAT will take, not what
// ext4 will — because the reason this program runs on a phone is the SD card in
// it, and a name that works on the laptop and not on the card is a name that
// fails where it is hardest to debug.
func component(text, fallback string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(text) {
		switch {
		case r < 0x20 || r == 0x7f:
			// Control characters, including the newline a broken tag can carry.
			b.WriteRune('_')
		case strings.ContainsRune(`<>:"/\|?*`, r):
			b.WriteRune('_')
		default:
			b.WriteRune(r)
		}
	}

	out := collapse(b.String())
	// Windows and FAT both refuse a name ending in a dot or a space, and a
	// LEADING dot hides the folder on every unix desktop — which would hide
	// somebody's music from them.
	out = strings.Trim(out, " .")
	out = truncate(out, maxComponent)
	out = strings.Trim(out, " .")

	// A component made ENTIRELY of filler is empty in every sense that matters:
	// `AC/DC` sanitising to `AC_DC` is a name, but `***` sanitising to `_` is a
	// folder called nothing. The design says the empty cases get the buckets
	// rather than an invented placeholder, and this is one of the empty cases.
	if strings.Trim(out, "_ .") == "" || isReserved(out) {
		return fallback
	}
	return out
}

// collapse squeezes runs of the replacement character, so a title made mostly of
// punctuation does not become a row of underscores.
func collapse(s string) string {
	var b strings.Builder
	prev := rune(0)
	for _, r := range s {
		if r == '_' && prev == '_' {
			continue
		}
		b.WriteRune(r)
		prev = r
	}
	return b.String()
}

// truncate cuts to a BYTE budget without splitting a character in half — a
// truncated multi-byte rune is invalid UTF-8, and a filename that is invalid
// UTF-8 displays as garbage in every file manager.
func truncate(s string, max int) string {
	if max < 1 {
		max = 1
	}
	if len(s) <= max {
		return s
	}
	cut := s[:max]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return strings.TrimRight(cut, " .")
}

// isReserved names the DOS device names, which Windows still refuses as
// filenames with or without an extension. A track called "Con" is not common and
// is not impossible.
func isReserved(name string) bool {
	stem, _, _ := strings.Cut(name, ".")
	switch strings.ToUpper(strings.TrimRight(stem, " ")) {
	case "CON", "PRN", "AUX", "NUL",
		"COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		return true
	}
	return false
}

// Writable reports whether a directory can be created and written in.
//
// It PROBES rather than reasoning about permissions: the answer on Android
// depends on storage volumes and grants that no mode bit describes, and the only
// honest test is to try. The probe file is removed; a directory that had to be
// created is left, since the caller is about to use it.
func Writable(dir string) error {
	if dir == "" {
		return fmt.Errorf("no folder chosen")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	probe := filepath.Join(dir, ".madplayer-write-test")
	f, err := os.OpenFile(probe, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	f.Close()
	return os.Remove(probe)
}

// IsAudio reports whether a name looks like music this program would have put
// somewhere. It is used to decide what counts as a stray file in the managed
// folder, so it follows the SCANNER's list rather than the decoders' — a file
// madplayer cannot play is still a file madplayer may have written.
func IsAudio(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".mp3", ".flac", ".wav", ".ogg", ".oga", ".opus", ".m4a", ".mp4", ".aac", ".wma", ".aiff", ".aif":
		return true
	}
	return false
}
