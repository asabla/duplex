// Package media supplies finite immutable byte representations. A URL is a
// locator, never a representation identity.
package media

import (
	"context"
	"errors"
)

// Representation errors distinguish changed content, expired leases, and unsupported containers.
var (
	ErrChanged     = errors.New("source representation changed")
	ErrExpired     = errors.New("source locator expired; open a new representation")
	ErrUnsupported = errors.New("unsupported media representation")
)

// Representation is an immutable, exact-size object with context-aware offset reads.
type Representation interface {
	ID() string
	Size() int64
	ReadAt(context.Context, []byte, int64) (int, error)
}

// Inspected adds verified container details to the byte-range source
// contract. Metadata files use Representation without media codec information.
type Inspected interface {
	Representation
	Details() Details
}

// Details contains the verified codec and container properties.
type Details struct {
	Container       string  `json:"container"`
	VideoCodec      string  `json:"video_codec"`
	AudioCodec      string  `json:"audio_codec"`
	Width           int     `json:"width"`
	Height          int     `json:"height"`
	DurationSeconds float64 `json:"duration_seconds"`
}

// Described attaches inspected media properties to an underlying byte representation.
type Described struct {
	Representation
	Info Details
}

// Details returns the verified codec and container properties.
func (r Described) Details() Details { return r.Info }
