// Package demo feeds the spike its rows. It exists because the toolkit decision
// was made by rendering identical data in two toolkits, so that nothing but the
// UI layer could differ; it outlives that comparison as the client's text and
// scroll load test.
//
// Two modes:
//
//	MADPLAYER_BASE set   → real rows from a running madshare over HTTP
//	                       (MADPLAYER_TOKEN optional; without it you get the
//	                       anonymous guest listing, which is usually empty)
//	unset                → generated fixtures, MADPLAYER_ROWS rows (default 5000)
//
// The fixture mode is not a shortcut, it is the point: the judgement here is
// scroll feel and text rendering at a size no real dev library reaches, with
// scripts a Latin-only corpus would never exercise.
package demo

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"time"

	"daemonlord.ygg/madplayer/internal/madshare"
)

// Load returns the rows to render plus a one-line description of where they came
// from, which both spikes show in the header — a spike that silently falls back
// to fixtures would have you judging a toolkit on the wrong data.
func Load(ctx context.Context) ([]madshare.Track, string) {
	base := os.Getenv("MADPLAYER_BASE")
	if base == "" {
		n := rows()
		return fixtures(n), fmt.Sprintf("fixtures · %d tracks · set MADPLAYER_BASE to use a real server", n)
	}

	c := madshare.New(base, os.Getenv("MADPLAYER_TOKEN"))
	who := "anonymous (guest listing)"
	if id, err := c.Me(ctx); err == nil {
		who = id.Username
	}

	tracks, err := walk(ctx, c)
	if err != nil {
		n := rows()
		return fixtures(n), fmt.Sprintf("%s UNREACHABLE (%v) · falling back to %d fixtures", base, err, n)
	}
	return tracks, fmt.Sprintf("%s · %s · %d tracks", base, who, len(tracks))
}

func rows() int {
	if n, err := strconv.Atoi(os.Getenv("MADPLAYER_ROWS")); err == nil && n > 0 {
		return n
	}
	return 5000
}

// walk drills artist → album → tracks to build a flat list. That is a spike
// shape, not a client shape: the real client drills on demand, one level per
// screen. It is here because a flat list is what a scroll test needs.
func walk(ctx context.Context, c *madshare.Client) ([]madshare.Track, error) {
	artists, err := c.Artists(ctx)
	if err != nil {
		return nil, err
	}
	var out []madshare.Track
	for _, a := range artists {
		albums, err := c.Albums(ctx, a.ID)
		if err != nil {
			return nil, err
		}
		for _, al := range albums {
			tracks, err := c.Tracks(ctx, al.ID)
			if err != nil {
				return nil, err
			}
			for _, t := range tracks {
				// /api/tracks does not carry the album title (the caller asked
				// for one album), but a flat list has to say which.
				if t.AlbumTitle == "" {
					t.AlbumTitle = al.Title
				}
				out = append(out, t)
			}
		}
	}
	return out, nil
}

// The corpus is deliberately multi-script. Text rendering is one of the four
// things being judged, and Latin-only names would hide the differences that
// actually matter: shaping, fallback fonts, and whether RTL is handled at all.
var artists = []string{
	"Sigur Rós", "Björk", "Mötley Crüe", "Édith Piaf", "Antônio Carlos Jobim",
	"СпЛиН", "Кино", "Аквариум", "Ноль", "Noize MC",
	"坂本龍一", "宇多田ヒカル", "王菲", "김광석", "少年ナイフ",
	"Ελευθερία Αρβανιτάκη", "فيروز", "أم كلثوم", "עידן רייכל", "लता मंगेशकर",
	"Godspeed You! Black Emperor", "…And You Will Know Us by the Trail of Dead",
	"múm", "Ólafur Arnalds", "Dakh Daughters", "Zdob și Zdub",
	"The Album Leaf", "Boards of Canada", "Autechre", "Aphex Twin",
}

var albums = []string{
	"Ágætis byrjun", "Homogenic", "Разное", "残響", "Geogaddi",
	"Untitled #3", "Selected Ambient Works 85–92", "Дом с привидениями",
	"Впередсмотрящий", "花樣年華", "Lifted or The Story Is in the Soil",
	"L'Hymne à l'amour", "Ísland", "Tri Repetae", "Kid A-side",
}

var titles = []string{
	"Svefn-g-englar", "Jóga", "Выхода нет", "Sunshine ☀️ (Remix)",
	"Roygbiv", "Windowlicker", "Non, je ne regrette rien", "Corcovado",
	"戦場のメリークリスマス", "First Love", "夢中人", "서른 즈음에",
	"Το τραγούδι της ξενιτιάς", "زهرة المدائن", "ממעמקים",
	"The Dead Flag Blues (a very long title that will not fit in a narrow column and must elide)",
	"Nightswimming", "Blue Monday", "Одинокая птица", "Твой звонок",
	"Untitled", "—", "Track 07", "Интро", "Аутро",
}

// fixtures builds n deterministic rows: the same seed means both spikes render
// byte-identical text, so a difference on screen is the toolkit, never the data.
func fixtures(n int) []madshare.Track {
	rnd := rand.New(rand.NewSource(20260808))
	out := make([]madshare.Track, 0, n)
	for i := 0; i < n; i++ {
		dur := float64(90 + rnd.Intn(500))
		trackNo := int64(i%18 + 1)
		discNo := int64(i/18%2 + 1)
		t := madshare.Track{
			ID:          int64(i + 1),
			TagsetID:    int64(i + 1),
			Hash:        fmt.Sprintf("%064x", i),
			Title:       titles[rnd.Intn(len(titles))],
			ArtistName:  artists[rnd.Intn(len(artists))],
			AlbumTitle:  albums[rnd.Intn(len(albums))],
			TrackNumber: &trackNo,
			DiscNumber:  &discNo,
			MimeType:    "audio/flac",
		}
		// Leave a slice of rows with no duration: an unanalysed file is a real
		// state (no ffprobe at ingest) and the row has to render without one.
		if i%23 != 0 {
			t.Duration = &dur
		}
		t.URL = "/files/" + t.Hash + "/track.flac"
		out = append(out, t)
	}
	return out
}

// Elapsed drives the spikes' fake transport. Decoding audio is a real burden of
// the native client, but it is the SAME burden in both toolkits (oto/beep either
// way), so it cannot discriminate between them and is left out on purpose.
func Elapsed(start time.Time, total float64) float64 {
	if total <= 0 {
		return 0
	}
	e := time.Since(start).Seconds()
	if e > total {
		return total
	}
	return e
}
