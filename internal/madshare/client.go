// Package madshare is the HTTP client for a madshare server's public API.
//
// It is deliberately a thin mirror of the JSON the server sends. Every rule that
// could be re-derived here — which names are artists, which file to play, sort
// order, disc grouping, availability — is decided server-side, and this package
// carries the answer rather than second-guessing it. The list is in
// docs/ui/madplayer.md §"What the server already computes"; re-deriving any of
// it produces a client that quietly disagrees with the web UI about what the
// library contains.
//
// The same client serves both levels of the client's ambition: against a remote
// server it is level 1, against the embedded backend on loopback it is level 2.
// Only the base URL differs.
package madshare

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Artist is a row of GET /api/artists. These are ALBUM artists — a performer who
// never fronts an album is a search hit, not a row here. Never build this list
// by grouping track rows: docs/ui/artists-and-performers.md.
type Artist struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	TrackCount int    `json:"track_count"`
	HasImage   bool   `json:"has_image"`
}

// ArtistPage is the paginated form of GET /api/artists, returned only when a
// limit is supplied. NextCursor is opaque: pass it back verbatim, never
// construct one.
type ArtistPage struct {
	Items      []Artist `json:"items"`
	NextCursor *string  `json:"next_cursor"`
}

// Album is a row of GET /api/albums?artist_id=.
type Album struct {
	ID         int64  `json:"id"`
	ArtistID   int64  `json:"artist_id"`
	Title      string `json:"title"`
	ArtistName string `json:"artist_name"`
	Year       *int64 `json:"year"`
	TrackCount int    `json:"track_count"`
	HasImage   bool   `json:"has_image"`
}

// Track is a row of GET /api/tracks?album_id= (and of the tracks half of
// /api/search, which additionally sets AlbumTitle).
//
// The listening identity is TagsetID — the appearance — which is what
// favourites, playlists and the renditions endpoint key on. Hash is the ORIGIN
// blob and exists for admin surfaces; a track whose origin blob is gone still
// plays, because URL resolves to a surviving rendition.
type Track struct {
	ID          int64    `json:"id"`
	TagsetID    int64    `json:"tagset_id"`
	Hash        string   `json:"hash"`
	Title       string   `json:"title"`
	ArtistName  string   `json:"artist_name"`
	AlbumTitle  string   `json:"album_title"`
	TrackNumber *int64   `json:"track_number"`
	DiscNumber  *int64   `json:"disc_number"`
	Duration    *float64 `json:"duration_seconds"`
	URL         string   `json:"url"`
	MimeType    string   `json:"mime_type"`
}

// SearchResults is the response of GET /api/search?q=.
type SearchResults struct {
	Artists []Artist `json:"artists"`
	Albums  []Album  `json:"albums"`
	Tracks  []Track  `json:"tracks"`
}

// Identity is the response of GET /api/auth/me.
//
// It is the ONLY question about who you are. The browse endpoints narrow rather
// than refuse — a caller without content.access gets the guest listing, same
// shape, no error — so an empty library is never evidence that sign-in failed.
type Identity struct {
	Username               string   `json:"username"`
	Permissions            []string `json:"permissions"`
	PasswordChangeRequired bool     `json:"password_change_required"`
}

// Has reports whether the identity holds a permission, e.g. "content.access".
func (id Identity) Has(perm string) bool {
	for _, p := range id.Permissions {
		if p == perm {
			return true
		}
	}
	return false
}

// Error is a non-2xx reply from the server.
type Error struct {
	Status int
	Method string
	Path   string
	Body   string

	// PasswordChange reports the X-Password-Change-Required header, which the
	// server sets on the 403 it gives an account that must change its password
	// before it may act. It is a different problem from a missing permission and
	// has a different answer — change it on that server's own web UI — so it is
	// carried rather than flattened into the status.
	PasswordChange bool
}

func (e *Error) Error() string {
	msg := strings.TrimSpace(e.Body)
	if msg == "" {
		msg = http.StatusText(e.Status)
	}
	return fmt.Sprintf("%s %s: %d %s", e.Method, e.Path, e.Status, msg)
}

// Unauthorized reports whether the server refused for lack of a valid identity.
func (e *Error) Unauthorized() bool { return e.Status == http.StatusUnauthorized }

// Client talks to one madshare server.
//
// Token is an API token from GET/POST /api/auth/tokens, sent as a bearer header
// — the documented credential for a non-browser client. Leaving it empty makes
// every request anonymous, which is a valid mode: the caller then sees the guest
// listing.
type Client struct {
	Base  string
	Token string
	HTTP  *http.Client
}

// New returns a client for base (e.g. "http://localhost:3000"). An empty token
// means anonymous.
func New(base, token string) *Client {
	return &Client{
		Base:  strings.TrimRight(base, "/"),
		Token: token,
		HTTP:  &http.Client{Timeout: 30 * time.Second},
	}
}

