// Package reqctx holds request-scoped values. It is its own package so auth and
// middleware can share it without an import cycle.
package reqctx

import (
	"context"

	"github.com/google/uuid"
)

type contextKey string

const userIDKey contextKey = "user_id"

// GetUserID returns the authenticated user's ID, if the request has one.
func GetUserID(ctx context.Context) (uuid.UUID, bool) {
	id, ok := ctx.Value(userIDKey).(uuid.UUID)
	return id, ok
}

// WithUserID returns a copy of ctx carrying the user's ID.
func WithUserID(ctx context.Context, id uuid.UUID) context.Context {
	return context.WithValue(ctx, userIDKey, id)
}
