package web

import (
	"crypto/subtle"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/nikolaiianchuk/hima/internal/oauthcal"
	"github.com/nikolaiianchuk/hima/internal/token"
)

const oauthStateCookie = "hima_oauth_state"

func (s *Server) provider(r *http.Request) *oauthcal.Provider {
	for _, p := range s.OAuth {
		if p.ID == r.PathValue("provider") {
			return p
		}
	}
	return nil
}

// handleOAuthConnect sends the Owner to the provider's consent screen. A
// random state in a short-lived cookie ties the callback to this browser.
func (s *Server) handleOAuthConnect(w http.ResponseWriter, r *http.Request) {
	p := s.provider(r)
	if p == nil {
		s.handleNotFound(w, r)
		return
	}
	state := token.New()
	http.SetCookie(w, &http.Cookie{
		Name: oauthStateCookie, Value: state, Path: "/admin/oauth/", MaxAge: int((10 * time.Minute).Seconds()),
		HttpOnly: true, Secure: r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https", SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, p.AuthURL(state), http.StatusFound)
}

func (s *Server) handleOAuthCallback(w http.ResponseWriter, r *http.Request) {
	p := s.provider(r)
	if p == nil {
		s.handleNotFound(w, r)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: oauthStateCookie, Path: "/admin/oauth/", MaxAge: -1})
	back := func(key, msg string) {
		http.Redirect(w, r, withQuery("/admin/sources", url.Values{key: {msg}}), http.StatusSeeOther)
	}
	q := r.URL.Query()
	c, err := r.Cookie(oauthStateCookie)
	if err != nil || q.Get("state") == "" || subtle.ConstantTimeCompare([]byte(c.Value), []byte(q.Get("state"))) != 1 {
		back("error", "The connection expired or came from another browser. Please try again.")
		return
	}
	if e := q.Get("error"); e != "" {
		back("error", p.Title+" said: "+orDefault(q.Get("error_description"), e))
		return
	}
	if err := p.Exchange(r.Context(), q.Get("code")); err != nil {
		s.log.Warn("oauth exchange", "provider", p.ID, "err", err)
		back("error", "Couldn't connect "+p.Title+": "+err.Error())
		return
	}
	// Read the new source right away instead of in up to 5 minutes.
	if err := s.svc.Calendars.RefreshIfOlder(r.Context(), 0); err != nil && !errors.Is(err, r.Context().Err()) {
		s.log.Warn("refresh after connect", "err", err)
	}
	back("connected", p.Title)
}

func (s *Server) handleOAuthDisconnect(w http.ResponseWriter, r *http.Request) {
	p := s.provider(r)
	if p == nil {
		s.handleNotFound(w, r)
		return
	}
	if err := p.Disconnect(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	s.svc.Calendars.RefreshIfOlder(r.Context(), 0)
	http.Redirect(w, r, withQuery("/admin/sources", url.Values{"disconnected": {p.Title}}), http.StatusSeeOther)
}
