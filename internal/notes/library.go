package notes

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/hridyeshh/stickynotes-backend/internal/db"
	"github.com/hridyeshh/stickynotes-backend/internal/reqctx"
	"github.com/hridyeshh/stickynotes-backend/internal/types"
)

// LibraryResponse is everything the web library shows. Edits go through /sync/push.
type LibraryResponse struct {
	Topics     []db.LibraryTopicsRow     `json:"topics"`
	Sources    []db.LibrarySourcesRow    `json:"sources"`
	Highlights []db.LibraryHighlightsRow `json:"highlights"`
}

// SharedResponse is one public topic: its title, the author's name, and its notes.
type SharedResponse struct {
	Title      string                    `json:"title"`
	Author     string                    `json:"author"`
	Sources    []db.LibrarySourcesRow    `json:"sources"`
	Highlights []db.LibraryHighlightsRow `json:"highlights"`
}

// ShareRequest is the body of POST /topics/{id}/share.
type ShareRequest struct {
	Public bool `json:"public"`
}

// ShareResponse is a topic's sharing state; the slug is empty while private.
type ShareResponse struct {
	Public bool   `json:"public"`
	Slug   string `json:"slug"`
}

// Library handles GET /api/v1/library: every live topic, source and note.
// ponytail: one response with every note, like a first pull; page it past tens of
// thousands of highlights.
func (h *Handler) Library(w http.ResponseWriter, r *http.Request) {
	userID, _ := reqctx.GetUserID(r.Context())
	res, err := h.library(r.Context(), userID)
	if err != nil {
		slog.Error("library", "error", err)
		types.WriteInternalError(w)
		return
	}
	types.WriteJSON(w, http.StatusOK, res)
}

func (h *Handler) library(ctx context.Context, userID uuid.UUID) (LibraryResponse, error) {
	var res LibraryResponse
	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return res, err
	}
	defer tx.Rollback(ctx) // read-only: nothing to commit

	q := db.New(tx)
	if res.Topics, err = q.LibraryTopics(ctx, userID); err != nil {
		return res, err
	}
	if res.Sources, err = q.LibrarySources(ctx, db.LibrarySourcesParams{UserID: userID}); err != nil {
		return res, err
	}
	res.Highlights, err = q.LibraryHighlights(ctx, db.LibraryHighlightsParams{UserID: userID})
	return res, err
}

// Shared handles GET /api/v1/shared/{slug}, with no sign-in: a topic its owner made public.
func (h *Handler) Shared(w http.ResponseWriter, r *http.Request) {
	res, err := h.shared(r.Context(), chi.URLParam(r, "slug"))
	if errors.Is(err, pgx.ErrNoRows) {
		types.WriteError(w, http.StatusNotFound, types.ErrCodeNotFound, "No shared notes here")
		return
	}
	if err != nil {
		slog.Error("shared topic", "error", err)
		types.WriteInternalError(w)
		return
	}
	types.WriteJSON(w, http.StatusOK, res)
}

func (h *Handler) shared(ctx context.Context, slug string) (SharedResponse, error) {
	var res SharedResponse
	tx, err := h.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return res, err
	}
	defer tx.Rollback(ctx)

	q := db.New(tx)
	t, err := q.SharedTopic(ctx, pgtype.Text{String: slug, Valid: true})
	if err != nil {
		return res, err
	}
	topic := pgtype.UUID{Bytes: t.ID, Valid: true}
	res.Title, res.Author = t.Title, t.Author
	if res.Sources, err = q.LibrarySources(ctx, db.LibrarySourcesParams{UserID: t.UserID, TopicID: topic}); err != nil {
		return res, err
	}
	res.Highlights, err = q.LibraryHighlights(ctx, db.LibraryHighlightsParams{UserID: t.UserID, TopicID: topic})
	return res, err
}

// Share handles POST /api/v1/topics/{id}/share: anyone with the link can read the
// topic, or only its owner.
func (h *Handler) Share(w http.ResponseWriter, r *http.Request) {
	var req ShareRequest
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil || json.NewDecoder(r.Body).Decode(&req) != nil {
		types.WriteError(w, http.StatusBadRequest, types.ErrCodeValidation, `A topic id and {"public": bool} are required`)
		return
	}
	userID, _ := reqctx.GetUserID(r.Context())

	row, err := db.New(h.pool).SetTopicShare(r.Context(), db.SetTopicShareParams{
		IsPublic: req.Public, NewSlug: pgtype.Text{String: newSlug(), Valid: true}, ID: id, UserID: userID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		types.WriteError(w, http.StatusNotFound, types.ErrCodeNotFound, "No such topic")
		return
	}
	if err != nil {
		slog.Error("share topic", "error", err)
		types.WriteInternalError(w)
		return
	}
	types.WriteJSON(w, http.StatusOK, ShareResponse{Public: row.IsPublic, Slug: row.ShareSlug.String})
}

// newSlug is 12 URL-safe characters (72 random bits): unguessable, short enough to paste.
func newSlug() string {
	b := make([]byte, 9)
	_, _ = rand.Read(b) // crypto/rand.Read never returns an error
	return base64.RawURLEncoding.EncodeToString(b)
}
