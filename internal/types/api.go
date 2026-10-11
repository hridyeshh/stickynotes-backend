package types

import (
	"encoding/json"
	"net/http"
	"time"
)

// WriteJSON writes v as JSON with the given status code.
func WriteJSON(w http.ResponseWriter, statusCode int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(v)
}

// ExtensionCodeRequest is the body of POST /extension/codes.
type ExtensionCodeRequest struct {
	CodeChallenge string `json:"code_challenge"`
}

// ExtensionCodeResponse carries the single-use code the web app hands to the extension.
type ExtensionCodeResponse struct {
	Code string `json:"code"`
}

// ExtensionTokenRequest is the body of POST /extension/token.
type ExtensionTokenRequest struct {
	Code         string `json:"code"`
	CodeVerifier string `json:"code_verifier"`
}

// GoogleAuthRequest is the body of POST /auth/google.
type GoogleAuthRequest struct {
	IDToken string `json:"id_token"`
}

// RefreshRequest is the body of POST /auth/refresh and /auth/logout.
type RefreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

// UserResponse is the public shape of a user.
type UserResponse struct {
	ID        string `json:"id"`
	Email     string `json:"email"`
	Name      string `json:"name"`
	AvatarURL string `json:"avatar_url"`
}

// MeResponse is GET /me: the user, and when a connected extension last refreshed
// its session (null when none is connected).
type MeResponse struct {
	UserResponse
	ExtensionSeenAt *time.Time `json:"extension_seen_at"`
}

// DeviceResponse is one signed-in browser or extension, for Settings.
type DeviceResponse struct {
	ID         string    `json:"id"`
	Client     string    `json:"client"` // "extension" or "web"
	UserAgent  string    `json:"user_agent"`
	LastActive time.Time `json:"last_active"`
}

// AuthResponse is returned by sign-in and refresh.
type AuthResponse struct {
	AccessToken  string       `json:"access_token"`
	RefreshToken string       `json:"refresh_token"`
	ExpiresIn    int          `json:"expires_in"`
	User         UserResponse `json:"user"`
}
