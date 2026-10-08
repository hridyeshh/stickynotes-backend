// Package config loads stickynotes-backend settings from the environment.
package config

import (
	"errors"
	"os"
	"strings"

	"github.com/joho/godotenv"
)

// Config holds runtime settings.
type Config struct {
	Port        string
	DatabaseURL string
	JWTSecret   string
	// GoogleClientIDs is the allowlist for the `aud` claim of Google ID tokens.
	GoogleClientIDs []string
	// GoogleTokenInfoURL is Google's ID-token check endpoint; tests point it at a stub.
	GoogleTokenInfoURL string
}

// Load reads the environment (falling back to .env in dev) and validates it.
func Load() (*Config, error) {
	_ = godotenv.Load() // optional; real environment variables win

	cfg := &Config{
		Port:               os.Getenv("PORT"),
		DatabaseURL:        os.Getenv("DATABASE_URL"),
		JWTSecret:          os.Getenv("JWT_SECRET"),
		GoogleClientIDs:    splitList(os.Getenv("GOOGLE_CLIENT_IDS")),
		GoogleTokenInfoURL: "https://oauth2.googleapis.com/tokeninfo",
	}
	if cfg.Port == "" {
		cfg.Port = "8080"
	}

	switch {
	case cfg.DatabaseURL == "":
		return nil, errors.New("DATABASE_URL is required")
	case len(cfg.JWTSecret) < 32:
		return nil, errors.New("JWT_SECRET must be at least 32 characters")
	case len(cfg.GoogleClientIDs) == 0:
		return nil, errors.New("GOOGLE_CLIENT_IDS is required")
	}
	return cfg, nil
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
