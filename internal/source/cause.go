package source

import (
	"context"
	"os"
)

// ResolutionCause is a bounded operation class, never a media ID or locator.
type ResolutionCause uint8

// Resolution causes identify the operation which starts shared source work.
const (
	CauseUnspecified ResolutionCause = iota
	CauseDiscovery
	CausePreparation
	CauseFilesystem
	CauseAsset
	CauseRenewal
)

type (
	causeKey          struct{}
	processCounterKey struct{}
)

// WithCause carries the initiating operation through shared resolver work.
func WithCause(ctx context.Context, cause ResolutionCause) context.Context {
	return context.WithValue(ctx, causeKey{}, cause)
}

// CauseName returns only a fixed vocabulary suitable for aggregate counters.
func CauseName(ctx context.Context) string {
	switch ctx.Value(causeKey{}) {
	case CauseDiscovery:
		return "discovery"
	case CausePreparation:
		return "preparation"
	case CauseFilesystem:
		return "filesystem"
	case CauseAsset:
		return "asset"
	case CauseRenewal:
		return "renewal"
	default:
		return "unspecified"
	}
}

func (s Observed) processContext(ctx context.Context, cause ResolutionCause) context.Context {
	if ctx.Value(causeKey{}) == nil {
		ctx = WithCause(ctx, cause)
	}

	if s.Count != nil {
		ctx = context.WithValue(ctx, processCounterKey{}, s.Count)
	}

	return ctx
}

func processStarted(ctx context.Context) {
	if count, ok := ctx.Value(processCounterKey{}).(func(string, int64)); ok {
		count("resolver.process.started", 1)
		count("resolver.process."+CauseName(ctx), 1)
	}
}

func processFinished(ctx context.Context, state *os.ProcessState) {
	if state == nil {
		return
	}

	if count, ok := ctx.Value(processCounterKey{}).(func(string, int64)); ok {
		count("resolver.process_cpu_ns."+CauseName(ctx), int64(state.UserTime()+state.SystemTime()))
	}
}