// NormalizeBase turns what a person types into a base URL.
//
// A bare host is assumed to be plain HTTP, which is the shape a madshare is
// actually reached at: a yggdrasil address or a LAN box, neither of which has a
// certificate (docs/ui/clipboard.md records the same fact from the other side —
// those origins are not secure contexts). Guessing https would fail on the
// common case and leave the person to work out why.
func NormalizeBase(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", errors.New("type a server address")
	}
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", fmt.Errorf("%s is not an address: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("%s: only http and https are supported", u.Scheme)
	}
	if u.Host == "" {
		return "", fmt.Errorf("%s has no host in it", raw)
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawQuery, u.Fragment = "", ""
	return u.String(), nil
}

// Resolve turns a server-relative URL (every row's play URL is one) into an
// absolute one. Relative URLs are what the server returns because its own web UI
// is same-origin; a native client has no origin, so it must join them itself.
func (c *Client) Resolve(rel string) string {
	if rel == "" || strings.HasPrefix(rel, "http://") || strings.HasPrefix(rel, "https://") {
		return rel
	}
	return c.Base + "/" + strings.TrimLeft(rel, "/")
}

func (c *Client) get(ctx context.Context, path string, out any) error {
	return c.do(ctx, http.MethodGet, path, nil, out)
}

// do runs one request. A nil body sends none; anything else is marshalled as
// JSON. A nil out discards the reply.
func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Base+path, rdr)
	if err != nil {
		return err
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")

	res, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(res.Body, 4<<10))
		return &Error{
			Status:         res.StatusCode,
			Method:         method,
			Path:           path,
			Body:           string(raw),
			PasswordChange: res.Header.Get("X-Password-Change-Required") != "",
		}
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, res.Body)
		return nil
	}
	return json.NewDecoder(res.Body).Decode(out)
}

// Me answers who the client is. It is the only reliable sign-in check.
func (c *Client) Me(ctx context.Context) (Identity, error) {
	var id Identity
	err := c.get(ctx, "/api/auth/me", &id)
	return id, err
}

// Artists returns the whole album-artist list (the bare-array form).
func (c *Client) Artists(ctx context.Context) ([]Artist, error) {
	var out []Artist
	err := c.get(ctx, "/api/artists", &out)
	return out, err
}

// ArtistsPage returns one page. Pass the previous page's NextCursor verbatim;
// an empty cursor starts at the beginning.
func (c *Client) ArtistsPage(ctx context.Context, cursor string, limit int) (ArtistPage, error) {
	q := url.Values{}
	q.Set("limit", strconv.Itoa(limit))
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	var out ArtistPage
	err := c.get(ctx, "/api/artists?"+q.Encode(), &out)
	return out, err
}

// Albums returns one artist's albums.
func (c *Client) Albums(ctx context.Context, artistID int64) ([]Album, error) {
	var out []Album
	err := c.get(ctx, "/api/albums?artist_id="+strconv.FormatInt(artistID, 10), &out)
	return out, err
}

// Tracks returns one album's tracks, in the server's order. Do not re-sort.
func (c *Client) Tracks(ctx context.Context, albumID int64) ([]Track, error) {
	var out []Track
	err := c.get(ctx, "/api/tracks?album_id="+strconv.FormatInt(albumID, 10), &out)
	return out, err
}

// Search runs the library search.
func (c *Client) Search(ctx context.Context, q string) (SearchResults, error) {
	var out SearchResults
	err := c.get(ctx, "/api/search?q="+url.QueryEscape(q), &out)
	return out, err
}

// Open streams a track's audio. rel is the URL from a track row, which is
// server-relative because the server's own web UI is same-origin.
//
// The body is the caller's to close. Playback is NOT where this is used
// directly: the decoders need a whole local file (go-mp3 walks every frame
// header before it will report a length), so the bytes go to the cache first
// and the decoder opens that — see internal/blobcache.
func (c *Client) Open(ctx context.Context, rel string) (io.ReadCloser, int64, error) {
	return c.OpenFrom(ctx, rel, 0)
}

// OpenFrom is Open resuming at a byte offset.
//
// It exists to make a failed swarm recoverable. Bytes the swarm delivered are
// per-chunk verified before they are readable, so a transfer that dies half way
// leaves a CORRECT PREFIX on disk — and since a blob is addressed by its content
// hash, the relay's copy of it is byte-identical. Asking for the rest is
// therefore sound, where asking for the whole thing again and appending it would
// be the noise this is avoiding.
//
// A server that ignores the Range header answers 200 with the WHOLE body, and
// that is REFUSED rather than used: appending a second copy of the prefix to the
// first is exactly the failure the resume exists to prevent. /files/* is served
// by http.ServeFile, which handles Range natively, so 206 is the expected answer.
func (c *Client) OpenFrom(ctx context.Context, rel string, offset int64) (io.ReadCloser, int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Resolve(rel), nil)
	if err != nil {
		return nil, 0, err
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, 0, err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(res.Body, 4<<10))
		res.Body.Close()
		return nil, 0, &Error{
			Status:         res.StatusCode,
			Method:         http.MethodGet,
			Path:           rel,
			Body:           string(raw),
			PasswordChange: res.Header.Get("X-Password-Change-Required") != "",
		}
	}
	if offset > 0 && res.StatusCode != http.StatusPartialContent {
		res.Body.Close()
		return nil, 0, fmt.Errorf("%s ignored a range request for byte %d and answered %d with the whole file",
			rel, offset, res.StatusCode)
	}
	return res.Body, res.ContentLength, nil
}
