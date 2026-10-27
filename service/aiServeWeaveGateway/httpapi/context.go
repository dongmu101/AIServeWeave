package httpapi

import (
	"context"

	"AIServeWeave/common/reqid"
)

func withRequestID(ctx context.Context, id string) context.Context {
	return reqid.WithValue(ctx, id)
}

func requestIDFrom(ctx context.Context) string {
	return reqid.FromContext(ctx)
}
