// Package auth handles Google sign-in and API sessions: short-lived JWT access
// tokens plus hashed refresh tokens, one per device, rotated on every refresh.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/hridyeshh/stickynotes-backend/internal/config"
	"github.com/hridyeshh/stickynotes-backend/internal/db"
	"github.com/hridyeshh/stickynotes-backend/internal/reqctx"
	"github.com/hridyeshh/stickynotes-backend/internal/token"
	"github.com/hridyeshh/stickynotes-backend/internal/types"
)

const (
	accessTokenTTL = 15 * time.Minute
	// Long enough that an extension used once a month stays connected.
	refreshTokenTTL = 60 * 24 * time.Hour
)

// Handler serves the /auth and /me routes.
type Handler struct {
	q   *db.Queries
	cfg *config.Config
}

// NewHandler returns an auth Handler.
func NewHandler(q *db.Queries, cfg *config.Config) *Handler {
	return &Handler{q: q, cfg: cfg}
}

// Google handles POST /api/v1/auth/google. The web app's server sends the Google
// ID token from sign-in; we verify it with Google, find or create the user by
// Google sub, and start a session.
func (h *Handler) Google(w http.ResponseWriter, r *http.Request) {
	var req types.GoogleAuthRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.IDToken == "" {
		types.WriteError(w, http.StatusBadRequest, types.ErrCodeValidation, "id_token is required")
		return
	}

	claims, err := h.verifyGoogleIDToken(r.Context(), req.IDToken)
	if err != nil {
		slog.Warn("google auth: verify", "error", err)
		types.WriteError(w, http.StatusUnauthorized, types.ErrCodeInvalidToken, "Google id_token verification failed")
		return
	}
	if claims.Subject == "" || claims.Email == "" || !claims.EmailVerified {
		types.WriteError(w, http.StatusUnauthorized, types.ErrCodeUnauthorized, "Google account has no verified email")
		return
	}

	user, err := h.q.UpsertGoogleUser(r.Context(), db.UpsertGoogleUserParams{
		GoogleSub: claims.Subject,
		Email:     strings.ToLower(claims.Email),
		Name:      claims.Name,
		AvatarUrl: claims.Picture,
	})
	if err != nil {
		slog.Error("google auth: upsert user", "error", err)
		types.WriteInternalError(w)
		return
	}

	h.startSession(w, r, user, deviceInfo(r))
}

// Refresh handles POST /api/v1/auth/refresh. The presented token is revoked and a
// new pair issued; a token that was already used gets 401.
func (h *Handler) Refresh(w http.ResponseWriter, r *http.Request) {
	var req types.RefreshRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.RefreshToken == "" {
		types.WriteError(w, http.StatusBadRequest, types.ErrCodeValidation, "refresh_token is required")
		return
	}

	old, err := h.q.ConsumeRefreshToken(r.Context(), hashToken(req.RefreshToken))
	if errors.Is(err, pgx.ErrNoRows) {
		types.WriteError(w, http.StatusUnauthorized, types.ErrCodeInvalidToken, "Refresh token is invalid or expired")
		return
	}
	if err != nil {
		slog.Error("refresh: consume token", "error", err)
		types.WriteInternalError(w)
		return
	}

	user, err := h.q.GetUser(r.Context(), old.UserID)
	if err != nil {
		slog.Error("refresh: get user", "error", err)
		types.WriteInternalError(w)
		return
	}

	// Keep device_info so a device has one identity across rotations.
	h.startSession(w, r, user, old.DeviceInfo)
}

// Logout handles POST /api/v1/auth/logout by revoking the given refresh token.
func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	var req types.RefreshRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.RefreshToken == "" {
		types.WriteError(w, http.StatusBadRequest, types.ErrCodeValidation, "refresh_token is required")
		return
	}
	if err := h.q.RevokeRefreshToken(r.Context(), hashToken(req.RefreshToken)); err != nil {
		slog.Error("logout: revoke token", "error", err)
		types.WriteInternalError(w)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Me handles GET /api/v1/me.
func (h *Handler) Me(w http.ResponseWriter, r *http.Request) {
	id, _ := reqctx.GetUserID(r.Context())
	user, err := h.q.GetUser(r.Context(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		// An access token outlives a deleted account by up to accessTokenTTL.
		types.WriteError(w, http.StatusUnauthorized, types.ErrCodeUnauthorized, "Account not found")
		return
	}
	if err != nil {
		slog.Error("me: get user", "error", err)
		types.WriteInternalError(w)
		return
	}
	types.WriteJSON(w, http.StatusOK, toUserResponse(user))
}

// DeleteMe handles DELETE /api/v1/me. The account goes, and by cascade every
// note, share link and session with it.
func (h *Handler) DeleteMe(w http.ResponseWriter, r *http.Request) {
	id, _ := reqctx.GetUserID(r.Context())
	if err := h.q.DeleteUser(r.Context(), id); err != nil {
		slog.Error("delete account", "error", err)
		types.WriteInternalError(w)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// startSession issues an access token and a stored refresh token, then writes both.
func (h *Handler) startSession(w http.ResponseWriter, r *http.Request, user db.User, device []byte) {
	access, err := token.Generate(user.ID, h.cfg.JWTSecret, accessTokenTTL)
	if err != nil {
		slog.Error("issue access token", "error", err)
		types.WriteInternalError(w)
		return
	}

	refresh, err := generateSecureToken()
	if err != nil {
		slog.Error("generate refresh token", "error", err)
		types.WriteInternalError(w)
		return
	}
	if err := h.q.CreateRefreshToken(r.Context(), db.CreateRefreshTokenParams{
		UserID:     user.ID,
		TokenHash:  hashToken(refresh),
		DeviceInfo: device,
		ExpiresAt:  time.Now().Add(refreshTokenTTL),
	}); err != nil {
		slog.Error("store refresh token", "error", err)
		types.WriteInternalError(w)
		return
	}

	types.WriteJSON(w, http.StatusOK, types.AuthResponse{
		AccessToken:  access,
		RefreshToken: refresh,
		ExpiresIn:    int(accessTokenTTL.Seconds()),
		User:         toUserResponse(user),
	})
}

func toUserResponse(u db.User) types.UserResponse {
	return types.UserResponse{ID: u.ID.String(), Email: u.Email, Name: u.Name, AvatarURL: u.AvatarUrl}
}

// generateSecureToken returns 32 random bytes, hex-encoded.
func generateSecureToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// hashToken is what gets stored, so a leaked table can't be replayed.
func hashToken(t string) string {
	sum := sha256.Sum256([]byte(t))
	return hex.EncodeToString(sum[:])
}

// deviceInfo labels a session for the devices list. No IP: we don't need it.
func deviceInfo(r *http.Request) []byte {
	b, _ := json.Marshal(map[string]string{"user_agent": r.UserAgent()})
	return b
}
