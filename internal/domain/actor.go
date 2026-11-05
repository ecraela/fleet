package domain

import "context"

type actorKey struct{}

// WithActor carries only the verified human subject into atomic control audits.
func WithActor(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, actorKey{}, userID)
}

func Actor(ctx context.Context) string {
	id, _ := ctx.Value(actorKey{}).(string)
	if !ValidID(id) {
		return ""
	}
	return id
}
