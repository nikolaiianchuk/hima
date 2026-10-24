// Package web serves the admin and public (Guest) HTTP surfaces on one port,
// separated by path (see docs/adr/0002).
package web

import (
	"log/slog"
	"net/http"

	"github.com/a-h/templ"

	"github.com/nikolaiianchuk/hima/internal/config"
	"github.com/nikolaiianchuk/hima/internal/store"
	"github.com/nikolaiianchuk/hima/internal/web/views"
)

type Server struct {
	cfg   *config.Config
	store *store.Store
	log   *slog.Logger
	mux   *http.ServeMux
}

func New(cfg *config.Config, st *store.Store, log *slog.Logger) *Server {
	s := &Server{cfg: cfg, store: st, log: log, mux: http.NewServeMux()}
	s.routes()
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})

	// Public, secured by unguessable tokens.
	s.mux.HandleFunc("GET /i/{token}", s.handleInvite)
	s.mux.HandleFunc("GET /b/{token}", s.handleNotFound)    // Manage link
	s.mux.HandleFunc("GET /feed/{token}", s.handleNotFound) // Owner feed

	// Admin.
	if s.cfg.Auth.Mode == config.AuthModePassword {
		s.mux.HandleFunc("GET /admin/login", s.handleLoginForm)
		s.mux.HandleFunc("POST /admin/login", s.handleLogin)
		s.mux.Handle("POST /admin/logout", s.requireOwner(http.HandlerFunc(s.handleLogout)))
	}
	s.mux.Handle("GET /admin/{$}", s.requireOwner(http.HandlerFunc(s.handleAdminHome)))
	s.mux.Handle("GET /{$}", http.RedirectHandler("/admin/", http.StatusFound))

	s.mux.HandleFunc("/", s.handleNotFound)
}

func (s *Server) handleInvite(w http.ResponseWriter, r *http.Request) {
	// No Invites exist yet; every token is treated as dead.
	s.render(w, r, http.StatusNotFound, views.DeadInvite(s.cfg.Owner.Name))
}

func (s *Server) handleAdminHome(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusOK, views.AdminHome(s.cfg.Owner.Name, s.cfg.Auth.Mode == config.AuthModePassword))
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
