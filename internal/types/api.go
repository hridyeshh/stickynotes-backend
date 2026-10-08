package types

import (
	"encoding/json"
	"net/http"
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

// AuthResponse is returned by sign-in and refresh.
type AuthResponse struct {
	AccessToken  string       `json:"access_token"`
	RefreshToken string       `json:"refresh_token"`
	ExpiresIn    int          `json:"expires_in"`
	User         UserResponse `json:"user"`
}
