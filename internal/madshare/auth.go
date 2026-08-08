package madshare

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"time"
)

// The credential a native client keeps is an API TOKEN, never the password.
//
// A password would have to be stored to survive a restart, and it opens every
// door the account has on that server — including changing itself. A token is
// named, listed and revocable from the server's own settings page, which is
// where somebody looking for "what is signed in to my account" will go. The
// server documents it as the credential for a non-browser client
// (docs/api/tokens.md); this is that flow.
//
// Password → token is a three-step handshake, because minting a token requires
// being signed in: log in for a session cookie, mint, then drop the session
// again. The token outlives the session, so the cookie has no reason to stay.

// ErrBadCredentials is a refused username or password. It is distinct from every
// other failure because it is the one the person can fix by typing again — a
// server that is unreachable, or an account that must change its password, both
// need something else done.
var ErrBadCredentials = errors.New("wrong username or password")

// ErrPasswordChangeRequired is an account the server will not let act until its
// password has been changed. There is nothing this client can do about it: the
// change-password flow lives on that server.
var ErrPasswordChangeRequired = errors.New("this account must change its password on the server first")

// TokenName is what the minted token is called in the server's token list. It
// names the program rather than the person, because that list is read while
// answering "what is signed in to my account".
const TokenName = "madplayer"

// SignIn exchanges a password for an API token and returns the token with the
// identity it belongs to.
//
// The password is used here and nowhere else — it is never stored and never
// returned. The session opened to mint the token is closed again before this
// returns, so the only credential that survives the call is the token itself.
func (c *Client) SignIn(ctx context.Context, username, password string) (token string, id Identity, err error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return "", Identity{}, err
	}
	// A separate client for the handshake: the session cookie belongs to these
	// three requests, and giving the long-lived client a jar would leave it
	// holding a credential it is meant to have replaced.
	sess := &Client{Base: c.Base, HTTP: &http.Client{Jar: jar, Timeout: c.timeout()}}

	if err := sess.do(ctx, http.MethodPost, "/api/auth/login", map[string]string{
		"username": username,
		"password": password,
	}, nil); err != nil {
		return "", Identity{}, loginError(err)
	}
	// From here on the session exists, so it is closed on every path out.
	defer func() { _ = sess.do(context.WithoutCancel(ctx), http.MethodPost, "/api/auth/logout", nil, nil) }()

	var minted struct {
		Token string `json:"token"`
	}
	if err := sess.do(ctx, http.MethodPost, "/api/auth/tokens", map[string]any{
		"name": fmt.Sprintf("%s (%s)", TokenName, time.Now().Format("2006-01-02")),
	}, &minted); err != nil {
		return "", Identity{}, loginError(err)
	}
	if minted.Token == "" {
		return "", Identity{}, errors.New("the server minted a token but did not return it")
	}

	// Ask who we are with the TOKEN, not the session: it proves the credential
	// that is being kept actually works, which is the thing worth knowing before
	// the caller saves it.
	probe := &Client{Base: c.Base, Token: minted.Token, HTTP: &http.Client{Timeout: c.timeout()}}
	id, err = probe.Me(ctx)
	if err != nil {
		return "", Identity{}, err
	}
	return minted.Token, id, nil
}

// SignOut revokes nothing: a token is revoked from the server's settings page,
// deliberately. Forgetting it here is what signing out of THIS device means, and
// the caller does that by dropping the stored credential.
//
// It exists as a named thing anyway, so the asymmetry is written down where
// somebody looks for it rather than discovered.
func (c *Client) SignOut() { c.Token = "" }

func (c *Client) timeout() time.Duration {
	if c.HTTP != nil && c.HTTP.Timeout > 0 {
		return c.HTTP.Timeout
	}
	return 30 * time.Second
}

// loginError maps the server's refusals onto the two a person can act on.
func loginError(err error) error {
	var e *Error
	if !errors.As(err, &e) {
		return err
	}
	switch {
	case e.PasswordChange:
		return ErrPasswordChangeRequired
	case e.Unauthorized():
		return ErrBadCredentials
	}
	return err
}
