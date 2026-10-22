package openrouter

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// fakeOpenRouter implements /auth/keys with real PKCE verification.
func fakeOpenRouter(t *testing.T, challenge *string) *Client {
	t.Helper()
	return newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/auth/keys" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		var body struct {
			Code, CodeVerifier, CodeChallengeMethod string
		}
		var raw map[string]string
		_ = json.NewDecoder(r.Body).Decode(&raw)
		body.Code, body.CodeVerifier, body.CodeChallengeMethod = raw["code"], raw["code_verifier"], raw["code_challenge_method"]
		sum := sha256.Sum256([]byte(body.CodeVerifier))
		if body.Code != "good-code" || body.CodeChallengeMethod != "S256" ||
			base64.RawURLEncoding.EncodeToString(sum[:]) != *challenge {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"message":"Invalid code or verifier","code":403}}`))
			return
		}
		_, _ = w.Write([]byte(`{"key":"sk-or-v1-from-oauth","user_id":"u1"}`))
	})
}

// browser plays the user's browser: it receives the auth URL, then hits the callback.
func browser(t *testing.T, challenge *string, then func(callback, state string)) func(string) error {
	return func(authURL string) error {
		u, err := url.Parse(authURL)
		if err != nil {
			return err
		}
		q := u.Query()
		if q.Get("code_challenge_method") != "S256" || q.Get("key_label") != "Pennyframe" || q.Get("state") == "" {
			t.Errorf("bad auth URL: %s", authURL)
		}
		if !strings.HasPrefix(q.Get("callback_url"), "http://localhost:") {
			t.Errorf("callback should be loopback: %s", q.Get("callback_url"))
		}
		*challenge = q.Get("code_challenge")
		go then(q.Get("callback_url"), q.Get("state"))
		return nil
	}
}

func get(t *testing.T, u string) int {
	t.Helper()
	resp, err := http.Get(u)
	if err != nil {
		t.Errorf("GET %s: %v", u, err)
		return 0
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

func TestAuthorizeSuccess(t *testing.T) {
	var challenge string
	c := fakeOpenRouter(t, &challenge)
	key, err := c.Authorize(context.Background(), AuthOptions{Label: "Pennyframe", Open: browser(t, &challenge, func(cb, state string) {
		if got := get(t, cb+"?code=good-code&state="+url.QueryEscape(state)); got != 200 {
			t.Errorf("callback status %d", got)
		}
	})})
	if err != nil || key != "sk-or-v1-from-oauth" {
		t.Fatalf("key=%q err=%v", key, err)
	}
}

func TestAuthorizeIgnoresWrongStateThenSucceeds(t *testing.T) {
	var challenge string
	c := fakeOpenRouter(t, &challenge)
	key, err := c.Authorize(context.Background(), AuthOptions{Label: "Pennyframe", Open: browser(t, &challenge, func(cb, state string) {
		if got := get(t, cb+"?code=evil&state=wrong"); got != http.StatusBadRequest {
			t.Errorf("wrong state should be rejected, got %d", got)
		}
		get(t, cb+"?code=good-code&state="+url.QueryEscape(state))
	})})
	if err != nil || key == "" {
		t.Fatalf("key=%q err=%v", key, err)
	}
}

func TestAuthorizeDenied(t *testing.T) {
	var challenge string
	c := fakeOpenRouter(t, &challenge)
	_, err := c.Authorize(context.Background(), AuthOptions{Label: "Pennyframe", Open: browser(t, &challenge, func(cb, _ string) { get(t, cb) })})
	if !errors.Is(err, ErrAuthDenied) {
		t.Fatalf("want ErrAuthDenied, got %v", err)
	}
}

func TestAuthorizeExchangeRejected(t *testing.T) {
	var challenge string
	c := fakeOpenRouter(t, &challenge)
	_, err := c.Authorize(context.Background(), AuthOptions{Label: "Pennyframe", Open: browser(t, &challenge, func(cb, state string) {
		get(t, cb+"?code=bad-code&state="+url.QueryEscape(state))
	})})
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != 403 {
		t.Fatalf("want 403 APIError, got %v", err)
	}
}

func TestAuthorizeCancelledStopsListener(t *testing.T) {
	var challenge, cb string
	c := fakeOpenRouter(t, &challenge)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err := c.Authorize(ctx, AuthOptions{Label: "Pennyframe", Open: browser(t, &challenge, func(callback, _ string) { cb = callback })})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want deadline exceeded, got %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if _, err := http.Get(cb + "?code=good-code&state=x"); err == nil {
		t.Fatal("listener should be closed after cancel")
	}
}

func TestAuthorizeUsesConfiguredAuthURL(t *testing.T) {
	c := New()
	c.AuthURL = "http://auth.example.test/auth"
	var got string
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, _ = c.Authorize(ctx, AuthOptions{Open: func(u string) error { got = u; return nil }})
	if !strings.HasPrefix(got, "http://auth.example.test/auth?") {
		t.Fatalf("auth URL = %q", got)
	}
}
