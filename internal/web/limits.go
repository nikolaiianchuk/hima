package web

import (
	"net"
	"net/http"
	"strings"

	"github.com/nikolaiianchuk/hima/internal/token"
	"github.com/nikolaiianchuk/hima/internal/web/views"
)

// Rate limits (set in New):
//   - booking or rescheduling: 10 per IP per hour, after a burst of 10
//   - booking through one Invite: 20 per hour, so a leaked multi-use Invite
//     can't be emptied by one script
//   - admin password login: 10 tries per IP per 15 minutes

// clientIP is the address the request came from. Behind a reverse proxy on a
// private network, it is the rightmost X-Forwarded-For entry: the client as
// seen by that proxy.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	if ip != nil && (ip.IsLoopback() || ip.IsPrivate()) {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			if fwd := strings.TrimSpace(parts[len(parts)-1]); net.ParseIP(fwd) != nil {
				return fwd
			}
		}
	}
	return host
}

// allowBooking applies the booking limits; inviteTok is empty for
// reschedules. It renders the refusal itself and reports whether to go on.
func (s *Server) allowBooking(w http.ResponseWriter, r *http.Request, inviteTok string) bool {
	ok := s.bookPerIP.Allow(clientIP(r))
	if ok && inviteTok != "" {
		ok = s.bookPerInvite.Allow(token.Hash(inviteTok))
	}
	if !ok {
		s.log.Warn("rate limited", "path", redactPath(r.URL.Path), "ip", clientIP(r))
		s.render(w, r, http.StatusTooManyRequests, views.TooMany(s.currentAccent()))
	}
	return ok
}
