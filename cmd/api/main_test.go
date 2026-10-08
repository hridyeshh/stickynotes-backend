package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hridyeshh/stickynotes-backend/internal/config"
	"github.com/hridyeshh/stickynotes-backend/internal/notes"
	"github.com/hridyeshh/stickynotes-backend/internal/types"
)

type caller func(method, path, bearer, body string, want int, out any)

// newTestAPI serves the real router against a real Postgres. Google is stubbed: the
// id_token sent at sign-in becomes the account's Google subject, so each test makes
// and touches only its own users.
// Run with: make docker-up && make test
func newTestAPI(t *testing.T) caller {
	t.Helper()
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
	t.Cleanup(pool.Close)

	google := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"aud": "test-client", "sub": "test-" + r.URL.Query().Get("id_token"),
			"email": "Ana@Example.com", "email_verified": "true", "name": "Ana Ruiz",
		})
	}))
	t.Cleanup(google.Close)

	cfg := &config.Config{
		JWTSecret:          strings.Repeat("s", 32),
		GoogleClientIDs:    []string{"test-client"},
		GoogleTokenInfoURL: google.URL,
	}
	api := httptest.NewServer(routes(cfg, pool))
	t.Cleanup(api.Close)

	return func(method, path, bearer, body string, want int, out any) {
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
}

