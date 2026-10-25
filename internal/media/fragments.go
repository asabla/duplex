package media

import (
	"context"
	"fmt"
	"math"
	"sort"
	"sync"
	"time"

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

type indexWindow struct {
	offset int64
	data   []byte
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

func fragmentWindows(index *mp4.SidxBox, size, budget int64) ([]indexWindow, int64, error) {
	if len(index.SidxRefs) == 0 || size < 1 || index.AnchorPoint > math.MaxInt64 {
		return nil, 0, ErrUnsupported
	}
	position := int64(index.AnchorPoint)
	if position > size {
		return nil, 0, ErrUnsupported
	}
	windows := make([]indexWindow, 0, len(index.SidxRefs))
	var required int64
	for _, ref := range index.SidxRefs {
		if ref.ReferenceType != 0 {
			return nil, 0, nil
		}
		length := int64(ref.ReferencedSize)
		if length < 8 || length > size-position {
			return nil, 0, ErrUnsupported
		}
		window := min(length, int64(4096))
		if required+window > budget {
			return nil, 0, fmt.Errorf("%w: fragment header budget exceeds 64 MiB", ErrUnsupported)
		}
		required += window
		windows = append(windows, indexWindow{offset: position, data: make([]byte, window)})
		position += length
	}
	return windows, required, nil
}

func fetchFragmentWindows(ctx context.Context, source *HTTPSource, windows []indexWindow) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan int)
	failures := make(chan error, 1)
	feederDone := make(chan struct{})
	var workers sync.WaitGroup
	for range min(8, len(windows)) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			if err := fetchWindowJobs(ctx, source, windows, jobs); err != nil {
				select {
				case failures <- err:
				default:
				}
				cancel()
			}
		}()
	}
	go func() { defer close(feederDone); defer close(jobs); feedWindowJobs(ctx, len(windows), jobs) }()
	workers.Wait()
	<-feederDone
	select {
	case err := <-failures:
		return err
	default:
	}
	return ctx.Err()
}

func fetchWindowJobs(ctx context.Context, source *HTTPSource, windows []indexWindow, jobs <-chan int) error {
	for n := range jobs {
		window := &windows[n]
		data, err := source.fetch(ctx, window.offset, len(window.data))
		if err != nil {
			return err
		}
		window.data = data
	}
	return nil
}

func feedWindowJobs(ctx context.Context, count int, jobs chan<- int) {
	// Pace tiny requests even when a warm CDN can respond immediately.
	ticker := time.NewTicker(40 * time.Millisecond)
	defer ticker.Stop()
	for n := range count {
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return
		}
		select {
		case jobs <- n:
		case <-ctx.Done():
			return
		}
	}
}
