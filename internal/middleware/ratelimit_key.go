package middleware

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/go-chi/httprate"
)

// KeyByAuthorizationOrIP returns a rate-limit bucket key.
// When Authorization: Bearer <jwt> is present (e.g. the Next.js server forwarding a
// session), the key comes from the token so many users behind one egress IP are not
// collapsed into one bucket. Otherwise it falls back to the client IP.
func KeyByAuthorizationOrIP(r *http.Request) (string, error) {
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	lower := strings.ToLower(auth)
	if strings.HasPrefix(lower, "bearer ") {
		token := strings.TrimSpace(auth[7:])
		if token != "" {
			sum := sha256.Sum256([]byte("bearer:" + token))
			return "auth:" + hex.EncodeToString(sum[:16]), nil
		}
	}
	return httprate.KeyByIP(r)
}