// TestAuthFlow runs sign-in → /me → refresh rotation → logout → account deletion.
func TestAuthFlow(t *testing.T) {
	call := newTestAPI(t)
	signInBody := `{"id_token":"` + uuid.NewString() + `"}`
	refresh := func(tok string) string { return `{"refresh_token":"` + tok + `"}` }

	var s types.AuthResponse
	call("POST", "/api/v1/auth/google", "", signInBody, http.StatusOK, &s)
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
	call("POST", "/api/v1/auth/google", "", signInBody, http.StatusOK, &other)
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

// TestSync connects an extension (PKCE code exchange), then syncs: repeat-safe
// pushes, same-title topic merges, moves and deletions reaching other devices, and
// one account unable to touch another's notes.
func TestSync(t *testing.T) {
	call := newTestAPI(t)
	signIn := func() types.AuthResponse {
		var s types.AuthResponse
		call("POST", "/api/v1/auth/google", "", `{"id_token":"`+uuid.NewString()+`"}`, http.StatusOK, &s)
		return s
	}
	web, other := signIn(), signIn()

	// The web session gets a code bound to the extension's challenge; only the
	// extension's verifier redeems it, and only once.
	verifier := strings.Repeat("v", 43)
	sum := sha256.Sum256([]byte(verifier))
	challenge := `{"code_challenge":"` + base64.RawURLEncoding.EncodeToString(sum[:]) + `"}`
	newCode := func() string {
		var c types.ExtensionCodeResponse
		call("POST", "/api/v1/extension/codes", web.AccessToken, challenge, http.StatusOK, &c)
		return c.Code
	}
	redeem := func(code, verifier string, want int, out any) {
		call("POST", "/api/v1/extension/token", "", `{"code":"`+code+`","code_verifier":"`+verifier+`"}`, want, out)
	}
	call("POST", "/api/v1/extension/codes", "", challenge, http.StatusUnauthorized, nil)
	call("POST", "/api/v1/extension/codes", web.AccessToken, `{"code_challenge":"plain"}`, http.StatusBadRequest, nil)
	burned := newCode()
	redeem(burned, strings.Repeat("w", 43), http.StatusUnauthorized, nil) // a wrong verifier...
	redeem(burned, verifier, http.StatusUnauthorized, nil)                // ...burns the code
	var ext types.AuthResponse
	code := newCode()
	redeem(code, verifier, http.StatusOK, &ext)
	redeem(code, verifier, http.StatusUnauthorized, nil)
	if ext.User.ID != web.User.ID || ext.RefreshToken == "" {
		t.Fatalf("extension session: %+v", ext)
	}
	tok := ext.AccessToken

	push := func(bearer string, actions ...string) notes.PushResponse {
		t.Helper()
		var res notes.PushResponse
		call("POST", "/api/v1/sync/push", bearer, `{"actions":[`+strings.Join(actions, ",")+`]}`, http.StatusOK, &res)
		return res
	}
	pull := func(bearer, cursor string) notes.PullResponse {
		t.Helper()
		var res notes.PullResponse
		call("GET", "/api/v1/sync/pull?cursor="+url.QueryEscape(cursor), bearer, "", http.StatusOK, &res)
		return res
	}
	topic := func(id, title string) string {
		return fmt.Sprintf(`{"type":"create_topic","id":%q,"title":%q}`, id, title)
	}
	highlight := func(id, topicID, url, text string) string {
		return fmt.Sprintf(`{"type":"create_highlight","id":%q,"topic_id":%q,"url":%q,"page_title":"A page","site":"example.com",`+
			`"text":%q,"section":"Intro","anchor":{"prefix":"abc"},"created_at":"2026-10-09T08:00:00.000Z"}`, id, topicID, url, text)
	}
	del := func(id string) string { return fmt.Sprintf(`{"type":"delete_highlight","id":%q}`, id) }
	move := func(url, topicID string) string {
		return fmt.Sprintf(`{"type":"move_source","url":%q,"topic_id":%q}`, url, topicID)
	}
	const ring, vnodes, jump = "https://a.example/ring?id=1", "https://b.example/vnodes", "https://c.example/jump"

	// A push applied twice (say its response was lost) leaves one copy.
	t1, h1, h2 := uuid.NewString(), uuid.NewString(), uuid.NewString()
	first := []string{topic(t1, "Consistent hashing"), highlight(h1, t1, ring, "Keys map onto a ring."), highlight(h2, t1, vnodes, "Virtual nodes even out load.")}
	for range 2 {
		if res := push(tok, first...); len(res.Rejected) != 0 || len(res.TopicIDs) != 0 {
			t.Fatalf("first push: %+v", res)
		}
	}
	full := pull(tok, "")
	if len(full.Topics) != 1 || len(full.Sources) != 2 || len(full.Highlights) != 2 {
		t.Fatalf("after first push: %d topics, %d sources, %d highlights", len(full.Topics), len(full.Sources), len(full.Highlights))
	}
	if a := string(full.Highlights[0].Anchor); !strings.Contains(a, `"abc"`) {
		t.Fatalf("anchor = %s, want the JSON that was pushed", a)
	}

	// The same title made on another device folds into the existing topic.
	t2, h3 := uuid.NewString(), uuid.NewString()
	if res := push(tok, topic(t2, "consistent HASHING"), highlight(h3, t2, jump, "Jump hash needs no ring.")); res.TopicIDs[t2] != t1 || len(res.Rejected) != 0 {
		t.Fatalf("merge push: %+v", res)
	}

	// Another account can neither write into nor change this one's notes.
	mine := uuid.NewString()
	res := push(other.AccessToken,
		highlight(uuid.NewString(), t1, "https://evil.example/", "Planted."), // not its topic
		del(h1),             // not its highlight: a quiet no-op
		topic(mine, "Mine"), // fine
		move(ring, mine),    // not its source
		topic(t1, "Hijack"), // the id is taken by the other account
	)
	if got := rejected(res); got != "0,3,4" {
		t.Fatalf("other account's push rejected %q, want 0,3,4: %+v", got, res)
	}
	if theirs := pull(other.AccessToken, ""); len(theirs.Topics) != 1 || len(theirs.Sources) != 0 || len(theirs.Highlights) != 0 {
		t.Fatalf("other account sees %+v", theirs)
	}

	// Bad actions are rejected one by one; an oversized push is refused whole.
	res = push(tok, highlight(uuid.NewString(), t1, "javascript:alert(1)", "x"), `{"type":"rename_everything"}`, highlight("not-a-uuid", t1, ring, "x"))
	if got := rejected(res); got != "0,1,2" {
		t.Fatalf("bad actions rejected %q, want 0,1,2", got)
	}
	tooMany := strings.TrimSuffix(strings.Repeat(del(uuid.NewString())+",", 201), ",")
	call("POST", "/api/v1/sync/push", tok, `{"actions":[`+tooMany+`]}`, http.StatusBadRequest, nil)
	call("GET", "/api/v1/sync/pull?cursor=yesterday", tok, "", http.StatusBadRequest, nil)

	// Moves and deletions reach other devices through pull, as deletion markers.
	t3 := uuid.NewString()
	if res := push(tok, topic(t3, "Load balancing"), move(vnodes, t3), del(h3)); len(res.Rejected) != 0 {
		t.Fatalf("move/delete push: %+v", res)
	}
	delta := pull(tok, full.Cursor)
	for _, s := range delta.Sources {
		if s.Url == vnodes && (s.TopicID.String() != t3 || s.Deleted) || s.Url == jump && !s.Deleted {
			t.Fatalf("source after move/delete: %+v", s)
		}
	}
	for _, h := range delta.Highlights {
		if h.ID.String() == h3 && (!h.Deleted || h.Text != "") || h.ID.String() == h1 && h.Deleted {
			t.Fatalf("highlight after delete: %+v", h)
		}
	}
	if now := pull(tok, ""); len(now.Topics) != 2 || len(now.Sources) != 2 || len(now.Highlights) != 2 {
		t.Fatalf("live copy: %d topics, %d sources, %d highlights", len(now.Topics), len(now.Sources), len(now.Highlights))
	}

	call("DELETE", "/api/v1/me", web.AccessToken, "", http.StatusNoContent, nil)
	call("DELETE", "/api/v1/me", other.AccessToken, "", http.StatusNoContent, nil)
}

// rejected lists the rejected action indexes, e.g. "0,3".
func rejected(res notes.PushResponse) string {
	var idx []string
	for _, r := range res.Rejected {
		idx = append(idx, strconv.Itoa(r.Index))
	}
	return strings.Join(idx, ",")
}
