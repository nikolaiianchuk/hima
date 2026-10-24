// Package web serves the admin and public (Guest) HTTP surfaces on one port,
// separated by path (see docs/adr/0002).
package web

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/a-h/templ"

	"github.com/nikolaiianchuk/hima/internal/booking"
	"github.com/nikolaiianchuk/hima/internal/calsource"
	"github.com/nikolaiianchuk/hima/internal/config"
	"github.com/nikolaiianchuk/hima/internal/ratelimit"
	"github.com/nikolaiianchuk/hima/internal/store"
	"github.com/nikolaiianchuk/hima/internal/web/views"
)

//go:embed static
var staticFiles embed.FS

// DefaultAccent is the accent colour until the Owner picks one.
const DefaultAccent = "#2D3E8F"

// sourceStatuser is implemented by calsource.Cache; the admin uses it to show
// how each Calendar source is doing.
type sourceStatuser interface {
	Status() []calsource.SourceStatus
}

type Server struct {
	cfg     *config.Config
	store   *store.Store
	svc     *booking.Service
	log     *slog.Logger
	handler http.Handler

	mu     sync.RWMutex
	accent string

	// Abuse limits; see limits.go.
	bookPerIP     *ratelimit.Limiter
	bookPerInvite *ratelimit.Limiter
	loginPerIP    *ratelimit.Limiter
}

func New(cfg *config.Config, st *store.Store, svc *booking.Service, log *slog.Logger) *Server {
	s := &Server{
		cfg: cfg, store: st, svc: svc, log: log, accent: DefaultAccent,
		bookPerIP:     ratelimit.New(10, 6*time.Minute),
		bookPerInvite: ratelimit.New(20, 3*time.Minute),
		loginPerIP:    ratelimit.New(10, 90*time.Second),
	}
	if v, err := st.Setting(context.Background(), store.SettingAccent); err == nil && validAccent(v) {
		s.accent = v
	}
	mux := http.NewServeMux()
	s.routes(mux)
	// Rejects cross-site form posts (CSRF) based on Sec-Fetch-Site / Origin.
	s.handler = securityHeaders(http.NewCrossOriginProtection().Handler(mux))
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.handler.ServeHTTP(w, r)
}

func (s *Server) currentAccent() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.accent
}

func (s *Server) setAccent(ctx context.Context, v string) error {
	if err := s.store.SetSetting(ctx, store.SettingAccent, v); err != nil {
		return err
	}
	s.mu.Lock()
	s.accent = v
	s.mu.Unlock()
	return nil
}

func init() {
	sub, _ := fs.Sub(staticFiles, "static")
	views.CurrentAssets = views.Assets{
		CSS:   "/static/hima.css?v=" + fileVersion(sub, "hima.css"),
		Fonts: "/static/fonts.css?v=" + fileVersion(sub, "fonts.css"),
	}
}

func fileVersion(fsys fs.FS, name string) string {
	b, err := fs.ReadFile(fsys, name)
	if err != nil {
		return "0"
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:5])
}

