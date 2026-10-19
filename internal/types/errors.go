// Package types holds the JSON shapes shared by handlers and middleware.
package types

import (
	"encoding/json"
	"net/http"
)

// ErrorResponse is the JSON envelope for errors: {"error": "message", "code": "CODE"}.
type ErrorResponse struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

// Error codes returned by all handlers.
const (
	ErrCodeValidation     = "VALIDATION_ERROR"
	ErrCodeUnauthorized   = "UNAUTHORIZED"
	ErrCodeRateLimited    = "RATE_LIMITED"
	ErrCodeInternalServer = "INTERNAL_ERROR"
	ErrCodeInvalidToken   = "INVALID_TOKEN"
	ErrCodeExpiredToken   = "EXPIRED_TOKEN"
	ErrCodeNotFound       = "NOT_FOUND"
	ErrCodeForbidden      = "FORBIDDEN"
)

// WriteError writes a JSON error response.
func WriteError(w http.ResponseWriter, statusCode int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(ErrorResponse{Error: message, Code: code})
}

// WriteInternalError writes a 500 without leaking details.
func WriteInternalError(w http.ResponseWriter) {
	WriteError(w, http.StatusInternalServerError, ErrCodeInternalServer, "An internal server error occurred")
}
