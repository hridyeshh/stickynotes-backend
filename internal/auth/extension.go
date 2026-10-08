package auth

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/hridyeshh/stickynotes-backend/internal/db"
	"github.com/hridyeshh/stickynotes-backend/internal/reqctx"
	"github.com/hridyeshh/stickynotes-backend/internal/types"
)

// A code only has to survive the hop from the web page to the extension.
const extensionCodeTTL = 60 * time.Second

// RFC 7636: a verifier is 43-128 unreserved characters; an S256 challenge is 43.
var (
	pkceVerifier  = regexp.MustCompile(`^[A-Za-z0-9._~-]{43,128}$`)
	pkceChallenge = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
)

// ExtensionCode handles POST /api/v1/extension/codes. The signed-in web app passes
// on the extension's PKCE challenge and gets back a single-use code for it.
func (h *Handler) ExtensionCode(w http.ResponseWriter, r *http.Request) {
	var req types.ExtensionCodeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || !pkceChallenge.MatchString(req.CodeChallenge) {
		types.WriteError(w, http.StatusBadRequest, types.ErrCodeValidation, "code_challenge must be an S256 PKCE challenge")
		return
	}
	userID, _ := reqctx.GetUserID(r.Context())

	code, err := generateSecureToken()
	if err != nil {
		slog.Error("extension code: generate", "error", err)
		types.WriteInternalError(w)
		return
	}
	if err := h.q.CreateExtensionCode(r.Context(), db.CreateExtensionCodeParams{
		CodeHash:      hashToken(code),
		UserID:        userID,
		CodeChallenge: req.CodeChallenge,
		ExpiresAt:     time.Now().Add(extensionCodeTTL),
	}); err != nil {
		slog.Error("extension code: store", "error", err)
		types.WriteInternalError(w)
		return
	}
	types.WriteJSON(w, http.StatusOK, types.ExtensionCodeResponse{Code: code})
}

// ExtensionToken handles POST /api/v1/extension/token. The extension trades the
// code plus its PKCE verifier for a session of its own, so the code alone is
// useless to anyone who sees it on the way.
func (h *Handler) ExtensionToken(w http.ResponseWriter, r *http.Request) {
	var req types.ExtensionTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Code == "" || !pkceVerifier.MatchString(req.CodeVerifier) {
		types.WriteError(w, http.StatusBadRequest, types.ErrCodeValidation, "code and code_verifier are required")
		return
	}

	// Consumed before the verifier is checked, so a wrong guess burns the code.
	row, err := h.q.ConsumeExtensionCode(r.Context(), hashToken(req.Code))
	if errors.Is(err, pgx.ErrNoRows) {
		types.WriteError(w, http.StatusUnauthorized, types.ErrCodeInvalidToken, "Code is invalid, used or expired")
		return
	}
	if err != nil {
		slog.Error("extension token: consume code", "error", err)
		types.WriteInternalError(w)
		return
	}
	sum := sha256.Sum256([]byte(req.CodeVerifier))
	if subtle.ConstantTimeCompare([]byte(base64.RawURLEncoding.EncodeToString(sum[:])), []byte(row.CodeChallenge)) != 1 {
		types.WriteError(w, http.StatusUnauthorized, types.ErrCodeInvalidToken, "code_verifier does not match")
		return
	}

	user, err := h.q.GetUser(r.Context(), row.UserID)
	if err != nil {
		slog.Error("extension token: get user", "error", err)
		types.WriteInternalError(w)
		return
	}
	device, _ := json.Marshal(map[string]string{"user_agent": r.UserAgent(), "client": "extension"})
	h.startSession(w, r, user, device)
}
