package library

import (
	"context"

	"duplex/internal/media"
)

// Node describes a verified file or directory without loading media indexes.
type Node struct {
	Directory bool
	Size      int64
	Modified  int64
	Revision  string // Immutable content identity; directories have no revision.
}

// VersionedProvider refuses to replace the bytes identified by an earlier lookup.
// An unavailable revision must fail rather than resolve to current path contents.
type VersionedProvider interface {
	Provider
	OpenVersion(context.Context, string, string) (media.Representation, error)
}

// Child is one stable directory entry. Names are ordered lexically for paging.
type Child struct {
	Name      string
	Directory bool
}

// Provider supplies bounded filesystem reads from durable catalogue state.
// Open must bind one immutable representation for the lifetime of its handle.
type Provider interface {
	Stat(context.Context, string) (Node, error)
	ReadDir(context.Context, string, string, int) ([]Child, error)
	Open(context.Context, string) (media.Representation, int64, error)
}
