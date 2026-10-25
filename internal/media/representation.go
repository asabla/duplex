// Package media supplies finite immutable byte representations. A URL is a
// locator, never a representation identity.
package media

import (
	"bytes"
	"context"
	"errors"
	"io"
)

// VerifyRanges checks beginning, middle, and end reads plus exact end-of-file behavior.
func VerifyRanges(ctx context.Context, r Representation) error {
	if r.Size() <= 0 {
		return ErrUnsupported
	}
	for _, offset := range []int64{0, r.Size() / 2, max(0, r.Size()-65536)} {
		size := min(int64(65536), r.Size()-offset)
		first, second := make([]byte, size), make([]byte, size)
		if n, e := r.ReadAt(ctx, first, offset); e != nil || n != len(first) {
			if e != nil {
				return e
			}
			return io.ErrUnexpectedEOF
		}
		if n, e := r.ReadAt(ctx, second, offset); e != nil || n != len(second) {
			if e != nil {
				return e
			}
			return io.ErrUnexpectedEOF
		}
		if !bytes.Equal(first, second) {
			return ErrChanged
		}
	}
	if n, err := r.ReadAt(ctx, make([]byte, 1), r.Size()); n != 0 || !errors.Is(err, io.EOF) {
		return ErrUnsupported
	}
	return nil
}

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

// Seeker is private to each parser or HTTP request. It does not share a cursor.
type Seeker struct {
	Context context.Context
	Source  Representation
	Offset  int64
}

func (s *Seeker) Read(p []byte) (int, error) {
	n, err := s.Source.ReadAt(s.Context, p, s.Offset)
	s.Offset += int64(n)
	return n, err
}

// Seek applies standard stream-relative offsets within an immutable representation.
func (s *Seeker) Seek(offset int64, whence int) (int64, error) {
	var base int64
	switch whence {
	case io.SeekStart:
	case io.SeekCurrent:
		base = s.Offset
	case io.SeekEnd:
		base = s.Source.Size()
	default:
		return s.Offset, errors.New("invalid seek origin")
	}
	if offset < -base || (offset > 0 && base > int64(^uint64(0)>>1)-offset) {
		return s.Offset, errors.New("invalid seek offset")
	}
	s.Offset = base + offset
	return s.Offset, nil
}
