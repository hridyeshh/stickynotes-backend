package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"

	"github.com/hridyeshh/stickynotes-backend/internal/token"
)

func TestKeyByUserOrIP(t *testing.T) {
	const secret = "test-secret"
	user := uuid.New()
	valid, err := token.Generate(user, secret, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	forged, _ := token.Generate(user, "another-secret", time.Minute)
	expired, _ := token.Generate(user, secret, -time.Minute)

	// Behind one proxy: the client may prepend anything to X-Forwarded-For, but the
	// proxy appends the address it saw, and that rightmost entry is the key.
	key := func(auth string) string {
		var got string
		h := chimw.ClientIPFromXFFTrustedProxies(1)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			got, _ = KeyByUserOrIP(secret)(r)
		}))
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/google", nil)
		req.RemoteAddr = "10.0.0.2:5000"
		req.Header.Set("X-Forwarded-For", "6.6.6.6, 203.0.113.7")
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		h.ServeHTTP(httptest.NewRecorder(), req)
		return got
	}

	for _, tc := range []struct{ name, auth, want string }{
		{"valid token: the user", "Bearer " + valid, "user:" + user.String()},
		{"no token: the client IP, not the spoofed one", "", "ip:203.0.113.7"},
		{"made-up token: still the IP", "Bearer not-a-jwt-" + uuid.NewString(), "ip:203.0.113.7"},
		{"token signed elsewhere: the IP", "Bearer " + forged, "ip:203.0.113.7"},
		{"expired token: the IP", "Bearer " + expired, "ip:203.0.113.7"},
	} {
		if got := key(tc.auth); got != tc.want {
			t.Errorf("%s: key = %q, want %q", tc.name, got, tc.want)
		}
	}

	// No proxy header (local runs): the connection's own address.
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.RemoteAddr = "192.0.2.9:4242"
	if got, _ := KeyByUserOrIP(secret)(req); got != "ip:192.0.2.9" {
		t.Errorf("direct: key = %q", got)
	}
}
