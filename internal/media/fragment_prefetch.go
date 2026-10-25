package media

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/Eyevinn/mp4ff/mp4"
)

type indexWindow struct {
	offset int64
	data   []byte
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

	go func() {
		defer close(feederDone)
		defer close(jobs)

		feedWindowJobs(ctx, len(windows), jobs)
	}()

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
