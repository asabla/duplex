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
