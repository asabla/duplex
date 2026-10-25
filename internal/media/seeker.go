package media

import (
	"context"
	"errors"
	"io"
)

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
