package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/nikolaiianchuk/hima/internal/config"
	"github.com/nikolaiianchuk/hima/internal/store"
)

func newTestServer(t *testing.T, auth config.Auth) *httptest.Server {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "hima.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cfg := &config.Config{Owner: config.Owner{Name: "Nikolai"}, Auth: auth}
	ts := httptest.NewServer(New(cfg, st, slog.New(slog.NewTextHandler(io.Discard, nil))))
	t.Cleanup(ts.Close)
	return ts
}

func noRedirect() *http.Client {
	return &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func TestProxyModeRequiresHeaderWhenConfigured(t *testing.T) {
	ts := newTestServer(t, config.Auth{Mode: config.AuthModeProxy, ProxyHeader: "Remote-User"})

	resp, err := http.Get(ts.URL + "/admin/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("without header: status = %d, want 401", resp.StatusCode)
	}

	req, _ := http.NewRequest("GET", ts.URL+"/admin/", nil)
	req.Header.Set("Remote-User", "nikolai")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("with header: status = %d, want 200", resp.StatusCode)
	}
}

func TestPasswordModeLoginFlow(t *testing.T) {
	hash, _ := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.MinCost)
	ts := newTestServer(t, config.Auth{Mode: config.AuthModePassword, PasswordHash: string(hash)})
	c := noRedirect()

	resp, _ := c.Get(ts.URL + "/admin/")
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/admin/login" {
		t.Fatalf("anonymous admin: status = %d, location = %q", resp.StatusCode, resp.Header.Get("Location"))
	}

	resp, _ = c.PostForm(ts.URL+"/admin/login", url.Values{"password": {"wrong"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong password: status = %d, want 401", resp.StatusCode)
	}

	resp, _ = c.PostForm(ts.URL+"/admin/login", url.Values{"password": {"secret"}})
	resp.Body.Close()
	cookies := resp.Cookies()
	if resp.StatusCode != http.StatusSeeOther || len(cookies) == 0 {
		t.Fatalf("login: status = %d, cookies = %d", resp.StatusCode, len(cookies))
	}

	req, _ := http.NewRequest("GET", ts.URL+"/admin/", nil)
	req.AddCookie(cookies[0])
	resp, _ = c.Do(req)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "Nikolai") {
		t.Errorf("logged-in admin: status = %d", resp.StatusCode)
	}
}

func TestDeadInviteIsNeutral(t *testing.T) {
	ts := newTestServer(t, config.Auth{Mode: config.AuthModeProxy})
	resp, _ := http.Get(ts.URL + "/i/whatever")
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound || !strings.Contains(string(body), "Ask Nikolai to send you a new one") {
		t.Errorf("status = %d, body = %s", resp.StatusCode, body)
	}
}