func (s *Server) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})
	sub, _ := fs.Sub(staticFiles, "static")
	static := http.StripPrefix("/static/", http.FileServerFS(sub))
	mux.Handle("GET /static/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Stylesheets are requested with a content hash; fonts never change.
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		static.ServeHTTP(w, r)
	}))
	mux.HandleFunc("GET /favicon.svg", s.handleFavicon)

	// Public, secured by unguessable tokens.
	mux.HandleFunc("GET /i/{token}", s.handleInvite)
	mux.HandleFunc("POST /i/{token}", s.handleBook)
	mux.HandleFunc("GET /b/{token}", s.handleManage)
	mux.HandleFunc("GET /b/{token}/event.ics", s.handleBookingICS)
	mux.HandleFunc("POST /b/{token}/cancel", s.handleGuestCancel)
	mux.HandleFunc("GET /b/{token}/reschedule", s.handleRescheduleForm)
	mux.HandleFunc("POST /b/{token}/reschedule", s.handleReschedule)
	mux.HandleFunc("GET /feed/{token}", s.handleFeed)

	// Admin.
	if s.cfg.Auth.Mode == config.AuthModePassword {
		mux.HandleFunc("GET /admin/login", s.handleLoginForm)
		mux.HandleFunc("POST /admin/login", s.handleLogin)
		mux.Handle("POST /admin/logout", s.requireOwner(http.HandlerFunc(s.handleLogout)))
	}
	admin := func(pattern string, h http.HandlerFunc) { mux.Handle(pattern, s.requireOwner(h)) }
	admin("GET /admin/{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin/invites", http.StatusFound)
	})
	admin("GET /admin/invites", s.handleInvites)
	admin("GET /admin/invites/new", s.handleNewInvite)
	admin("POST /admin/invites", s.handleCreateInvite)
	admin("GET /admin/invites/{id}", s.handleInviteDetail)
	admin("POST /admin/invites/{id}", s.handleEditInvite)
	admin("POST /admin/invites/{id}/revoke", s.handleRevokeInvite)
	admin("GET /admin/bookings", s.handleBookings)
	admin("POST /admin/bookings/{id}", s.handleEditBooking)
	admin("POST /admin/bookings/{id}/cancel", s.handleOwnerCancel)
	admin("POST /admin/feed", s.handleRotateFeed)
	admin("GET /admin/availability", s.handleAvailability)
	admin("GET /admin/sources", s.handleSources)
	admin("GET /admin/activity", s.handleActivity)
	admin("GET /admin/settings", s.handleSettings)
	admin("POST /admin/settings", s.handleSaveSettings)
	mux.Handle("GET /{$}", http.RedirectHandler("/admin/", http.StatusFound))

	mux.HandleFunc("/", s.handleNotFound)
}

// securityHeaders keeps token-bearing URLs out of Referer headers (e.g. when a
// Guest follows a video-call Location) and out of shared caches.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		for _, prefix := range []string{"/i/", "/b/", "/feed/", "/admin"} {
			if strings.HasPrefix(r.URL.Path, prefix) {
				h.Set("Cache-Control", "no-store")
				break
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleFavicon(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	acc := s.currentAccent()
	w.Write([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 48 48"><g fill="#16181A">` +
		`<rect x="4" y="4" width="11" height="11" rx="2.5"/><rect x="18.5" y="4" width="11" height="11" rx="2.5"/><rect x="33" y="4" width="11" height="11" rx="2.5"/>` +
		`<rect x="4" y="18.5" width="11" height="11" rx="2.5"/><rect x="18.5" y="18.5" width="11" height="11" rx="2.5"/>` +
		`<rect x="4" y="33" width="11" height="11" rx="2.5"/><rect x="18.5" y="33" width="11" height="11" rx="2.5"/><rect x="33" y="33" width="11" height="11" rx="2.5"/></g>` +
		`<rect x="34" y="19.5" width="9" height="9" rx="2" fill="none" stroke-width="2.5" stroke="` + acc + `"/></svg>`))
}

func (s *Server) handleNotFound(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusNotFound, views.NotFound(s.currentAccent()))
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, status int, c templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := c.Render(r.Context(), w); err != nil {
		s.log.Error("render", "path", redactPath(r.URL.Path), "err", err)
	}
}

// fail renders the page matching a service error, logging unexpected ones.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, booking.ErrInviteDead):
		s.render(w, r, http.StatusNotFound, views.DeadInvite(s.currentAccent()))
	case errors.Is(err, booking.ErrNotFound), errors.Is(err, store.ErrNotFound):
		s.render(w, r, http.StatusNotFound, views.NotFound(s.currentAccent()))
	case errors.Is(err, calsource.ErrStale):
		s.render(w, r, http.StatusServiceUnavailable, views.Unavailable(s.currentAccent()))
	default:
		s.log.Error("request failed", "path", redactPath(r.URL.Path), "err", err)
		http.Error(w, "Something went wrong.", http.StatusInternalServerError)
	}
}

// baseURL is the public origin for links: base_url if configured, otherwise
// derived from the request.
func (s *Server) baseURL(r *http.Request) string {
	if s.cfg.BaseURL != "" {
		return s.cfg.BaseURL
	}
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// redactPath hides tokens in /i/, /b/ and /feed/ paths before logging.
func redactPath(p string) string {
	for _, prefix := range []string{"/i/", "/b/", "/feed/"} {
		if rest, ok := strings.CutPrefix(p, prefix); ok {
			if _, tail, found := strings.Cut(rest, "/"); found {
				return prefix + "…/" + tail
			}
			return prefix + "…"
		}
	}
	return p
}
