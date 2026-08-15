package probe_test

import (
	"encoding/json"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"daemonlord.ygg/madplayer/internal/probe"
)

// The oracle: do these readers report what ffprobe reports?
//
// The hermetic tests state what each layout means. This states that the meaning
// is the same one ffprobe assigns, which is the only thing that matters — these
// columns are compared with a server's for the same file, and a server fills
// them with ffprobe. Where the two disagree it is this that is wrong.
//
// Env-gated on a real music directory, and skipped without ffprobe, which is
// not a build dependency of this program.
func TestAgreesWithFfprobe(t *testing.T) {
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not installed; nothing to compare against")
	}
	dir := os.Getenv("MADPLAYER_FPCALC_CORPUS")
	if dir == "" {
		t.Skip("set MADPLAYER_FPCALC_CORPUS to a directory of audio files")
	}

	var found int
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !known(path) {
			return nil
		}
		found++
		t.Run(filepath.Base(path), func(t *testing.T) {
			got, err := probe.Inspect(path)
			if err != nil {
				t.Fatalf("Inspect: %v", err)
			}
			want := ffprobe(t, path)

			if got.Codec != want.Codec {
				t.Errorf("Codec = %q, ffprobe %q", got.Codec, want.Codec)
			}
			if got.SampleRate != want.SampleRate {
				t.Errorf("SampleRate = %d, ffprobe %d", got.SampleRate, want.SampleRate)
			}
			if got.Channels != want.Channels {
				t.Errorf("Channels = %d, ffprobe %d", got.Channels, want.Channels)
			}
			if got.BitDepth != want.BitDepth {
				t.Errorf("BitDepth = %d, ffprobe %d", got.BitDepth, want.BitDepth)
			}
			// A millisecond of slack on the duration: both sides round, and
			// nothing uses the number more finely than that.
			if math.Abs(got.DurationSeconds-want.DurationSeconds) > 0.001 {
				t.Errorf("Duration = %v, ffprobe %v", got.DurationSeconds, want.DurationSeconds)
			}
			// An MP3's bitrate is EXACT, because ffmpeg's own formula is
			// reproduced rather than approximated — and slack here hid a real
			// mistake once, a rate divided by the padded duration instead of the
			// unpadded one, which came to 37 bps and passed a 0.1% tolerance.
			// Everything else is derived from the file's size, where ffprobe may
			// be counting something slightly different.
			tolerance := float64(want.Bitrate) / 1000
			if strings.EqualFold(filepath.Ext(path), ".mp3") {
				tolerance = 0
			}
			if want.Bitrate > 0 && math.Abs(float64(got.Bitrate-want.Bitrate)) > tolerance {
				t.Errorf("Bitrate = %d, ffprobe %d", got.Bitrate, want.Bitrate)
			}
			t.Logf("%s %gs %dbps %dHz %dch depth=%d", got.Codec, got.DurationSeconds,
				got.Bitrate, got.SampleRate, got.Channels, got.BitDepth)
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	if found == 0 {
		t.Fatalf("no audio under %s", dir)
	}
}

func known(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mp3", ".flac", ".wav", ".ogg", ".oga", ".opus", ".m4a", ".mp4":
		return true
	}
	return false
}

// ffprobe reads the same file the way madshare's media.ProbeTech does, so the
// comparison is against the exact numbers a server would store.
func ffprobe(t *testing.T, path string) probe.Info {
	t.Helper()
	out, err := exec.Command("ffprobe", "-v", "quiet", "-print_format", "json",
		"-show_streams", "-show_format", path).Output()
	if err != nil {
		t.Fatalf("ffprobe: %v", err)
	}
	var parsed struct {
		Streams []struct {
			CodecType        string `json:"codec_type"`
			CodecName        string `json:"codec_name"`
			SampleRate       string `json:"sample_rate"`
			Channels         int    `json:"channels"`
			BitsPerSample    int    `json:"bits_per_sample"`
			BitsPerRawSample string `json:"bits_per_raw_sample"`
			BitRate          string `json:"bit_rate"`
			Duration         string `json:"duration"`
		} `json:"streams"`
		Format struct {
			Duration string `json:"duration"`
			BitRate  string `json:"bit_rate"`
		} `json:"format"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("ffprobe output: %v", err)
	}

	var info probe.Info
	for _, s := range parsed.Streams {
		if s.CodecType != "audio" {
			continue
		}
		info.Codec = s.CodecName
		info.Channels = s.Channels
		info.SampleRate = atoi(s.SampleRate)
		if d := atoi(s.BitsPerRawSample); d > 0 {
			info.BitDepth = d
		} else {
			info.BitDepth = s.BitsPerSample
		}
		info.Bitrate = atoi(s.BitRate)
		info.DurationSeconds = atof(s.Duration)
		break
	}
	if info.DurationSeconds == 0 {
		info.DurationSeconds = atof(parsed.Format.Duration)
	}
	if info.Bitrate == 0 {
		info.Bitrate = atoi(parsed.Format.BitRate)
	}
	return info
}

func atoi(s string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

func atof(s string) float64 {
	f, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return f
}
