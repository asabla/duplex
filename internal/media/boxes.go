package media

import (
	"bytes"
	"encoding/binary"
	"math"

	"github.com/Eyevinn/mp4ff/mp4"
)

type sourceBox struct {
	offset, size int64
	kind         string
}

func (r *indexReader) boxHeader(offset int64) (sourceBox, error) {
	box := sourceBox{offset: offset}
	if offset < 0 || offset > r.source.Size() {
		return box, ErrUnsupported
	}

	header := make([]byte, 16)
	if err := r.read(header[:8], offset); err != nil {
		return box, err
	}

	box.size = int64(binary.BigEndian.Uint32(header))
	box.kind = string(header[4:8])
	minimum := int64(8)

	if box.size == 1 {
		if err := r.read(header[8:], offset+8); err != nil {
			return box, err
		}

		size := binary.BigEndian.Uint64(header[8:])
		if size > math.MaxInt64 {
			return box, ErrUnsupported
		}

		box.size = int64(size)
		minimum = 16
	}

	if box.size == 0 {
		box.size = r.source.Size() - offset
	}

	if box.size < minimum || box.size > r.source.Size()-offset {
		return box, ErrUnsupported
	}

	return box, nil
}

func (r *indexReader) decode(box sourceBox, maximum int64) (mp4.Box, error) {
	if box.offset < 0 || box.size < 8 || box.size > maximum {
		return nil, ErrUnsupported
	}

	data := make([]byte, box.size)
	if err := r.read(data, box.offset); err != nil {
		return nil, err
	}

	result, err := mp4.DecodeBox(uint64(box.offset), bytes.NewReader(data))
	if err != nil {
		return nil, ErrUnsupported
	}

	return result, nil
}
