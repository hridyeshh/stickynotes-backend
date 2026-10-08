// Package middleware provides HTTP middleware for the stickynotes API.
package middleware

import (
	"errors"
	"net/http"
	"strings"

	"github.com/hridyeshh/stickynotes-backend/internal/reqctx"
	"github.com/hridyeshh/stickynotes-backend/internal/token"
	"github.com/hridyeshh/stickynotes-backend/internal/types"
)

// Authenticate validates a Bearer access token and puts the user ID in the
// request context (read it with reqctx.GetUserID).
func Authenticate(jwtSecret string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			scheme, tok, ok := strings.Cut(r.Header.Get("Authorization"), " ")
			if !ok || !strings.EqualFold(scheme, "Bearer") || tok == "" {
				types.WriteError(w, http.StatusUnauthorized, types.ErrCodeUnauthorized, "Authorization header must be: Bearer <token>")
				return
			}

			claims, err := token.Parse(tok, jwtSecret)
			if errors.Is(err, token.ErrTokenExpired) {
				types.WriteError(w, http.StatusUnauthorized, types.ErrCodeExpiredToken, "Access token has expired")
				return
			}
			if err != nil {
				types.WriteError(w, http.StatusUnauthorized, types.ErrCodeInvalidToken, "Invalid access token")
				return
			}

			next.ServeHTTP(w, r.WithContext(reqctx.WithUserID(r.Context(), claims.UserID)))
		})
	}
}
