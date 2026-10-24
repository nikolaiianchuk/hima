package web

import (
	"net/http"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/nikolaiianchuk/hima/internal/config"
	"github.com/nikolaiianchuk/hima/internal/token"
	"github.com/nikolaiianchuk/hima/internal/web/views"
)

const (
	sessionCookie = "hima_session"
	sessionTTL    = 30 * 24 * time.Hour
)

// requireOwner guards admin routes according to auth.mode.
func (s *Server) requireOwner(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch s.cfg.Auth.Mode {
		case config.AuthModeProxy:
			if h := s.cfg.Auth.ProxyHeader; h != "" && r.Header.Get(h) == "" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		case config.AuthModePassword:
			if !s.hasSession(r) {
				http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
				return
			}
		default:
			// Unreachable: config.Validate rejects other modes. Fail closed anyway.
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) hasSession(r *http.Request) bool {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return false
	}
	ok, err := s.store.SessionValid(r.Context(), token.Hash(c.Value), time.Now())
	if err != nil {
		s.log.Error("session lookup", "err", err)
	}
	return ok
}

func (s *Server) handleLoginForm(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusOK, views.Login(false, s.currentAccent()))
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	pw := r.PostFormValue("password")
	if bcrypt.CompareHashAndPassword([]byte(s.cfg.Auth.PasswordHash), []byte(pw)) != nil {
		s.render(w, r, http.StatusUnauthorized, views.Login(true, s.currentAccent()))
		return
	}
	tok := token.New()
	expires := time.Now().Add(sessionTTL)
	if err := s.store.CreateSession(r.Context(), token.Hash(tok), expires); err != nil {
		s.log.Error("create session", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    tok,
		Path:     "/admin",
		Expires:  expires,
		HttpOnly: true,
		Secure:   r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/admin/", http.StatusSeeOther)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		if err := s.store.DeleteSession(r.Context(), token.Hash(c.Value)); err != nil {
			s.log.Error("delete session", "err", err)
		}
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Path: "/admin", MaxAge: -1})
	http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
}
