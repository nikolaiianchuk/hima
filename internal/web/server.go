// Package web serves the admin and public (Guest) HTTP surfaces on one port,
// separated by path (see docs/adr/0002).
package web

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/a-h/templ"

	"github.com/nikolaiianchuk/hima/internal/booking"
	"github.com/nikolaiianchuk/hima/internal/calsource"
	"github.com/nikolaiianchuk/hima/internal/config"
	"github.com/nikolaiianchuk/hima/internal/store"
	"github.com/nikolaiianchuk/hima/internal/web/views"
)

type Server struct {
	cfg     *config.Config
	store   *store.Store
	svc     *booking.Service
	log     *slog.Logger
	handler http.Handler
}

func New(cfg *config.Config, st *store.Store, svc *booking.Service, log *slog.Logger) *Server {
	s := &Server{cfg: cfg, store: st, svc: svc, log: log}
	mux := http.NewServeMux()
	s.routes(mux)
	// Rejects cross-site form posts (CSRF) based on Sec-Fetch-Site / Origin.
	s.handler = securityHeaders(http.NewCrossOriginProtection().Handler(mux))
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.handler.ServeHTTP(w, r)
}

func (s *Server) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})

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
	mux.Handle("GET /admin/{$}", s.requireOwner(http.HandlerFunc(s.handleAdmin)))
	mux.Handle("POST /admin/invites", s.requireOwner(http.HandlerFunc(s.handleCreateInvite)))
	mux.Handle("POST /admin/invites/{id}/revoke", s.requireOwner(http.HandlerFunc(s.handleRevokeInvite)))
	mux.Handle("POST /admin/bookings/{id}/cancel", s.requireOwner(http.HandlerFunc(s.handleOwnerCancel)))
	mux.Handle("POST /admin/feed", s.requireOwner(http.HandlerFunc(s.handleRotateFeed)))
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
		if strings.HasPrefix(r.URL.Path, "/i/") || strings.HasPrefix(r.URL.Path, "/b/") || strings.HasPrefix(r.URL.Path, "/feed/") || strings.HasPrefix(r.URL.Path, "/admin") {
			h.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleNotFound(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusNotFound, views.NotFound())
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, status int, c templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := c.Render(r.Context(), w); err != nil {
		s.log.Error("render", "path", r.URL.Path, "err", err)
	}
}

// fail renders the page matching a service error, logging unexpected ones.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, booking.ErrInviteDead):
		s.render(w, r, http.StatusNotFound, views.DeadInvite(s.cfg.Owner.Name))
	case errors.Is(err, booking.ErrNotFound), errors.Is(err, store.ErrNotFound):
		s.render(w, r, http.StatusNotFound, views.NotFound())
	case errors.Is(err, calsource.ErrStale):
		s.render(w, r, http.StatusServiceUnavailable, views.Unavailable())
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
