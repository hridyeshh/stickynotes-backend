package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

// googleClaims is the subset of Google's tokeninfo response we use.
type googleClaims struct {
	Audience      string
	Subject       string
	Email         string
	EmailVerified bool
	Name          string
	Picture       string
}

// verifyGoogleIDToken asks Google's tokeninfo endpoint about an ID token. A 200
// proves signature, expiry and issuer, but not that the token was minted for us,
// so `aud` must also be in cfg.GoogleClientIDs.
//
// ponytail: one HTTPS round-trip per sign-in. Verify locally against Google's
// JWKS if sign-in volume ever makes that matter.
func (h *Handler) verifyGoogleIDToken(ctx context.Context, idToken string) (*googleClaims, error) {
	endpoint := h.cfg.GoogleTokenInfoURL + "?" + url.Values{"id_token": {idToken}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call tokeninfo: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("tokeninfo status %d: %s", resp.StatusCode, body)
	}

	// tokeninfo returns values as strings ("email_verified": "true"); tolerate real
	// booleans too.
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("decode tokeninfo: %w", err)
	}
	str := func(k string) string { s, _ := raw[k].(string); return s }
	out := &googleClaims{
		Audience: str("aud"),
		Subject:  str("sub"),
		Email:    str("email"),
		Name:     str("name"),
		Picture:  str("picture"),
	}
	switch v := raw["email_verified"].(type) {
	case bool:
		out.EmailVerified = v
	case string:
		out.EmailVerified = strings.EqualFold(v, "true")
	}

	// Without this, a valid Google ID token from any app would sign in here.
	if !slices.Contains(h.cfg.GoogleClientIDs, out.Audience) {
		slog.Warn("google_oauth_audience_mismatch", "got", out.Audience)
		return nil, fmt.Errorf("google token audience mismatch: got %q", out.Audience)
	}
	return out, nil
}
