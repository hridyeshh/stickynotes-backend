package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hridyeshh/stickynotes-backend/internal/config"
)

const ourClient = "123-ours.apps.googleusercontent.com"

// stubTokenInfo serves body as Google's tokeninfo response and returns a Handler
// that accepts the given audiences.
func stubTokenInfo(t *testing.T, body string, allowed []string) *Handler {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return &Handler{cfg: &config.Config{GoogleClientIDs: allowed, GoogleTokenInfoURL: srv.URL}}
}

func TestVerifyGoogleIDToken_AudienceMismatch(t *testing.T) {
	h := stubTokenInfo(t,
		`{"aud":"other-app.apps.googleusercontent.com","sub":"1","email":"a@b.com","email_verified":"true"}`,
		[]string{ourClient})

	_, err := h.verifyGoogleIDToken(context.Background(), "irrelevant")
	if err == nil || !strings.Contains(err.Error(), "audience mismatch") {
		t.Fatalf("want audience mismatch, got %v", err)
	}
}

func TestVerifyGoogleIDToken_AudienceMatch(t *testing.T) {
	h := stubTokenInfo(t,
		`{"aud":"`+ourClient+`","sub":"1","email":"a@b.com","email_verified":"true","name":"Ana","picture":"https://x/p.png"}`,
		[]string{ourClient})

	c, err := h.verifyGoogleIDToken(context.Background(), "irrelevant")
	if err != nil {
		t.Fatalf("want success, got %v", err)
	}
	if c.Subject != "1" || c.Email != "a@b.com" || !c.EmailVerified || c.Name != "Ana" || c.Picture != "https://x/p.png" {
		t.Fatalf("claims not parsed: %+v", c)
	}
}

func TestVerifyGoogleIDToken_EmptyAllowlistFailsClosed(t *testing.T) {
	h := stubTokenInfo(t, `{"aud":"anything","sub":"1","email":"a@b.com","email_verified":"true"}`, nil)

	if _, err := h.verifyGoogleIDToken(context.Background(), "irrelevant"); err == nil {
		t.Fatal("empty allowlist must reject every token")
	}
}
