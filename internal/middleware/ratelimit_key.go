package middleware

import (
	"net"
	"net/http"
	"strings"

	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/hridyeshh/stickynotes-backend/internal/token"
)

// KeyByUserOrIP returns a rate-limit bucket key: the user when the request carries a
// valid access token (the Next.js server forwards sessions, so many users share its
// egress IP), else the client IP. A made-up or expired token counts as none, so sending
// a fresh one with every request can't buy a fresh bucket.
func KeyByUserOrIP(jwtSecret string) func(*http.Request) (string, error) {
	return func(r *http.Request) (string, error) {
		if scheme, tok, ok := strings.Cut(r.Header.Get("Authorization"), " "); ok && strings.EqualFold(scheme, "Bearer") {
			if claims, err := token.Parse(strings.TrimSpace(tok), jwtSecret); err == nil {
				return "user:" + claims.UserID.String(), nil
			}
		}
		return "ip:" + clientIP(r), nil
	}
}

// clientIP is the IP the ClientIPFrom* middleware found, or the connection's own when
// there was no proxy header to read (local runs, tests).
func clientIP(r *http.Request) string {
	if ip := chimw.GetClientIP(r.Context()); ip != "" {
		return ip
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}
