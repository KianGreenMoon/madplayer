package madshare

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeServer is enough of madshare's auth surface to drive the handshake: it
// checks the session cookie the way the real one does, so a client that skipped
// the login step could not mint a token here either.
type fakeServer struct {
	mux      *http.ServeMux
	password string

	sessions       map[string]bool
	loggedOut      bool
	minted         string
	passwordChange bool
}

func newFakeServer(t *testing.T) (*fakeServer, *httptest.Server) {
	t.Helper()
	f := &fakeServer{
		mux:      http.NewServeMux(),
		password: "correct horse",
		sessions: map[string]bool{},
		minted:   "tok_abc123",
	}

	f.mux.HandleFunc("POST /api/auth/login", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Username, Password string }
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Password != f.password {
			http.Error(w, "invalid credentials", http.StatusUnauthorized)
			return
		}
		f.sessions["sess1"] = true
		http.SetCookie(w, &http.Cookie{Name: "madshare_session", Value: "sess1", Path: "/"})
		_ = json.NewEncoder(w).Encode(map[string]any{"username": req.Username})
	})

	f.mux.HandleFunc("POST /api/auth/logout", func(w http.ResponseWriter, r *http.Request) {
		f.loggedOut = true
		delete(f.sessions, "sess1")
		w.WriteHeader(http.StatusNoContent)
	})

	f.mux.HandleFunc("POST /api/auth/tokens", func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie("madshare_session")
		if err != nil || !f.sessions[c.Value] {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		if f.passwordChange {
			w.Header().Set("X-Password-Change-Required", "1")
			http.Error(w, "password change required", http.StatusForbidden)
			return
		}
		var req struct{ Name string }
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 1, "name": req.Name, "token": f.minted})
	})

	f.mux.HandleFunc("GET /api/auth/me", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+f.minted {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"username":    "kian",
			"permissions": []string{"content.access", "file.upload"},
		})
	})

	srv := httptest.NewServer(f.mux)
	t.Cleanup(srv.Close)
	return f, srv
}

func TestSignInMintsATokenAndDropsTheSession(t *testing.T) {
	f, srv := newFakeServer(t)
	c := New(srv.URL, "")

	token, id, err := c.SignIn(context.Background(), "kian", f.password)
	if err != nil {
		t.Fatalf("SignIn: %v", err)
	}
	if token != f.minted {
		t.Errorf("token = %q, want %q", token, f.minted)
	}
	if id.Username != "kian" {
		t.Errorf("username = %q, want kian", id.Username)
	}
	if !id.Has("content.access") {
		t.Error("identity should carry content.access")
	}
	// The session was a means to mint the token; leaving it open would leave a
	// second live credential behind that nobody can see or revoke by name.
	if !f.loggedOut {
		t.Error("the session opened to mint the token was not closed")
	}
	// SignIn must not leave the password-bearing session on the long-lived
	// client, and must not quietly adopt the token either — saving it is the
	// caller's decision.
	if c.Token != "" {
		t.Errorf("client token = %q, want it left to the caller", c.Token)
	}
}

func TestSignInReportsABadPasswordAsSuch(t *testing.T) {
	_, srv := newFakeServer(t)
	c := New(srv.URL, "")

	_, _, err := c.SignIn(context.Background(), "kian", "wrong")
	if !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("err = %v, want ErrBadCredentials", err)
	}
}

// A forced password change is a 403 with a header, not a 401, and the answer is
// on the server's own web UI — so it must not be reported as a typo.
func TestSignInReportsAForcedPasswordChange(t *testing.T) {
	f, srv := newFakeServer(t)
	f.passwordChange = true
	c := New(srv.URL, "")

	_, _, err := c.SignIn(context.Background(), "kian", f.password)
	if !errors.Is(err, ErrPasswordChangeRequired) {
		t.Fatalf("err = %v, want ErrPasswordChangeRequired", err)
	}
	if errors.Is(err, ErrBadCredentials) {
		t.Error("a forced password change must not look like a wrong password")
	}
}

func TestNormalizeBase(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		// A bare host is plain HTTP: that is what a ygg address or a LAN box is
		// reached at, and guessing https would fail the common case.
		{"192.168.1.5:3000", "http://192.168.1.5:3000"},
		{"  music.example  ", "http://music.example"},
		{"https://music.example/", "https://music.example"},
		{"http://host:3000/madshare/", "http://host:3000/madshare"},
		{"http://host:3000/?x=1#y", "http://host:3000"},
	}
	for _, tc := range cases {
		got, err := NormalizeBase(tc.in)
		if err != nil {
			t.Errorf("NormalizeBase(%q): %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("NormalizeBase(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}

	for _, bad := range []string{"", "   ", "ftp://host", "http://"} {
		if got, err := NormalizeBase(bad); err == nil {
			t.Errorf("NormalizeBase(%q) = %q, want an error", bad, got)
		}
	}
}

func TestOpenStreamsAudioWithTheBearerToken(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if r.URL.Path != "/files/abc/song.mp3" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte("ID3 bytes"))
	}))
	defer srv.Close()

	c := New(srv.URL, "tok_abc123")
	body, n, err := c.Open(context.Background(), "/files/abc/song.mp3")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer body.Close()
	if gotAuth != "Bearer tok_abc123" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if n != int64(len("ID3 bytes")) {
		t.Errorf("length = %d, want %d", n, len("ID3 bytes"))
	}

	if _, _, err := c.Open(context.Background(), "/files/missing/x.mp3"); err == nil {
		t.Error("a 404 should be an error")
	} else if e := (*Error)(nil); !errors.As(err, &e) || e.Status != http.StatusNotFound {
		t.Errorf("err = %v, want a 404 *Error", err)
	}
}

func TestResolveJoinsRelativeURLs(t *testing.T) {
	c := New("http://host:3000/", "")
	if got := c.Resolve("/files/a/b.mp3"); got != "http://host:3000/files/a/b.mp3" {
		t.Errorf("Resolve = %q", got)
	}
	// An absolute URL is passed through: a server behind a proxy may return one.
	abs := "https://cdn.example/a.mp3"
	if got := c.Resolve(abs); got != abs {
		t.Errorf("Resolve(absolute) = %q, want it untouched", got)
	}
}

func TestErrorMessageNamesTheRequest(t *testing.T) {
	e := &Error{Status: 403, Method: "POST", Path: "/api/auth/tokens", Body: "  nope\n"}
	if got := e.Error(); !strings.Contains(got, "403") || !strings.Contains(got, "nope") {
		t.Errorf("Error() = %q", got)
	}
}
