// Package notes serves a user's topics, sources and highlights. Sync is how the
// extension keeps its local copy and the server in step.
package notes

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hridyeshh/stickynotes-backend/internal/db"
	"github.com/hridyeshh/stickynotes-backend/internal/reqctx"
	"github.com/hridyeshh/stickynotes-backend/internal/types"
)

const (
	maxActions = 200
	// Pull re-sends this much history before the cursor, so a row written by a
	// transaction that committed after an earlier pull is not skipped.
	pullOverlap = 5 * time.Minute
)

// Handler serves the /sync routes.
type Handler struct {
	pool *pgxpool.Pool
}

// NewHandler returns a notes Handler.
func NewHandler(pool *pgxpool.Pool) *Handler {
	return &Handler{pool: pool}
}

// PushRequest is the body of POST /sync/push.
type PushRequest struct {
	Actions []PushAction `json:"actions"`
}

// PushAction is one queued change from a device. Ids are client-made UUIDs, kept as
// strings so one malformed action is rejected alone instead of failing the push.
type PushAction struct {
	Type      string          `json:"type"` // create_topic, create_highlight, delete_highlight, move_source
	ID        string          `json:"id"`
	TopicID   string          `json:"topic_id"`
	Title     string          `json:"title"`
	URL       string          `json:"url"`
	PageTitle string          `json:"page_title"`
	Site      string          `json:"site"`
	Text      string          `json:"text"`
	Section   string          `json:"section"`
	Anchor    json.RawMessage `json:"anchor"`
	CreatedAt string          `json:"created_at"`
}

// PushResponse maps merged topic ids (client id → the id that won) and lists the
// rejected actions. A rejected action will never succeed, so clients drop it.
type PushResponse struct {
	TopicIDs map[string]string `json:"topic_ids"`
	Rejected []Rejected        `json:"rejected"`
}

// Rejected names an action by its index in the request.
type Rejected struct {
	Index int    `json:"index"`
	Error string `json:"error"`
}

// PullResponse is what changed since the cursor, and the cursor for next time.
type PullResponse struct {
	Topics     []db.TopicsSinceRow     `json:"topics"`
	Sources    []db.SourcesSinceRow    `json:"sources"`
	Highlights []db.HighlightsSinceRow `json:"highlights"`
	Cursor     string                  `json:"cursor"`
}

// Push handles POST /api/v1/sync/push. Actions apply in order, in one transaction,
// and are safe to repeat. A new topic with a title the user already has merges into
// that topic.
func (h *Handler) Push(w http.ResponseWriter, r *http.Request) {
	var req PushRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		types.WriteError(w, http.StatusBadRequest, types.ErrCodeValidation, "Invalid JSON body")
		return
	}
	if len(req.Actions) > maxActions {
		types.WriteError(w, http.StatusBadRequest, types.ErrCodeValidation, "At most 200 actions per push")
		return
	}
	userID, _ := reqctx.GetUserID(r.Context())

	res, err := h.push(r.Context(), userID, req.Actions)
	if err != nil {
		slog.Error("sync push", "error", err)
		types.WriteInternalError(w)
		return
	}
	types.WriteJSON(w, http.StatusOK, res)
}

func (h *Handler) push(ctx context.Context, userID uuid.UUID, actions []PushAction) (PushResponse, error) {
	res := PushResponse{TopicIDs: map[string]string{}, Rejected: []Rejected{}}
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		return res, err
	}
	defer tx.Rollback(ctx) // no-op once committed

	merged := map[uuid.UUID]uuid.UUID{} // client topic id → server topic id
	for i, a := range actions {
		// A savepoint per action, so a rejected one leaves the others applied.
		sp, err := tx.Begin(ctx)
		if err != nil {
			return res, err
		}
		reason, err := apply(ctx, db.New(sp), userID, a, merged)
		if err != nil && isDataError(err) {
			reason, err = "invalid data", nil
		}
		if err != nil {
			return res, err
		}
		if reason != "" {
			res.Rejected = append(res.Rejected, Rejected{Index: i, Error: reason})
			err = sp.Rollback(ctx)
		} else {
			err = sp.Commit(ctx)
		}
		if err != nil {
			return res, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return res, err
	}
	for from, to := range merged {
		if from != to {
			res.TopicIDs[from.String()] = to.String()
		}
	}
	return res, nil
}

