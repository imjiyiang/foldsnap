package fsevents

import "context"

func WatchMounts(ctx context.Context, notify func()) error { <-ctx.Done(); return nil }
