package media

import (
	"context"
	"errors"
	"math"
)

func (v *VirtualMP4) validateLayout() error {
	if v.Sources[0] == "" || v.Sources[1] == "" || v.Sources[0] == v.Sources[1] {
		return ErrUnsupported
	}

	next := int64(len(v.Header))
	for _, span := range v.Spans {
		if !validLayoutSpan(span, next, v.Length) {
			return ErrUnsupported
		}

		if span.SourceID != v.Sources[0] && span.SourceID != v.Sources[1] {
			return ErrUnsupported
		}

		if size := v.SourceLengths[span.SourceID]; size != 0 && (size < 0 || span.Offset+span.Length > size) {
			return ErrUnsupported
		}

		next += span.Length
	}

	if next != v.Length {
		return ErrUnsupported
	}

	return nil
}

func validLayoutSpan(span Span, next, length int64) bool {
	return span.Output == next && span.Offset >= 0 && span.Length > 0 && span.Length <= length-next && span.Offset <= math.MaxInt64-span.Length
}

func (v *VirtualMP4) sourceLengths() map[string]int64 {
	sizes := make(map[string]int64, len(v.Sources))
	for _, id := range v.Sources {
		if v.SourceLengths[id] > 0 {
			sizes[id] = v.SourceLengths[id]
		}
	}

	for _, span := range v.Spans {
		if span.source != nil {
			sizes[span.SourceID] = span.source.Size()
		}
	}

	return sizes
}

// BindLayout attaches readers to an already checked layout. A zero source size
// denotes an older index; the reader must bind before serving uncached payload.
func (v *VirtualMP4) BindLayout(reader func(string, int64) Representation) {
	sources := make(map[string]Representation, len(v.Sources))
	for _, id := range v.Sources {
		sources[id] = reader(id, v.SourceLengths[id])
	}

	for i := range v.Spans {
		v.Spans[i].source = sources[v.Spans[i].SourceID]
	}
}

// ReadSource exposes only the freshly verified track matching an immutable ID.
func (v *VirtualMP4) ReadSource(ctx context.Context, id string, p []byte, offset int64) (int, error) {
	for _, span := range v.Spans {
		if span.SourceID == id && span.source != nil {
			return span.source.ReadAt(ctx, p, offset)
		}
	}

	return 0, errors.New("index source is not bound")
}
