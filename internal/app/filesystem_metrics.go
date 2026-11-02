package app

import (
	"context"
	"strings"

	"duplex/internal/diagnostics"
	"duplex/internal/media"
)

// The operation vocabulary is bounded and never contains paths or media IDs.
func filesystemOperation(operation, name string) string {
	if strings.HasSuffix(name, ".mp4") {
		return "filesystem." + operation + ".media"
	}

	return "filesystem." + operation + ".metadata"
}

type observedMedia struct {
	media.Representation
	recorder  *diagnostics.Recorder
	operation string
}

func (m observedMedia) ReadAt(ctx context.Context, data []byte, offset int64) (int, error) {
	defer m.recorder.Begin(m.operation)()

	n, err := m.Representation.ReadAt(ctx, data, offset)
	if err != nil {
		m.recorder.Count(m.operation+".errors", 1)
	}

	return n, err
}

func (p catalogueFS) observedFile(rep media.Representation, name string) media.Representation {
	if rep == nil {
		return nil
	}

	if _, ok := rep.(observedMedia); ok {
		return rep
	}

	return observedMedia{Representation: rep, recorder: p.app.Measurements, operation: filesystemOperation("read", name)}
}
