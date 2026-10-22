package openrouter

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	"net"
	"net/http"
	"net/url"
	"time"
)

const DefaultAuthURL = "https://openrouter.ai/auth"

// ErrAuthDenied means the user declined (or abandoned) the authorization page.
var ErrAuthDenied = errors.New("authorization was cancelled in the browser")

// AuthOptions configures Authorize.
type AuthOptions struct {
	// Label prefills the name of the key OpenRouter creates.
	Label string
	// Open shows the authorization URL to the user (normally in their browser).
	Open func(authURL string) error
}

// Authorize runs OpenRouter's OAuth PKCE flow for desktop apps: it listens on a loopback port,
// sends the user to OpenRouter to sign in and pick/create a key, then exchanges the returned
// one-time code for an API key that belongs to the user. It blocks until done or ctx ends.
func (c *Client) Authorize(ctx context.Context, opts AuthOptions) (string, error) {
	verifier, err := randomToken(48)
	if err != nil {
		return "", err
	}
	state, err := randomToken(24)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])

	// "localhost" (not 127.0.0.1) is what OpenRouter documents for loopback callbacks; any port works.
	ln, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		return "", fmt.Errorf("couldn't start the local sign-in listener: %w", err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	type result struct {
		code string
		err  error
	}
	done := make(chan result, 1)
	finish := func(r result) {
		select {
		case done <- r:
		default:
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		code, got := q.Get("code"), q.Get("state")
		switch {
		case code == "" && got == "":
			writePage(w, http.StatusOK, "Connection cancelled", "Nothing was connected. You can close this tab and try again from Pennyframe.")
			finish(result{err: ErrAuthDenied})
		case code == "" || subtle.ConstantTimeCompare([]byte(got), []byte(state)) != 1:
			// Not our request (stale tab, other site probing the port): ignore and keep waiting.
			writePage(w, http.StatusBadRequest, "Link not recognised", "This sign-in link doesn't belong to the current Pennyframe connection. Go back to Pennyframe and try again.")
		default:
			writePage(w, http.StatusOK, "Pennyframe is connected", "You can close this tab and return to Pennyframe.")
			finish(result{code: code})
		}
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	defer func() {
		sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()

	q := url.Values{}
	q.Set("callback_url", fmt.Sprintf("http://localhost:%d/callback", port))
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("state", state)
	if opts.Label != "" {
		q.Set("key_label", opts.Label)
	}
	if opts.Open == nil {
		return "", errors.New("no way to open the browser")
	}
	if err := opts.Open(c.authBase() + "?" + q.Encode()); err != nil {
		return "", fmt.Errorf("couldn't open the browser: %w", err)
	}

	select {
	case r := <-done:
		if r.err != nil {
			return "", r.err
		}
		return c.exchangeCode(ctx, r.code, verifier)
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (c *Client) authBase() string {
	if c.AuthURL != "" {
		return c.AuthURL
	}
	return DefaultAuthURL
}

func (c *Client) exchangeCode(ctx context.Context, code, verifier string) (string, error) {
	var out struct {
		Key string `json:"key"`
	}
	err := c.do(ctx, http.MethodPost, "/auth/keys", "", map[string]string{
		"code":                  code,
		"code_verifier":         verifier,
		"code_challenge_method": "S256",
	}, &out)
	if err != nil {
		return "", err
	}
	if out.Key == "" {
		return "", errors.New("OpenRouter didn't return a key")
	}
	return out.Key, nil
}

func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func writePage(w http.ResponseWriter, status int, title, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><title>%[1]s</title>
<style>body{font:16px system-ui,sans-serif;background:#141417;color:#ececf1;display:grid;place-items:center;height:100vh;margin:0}
main{text-align:center;max-width:420px;padding:24px}h1{font-size:22px;margin:0 0 8px}p{color:#9a9aa8;margin:0}
i{display:inline-block;width:40px;height:40px;border-radius:50%%;background:radial-gradient(circle at 32%% 28%%,#f3c08c,#d9904f 55%%,#8a4d17);margin-bottom:16px}</style>
<main><i></i><h1>%[1]s</h1><p>%[2]s</p></main>`, html.EscapeString(title), html.EscapeString(body))
}