// apply runs one action and returns why it was rejected, if it was.
func apply(ctx context.Context, q *db.Queries, userID uuid.UUID, a PushAction, merged map[uuid.UUID]uuid.UUID) (string, error) {
	id, idErr := uuid.Parse(a.ID)
	topicID, topicErr := uuid.Parse(a.TopicID)
	if to, ok := merged[topicID]; ok {
		topicID = to
	}

	switch a.Type {
	case "create_topic":
		title := strings.TrimSpace(a.Title)
		if idErr != nil || title == "" || utf8.RuneCountInString(title) > 300 {
			return "create_topic needs an id and a title of 1-300 characters", nil
		}
		if err := q.InsertTopic(ctx, db.InsertTopicParams{ID: id, UserID: userID, Title: title}); err != nil {
			return "", err
		}
		won, err := q.ResolveTopic(ctx, db.ResolveTopicParams{UserID: userID, ID: id, Title: title})
		if errors.Is(err, pgx.ErrNoRows) {
			return "topic id is already taken", nil // by another account's topic
		}
		if err != nil {
			return "", err
		}
		merged[id] = won
		return "", nil

	case "create_highlight":
		text := strings.TrimSpace(a.Text)
		switch {
		case idErr != nil || topicErr != nil:
			return "create_highlight needs an id and a topic_id", nil
		case text == "" || utf8.RuneCountInString(text) > 10000:
			return "text must be 1-10000 characters", nil
		case !validURL(a.URL):
			return "url must be an http(s) URL of at most 2048 characters", nil
		}
		// A repeat of a create already applied leaves the highlight, and its source, alone.
		if exists, err := q.HighlightExists(ctx, id); err != nil || exists {
			return "", err
		}
		sourceID, err := q.UpsertSource(ctx, db.UpsertSourceParams{
			Url: a.URL, Title: clip(a.PageTitle, 300), Site: clip(a.Site, 255), TopicID: topicID, UserID: userID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return "topic_id is not one of your topics", nil
		}
		if err != nil {
			return "", err
		}
		return "", q.InsertHighlight(ctx, db.InsertHighlightParams{
			ID: id, UserID: userID, SourceID: sourceID, Text: text, Section: clip(a.Section, 300),
			Anchor: anchor(a.Anchor), CreatedAt: createdAt(a.CreatedAt),
		})

	case "delete_highlight":
		if idErr != nil {
			return "delete_highlight needs an id", nil
		}
		sourceID, err := q.DeleteHighlight(ctx, db.DeleteHighlightParams{ID: id, UserID: userID})
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil // already deleted, or not this user's: nothing to do
		}
		if err != nil {
			return "", err
		}
		return "", q.DeleteSourceIfEmpty(ctx, db.DeleteSourceIfEmptyParams{ID: sourceID, UserID: userID})

	case "move_source":
		if topicErr != nil || !validURL(a.URL) {
			return "move_source needs a url and a topic_id", nil
		}
		n, err := q.MoveSource(ctx, db.MoveSourceParams{UserID: userID, Url: a.URL, TopicID: topicID})
		if err != nil {
			return "", err
		}
		if n == 0 {
			return "no such source or topic", nil
		}
		return "", nil
	}
	return "unknown action type", nil
}

// Pull handles GET /api/v1/sync/pull?cursor=. It returns what changed since the
// cursor, deletions included, and the cursor for next time. Without a cursor it
// returns the whole live copy.
func (h *Handler) Pull(w http.ResponseWriter, r *http.Request) {
	userID, _ := reqctx.GetUserID(r.Context())
	since, withDeleted := time.Time{}, false
	if c := r.URL.Query().Get("cursor"); c != "" {
		t, err := time.Parse(time.RFC3339Nano, c)
		if err != nil {
			types.WriteError(w, http.StatusBadRequest, types.ErrCodeValidation, "cursor is invalid")
			return
		}
		since, withDeleted = t.Add(-pullOverlap), true
	}
	cursor := time.Now().UTC().Format(time.RFC3339Nano)

	res, err := h.pull(r.Context(), userID, since, withDeleted)
	if err != nil {
		slog.Error("sync pull", "error", err)
		types.WriteInternalError(w)
		return
	}
	res.Cursor = cursor
	types.WriteJSON(w, http.StatusOK, res)
}

// ponytail: a first pull sends every live note in one response; page it if
// accounts grow past tens of thousands of highlights.
func (h *Handler) pull(ctx context.Context, userID uuid.UUID, since time.Time, withDeleted bool) (PullResponse, error) {
	var res PullResponse
	// One snapshot for the three reads, so a highlight never arrives without its source.
	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return res, err
	}
	defer tx.Rollback(ctx) // read-only: nothing to commit

	q := db.New(tx)
	if res.Topics, err = q.TopicsSince(ctx, db.TopicsSinceParams{UserID: userID, Since: since, WithDeleted: withDeleted}); err != nil {
		return res, err
	}
	if res.Sources, err = q.SourcesSince(ctx, db.SourcesSinceParams{UserID: userID, Since: since, WithDeleted: withDeleted}); err != nil {
		return res, err
	}
	res.Highlights, err = q.HighlightsSince(ctx, db.HighlightsSinceParams{UserID: userID, Since: since, WithDeleted: withDeleted})
	return res, err
}

// validURL matches the sources table check: http(s) only, because these URLs
// become links on public share pages.
func validURL(s string) bool {
	if !strings.HasPrefix(s, "https://") && !strings.HasPrefix(s, "http://") || utf8.RuneCountInString(s) > 2048 {
		return false
	}
	u, err := url.Parse(s)
	return err == nil && u.Host != ""
}

// clip trims s and cuts it to n characters: page titles and headings can run long.
func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > n {
		s = string([]rune(s)[:n])
	}
	return s
}

// anchor keeps a small JSON anchor and drops anything else. It is only a hint for
// finding the passage on the page again, so a bad one is not worth rejecting over.
func anchor(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 || len(raw) > 4096 || string(raw) == "null" || !json.Valid(raw) {
		return nil
	}
	return raw
}

// createdAt trusts the device's clock, within reason.
func createdAt(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil || t.After(time.Now().Add(24*time.Hour)) {
		return time.Now()
	}
	return t
}

// isDataError reports a value the database refused (SQLSTATE classes 22 and 23):
// the action is bad, not the server.
func isDataError(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && (strings.HasPrefix(pgErr.Code, "22") || strings.HasPrefix(pgErr.Code, "23"))
}
