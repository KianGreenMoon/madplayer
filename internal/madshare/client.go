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
	"context"
	"encoding/json"
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
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Base+path, nil)
	if err != nil {
		return err
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	req.Header.Set("Accept", "application/json")

	res, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 4<<10))
		return &Error{Status: res.StatusCode, Method: http.MethodGet, Path: path, Body: string(body)}
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
