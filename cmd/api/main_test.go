package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hridyeshh/stickynotes-backend/internal/config"
	"github.com/hridyeshh/stickynotes-backend/internal/types"
)

// TestAuthFlow runs sign-in → /me → refresh rotation → logout → account deletion
// through the real router and a real Postgres. Google is stubbed.
// Run with: make docker-up && make test
func TestAuthFlow(t *testing.T) {
	dbURL := os.Getenv("TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("TEST_DATABASE_URL not set; run `make docker-up` then `make test`")
	}
	if err := runMigrations(dbURL); err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(context.Background(), dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	// A fresh Google subject per run, so the test only ever touches its own rows.
	sub := "test-" + uuid.NewString()
	google := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"aud": "test-client", "sub": sub, "email": "Ana@Example.com", "email_verified": "true", "name": "Ana Ruiz",
		})
	}))
	defer google.Close()

	cfg := &config.Config{
		JWTSecret:          strings.Repeat("s", 32),
		GoogleClientIDs:    []string{"test-client"},
		GoogleTokenInfoURL: google.URL,
	}
	api := httptest.NewServer(routes(cfg, pool))
	defer api.Close()
	call := func(method, path, bearer, body string, want int, out any) {
		t.Helper()
		req, err := http.NewRequest(method, api.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != want {
			b, _ := io.ReadAll(resp.Body)
			t.Fatalf("%s %s: status %d, want %d: %s", method, path, resp.StatusCode, want, b)
		}
		if out != nil {
			if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
				t.Fatal(err)
			}
		}
	}
	refresh := func(tok string) string { return `{"refresh_token":"` + tok + `"}` }

	var s types.AuthResponse
	call("POST", "/api/v1/auth/google", "", `{"id_token":"x"}`, http.StatusOK, &s)
	if s.User.Email != "ana@example.com" || s.AccessToken == "" || s.RefreshToken == "" {
		t.Fatalf("sign-in response: %+v", s)
	}

	var me types.UserResponse
	call("GET", "/api/v1/me", s.AccessToken, "", http.StatusOK, &me)
	if me.ID != s.User.ID || me.Name != "Ana Ruiz" {
		t.Fatalf("/me = %+v, want user %s", me, s.User.ID)
	}
	call("GET", "/api/v1/me", "", "", http.StatusUnauthorized, nil)
	call("GET", "/api/v1/me", "not-a-jwt", "", http.StatusUnauthorized, nil)

	// Signing in again (a second device) is the same account.
	var other types.AuthResponse
	call("POST", "/api/v1/auth/google", "", `{"id_token":"x"}`, http.StatusOK, &other)
	if other.User.ID != s.User.ID {
		t.Fatalf("second sign-in made a new user: %s != %s", other.User.ID, s.User.ID)
	}

	// Rotation: a refresh token works exactly once.
	var s2 types.AuthResponse
	call("POST", "/api/v1/auth/refresh", "", refresh(s.RefreshToken), http.StatusOK, &s2)
	call("POST", "/api/v1/auth/refresh", "", refresh(s.RefreshToken), http.StatusUnauthorized, nil)

	// Logout revokes.
	call("POST", "/api/v1/auth/logout", "", refresh(s2.RefreshToken), http.StatusNoContent, nil)
	call("POST", "/api/v1/auth/refresh", "", refresh(s2.RefreshToken), http.StatusUnauthorized, nil)

	// Deleting the account takes every other session with it.
	call("DELETE", "/api/v1/me", s2.AccessToken, "", http.StatusNoContent, nil)
	call("GET", "/api/v1/me", s2.AccessToken, "", http.StatusUnauthorized, nil)
	call("POST", "/api/v1/auth/refresh", "", refresh(other.RefreshToken), http.StatusUnauthorized, nil)
}
