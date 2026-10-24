// Package oauthcal connects hima to Google Calendar and Microsoft 365 with
// OAuth, using an app the Owner registers with the provider (see
// docs/adr/0004). It reads busy time through each provider's free/busy API
// and writes Bookings as events.
package oauthcal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"golang.org/x/oauth2"

	"github.com/nikolaiianchuk/hima/internal/calsource"
	"github.com/nikolaiianchuk/hima/internal/store"
)

// Provider is one connected calendar service.
type Provider struct {
	// ID is "google" or "microsoft"; Title is for people.
	ID, Title string
	OAuth     *oauth2.Config
	Store     *store.Store
	// APIBase is the REST API root, overridable in tests.
	APIBase string
	// AuthOptions are extra parameters for the consent URL.
	AuthOptions []oauth2.AuthCodeOption
	// fetchAccount returns the connected account's email address.
	fetchAccount func(ctx context.Context, c *http.Client) (string, error)

	mu sync.Mutex
}

func (p *Provider) tokenKey() string   { return "oauth." + p.ID + ".token" }
func (p *Provider) accountKey() string { return "oauth." + p.ID + ".account" }

// AuthURL is where the Owner is sent to grant access.
func (p *Provider) AuthURL(state string) string {
	return p.OAuth.AuthCodeURL(state, p.AuthOptions...)
}

// Exchange finishes the consent flow and stores the tokens and account.
func (p *Provider) Exchange(ctx context.Context, code string) error {
	tok, err := p.OAuth.Exchange(ctx, code)
	if err != nil {
		return fmt.Errorf("exchange code: %w", err)
	}
	if tok.RefreshToken == "" {
		return errors.New("the provider returned no refresh token; remove hima's access in your account settings and connect again")
	}
	account, err := p.fetchAccount(ctx, p.OAuth.Client(ctx, tok))
	if err != nil {
		return fmt.Errorf("read account: %w", err)
	}
	if err := p.saveToken(ctx, tok); err != nil {
		return err
	}
	return p.Store.SetSetting(ctx, p.accountKey(), account)
}

// Account is the connected account's address, or "" when not connected.
func (p *Provider) Account(ctx context.Context) (string, error) {
	return p.Store.Setting(ctx, p.accountKey())
}

// Disconnect forgets the tokens. Access should also be revoked in the
// provider's account settings.
func (p *Provider) Disconnect(ctx context.Context) error {
	if err := p.Store.DeleteSetting(ctx, p.tokenKey()); err != nil {
		return err
	}
	return p.Store.DeleteSetting(ctx, p.accountKey())
}

func (p *Provider) saveToken(ctx context.Context, tok *oauth2.Token) error {
	b, err := json.Marshal(tok)
	if err != nil {
		return err
	}
	return p.Store.SetSetting(ctx, p.tokenKey(), string(b))
}

// client returns an HTTP client that authorises requests and stores
// refreshed tokens, or calsource.ErrNotConnected.
func (p *Provider) client(ctx context.Context) (*http.Client, error) {
	raw, err := p.Store.Setting(ctx, p.tokenKey())
	if err != nil {
		return nil, err
	}
	if raw == "" {
		return nil, calsource.ErrNotConnected
	}
	var tok oauth2.Token
	if err := json.Unmarshal([]byte(raw), &tok); err != nil {
		return nil, err
	}
	src := &savingSource{p: p, base: p.OAuth.TokenSource(context.Background(), &tok), last: tok.AccessToken}
	return oauth2.NewClient(ctx, oauth2.ReuseTokenSource(&tok, src)), nil
}

// savingSource stores tokens the provider refreshes, so the newest refresh
// token survives restarts.
type savingSource struct {
	p    *Provider
	base oauth2.TokenSource
	last string
}

func (s *savingSource) Token() (*oauth2.Token, error) {
	tok, err := s.base.Token()
	if err != nil {
		return nil, err
	}
	s.p.mu.Lock()
	defer s.p.mu.Unlock()
	if tok.AccessToken != s.last {
		s.last = tok.AccessToken
		if err := s.p.saveToken(context.Background(), tok); err != nil {
			return nil, err
		}
	}
	return tok, nil
}

// do sends a JSON request to the provider's API and decodes the response
// into out (if non-nil). Error responses include the start of the body.
func (p *Provider) do(ctx context.Context, method, path string, body, out any, header ...string) (int, error) {
	c, err := p.client(ctx)
	if err != nil {
		return 0, err
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		rd = strings.NewReader(string(b))
	}
	req, err := http.NewRequestWithContext(ctx, method, p.APIBase+path, rd)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	resp, err := c.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return resp.StatusCode, fmt.Errorf("%s %s: HTTP %s: %s", method, path, resp.Status, strings.TrimSpace(string(msg)))
	}
	if out != nil {
		return resp.StatusCode, json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode, nil
}
