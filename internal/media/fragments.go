package media

import (
	"context"
	"fmt"
	"sort"

	"github.com/Eyevinn/mp4ff/mp4"
)

// Index reads use small windows instead of filling a 1 MiB media chunk for each
// fragment header. Media bodies are skipped. This state lasts only for a build.
type indexReader struct {
	ctx     context.Context
	source  Representation
	data    []byte
	offset  int64
	total   int64
	windows []indexWindow
}

func (r *indexReader) read(p []byte, offset int64) error {
	if offset < 0 || int64(len(p)) > r.source.Size()-offset {
		return ErrUnsupported
	}

	if offset >= r.offset && offset+int64(len(p)) <= r.offset+int64(len(r.data)) {
		copy(p, r.data[offset-r.offset:])
		return nil
	}

	n := sort.Search(len(r.windows), func(n int) bool { return r.windows[n].offset+int64(len(r.windows[n].data)) > offset })
	if n < len(r.windows) {
		w := r.windows[n]
		if offset >= w.offset && offset+int64(len(p)) <= w.offset+int64(len(w.data)) {
			copy(p, w.data[offset-w.offset:])
			return nil
		}
	}

	length := min(max(len(p), 16*1024), int(r.source.Size()-offset))
	if r.total+int64(length) > maxIndexBytes {
		return fmt.Errorf("%w: index traffic exceeds 64 MiB", ErrUnsupported)
	}

	r.data = make([]byte, length)
	r.offset = offset
	r.total += int64(length)

	var err error
	if source, ok := r.source.(*HTTPSource); ok {
		r.data, err = source.fetch(r.ctx, offset, length)
	} else {
		_, err = r.source.ReadAt(r.ctx, r.data, offset)
	}

	if err != nil {
		return err
	}

	copy(p, r.data)

	return nil
}

// A flat sidx provides independent fragment offsets. Fetch bounded header
// windows with eight workers, then parse in source order. This avoids a serial
// round trip for every fragment in a multi-hour replay.
func (r *indexReader) prefetch(ctx context.Context, index *mp4.SidxBox) error {
	source, ok := r.source.(*HTTPSource)
	if !ok || len(r.windows) > 0 {
		return nil
	}

	windows, required, err := fragmentWindows(index, source.Size(), maxIndexBytes-r.total)
	if err != nil {
		return err
	}

	if len(windows) == 0 {
		return nil
	}

	r.total += required

	if err = fetchFragmentWindows(ctx, source, windows); err != nil {
		return err
	}

	r.windows = windows

	return nil
}
