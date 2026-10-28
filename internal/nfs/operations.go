package nfs

import (
	"context"
	"errors"
	"io"
	"math"
	"path"
	"strings"
)

func (s *Server) operation(ctx context.Context, procedure uint32, d *decoder) encoder {
	if procedure < 1 || procedure > 21 {
		return nil
	}

	if mutation(procedure) {
		d.data = nil // Every mutation is refused, including SETATTR and COMMIT.
		return operationError(30, procedure)
	}

	r, err := s.handles.load(ctx, d.opaque(64))
	if err != nil || d.err != nil {
		return operationError(70, procedure)
	}

	if r.info.Directory {
		r.info, err = s.provider.Stat(ctx, r.name)
		if err != nil {
			return operationError(status(err), procedure)
		}
	}

	return s.readOperation(ctx, r, procedure, d)
}

func mutation(procedure uint32) bool {
	switch procedure {
	case 2, 7, 8, 9, 10, 11, 12, 13, 14, 15, 21:
		return true
	default:
		return false
	}
}

func (s *Server) readOperation(ctx context.Context, r record, procedure uint32, d *decoder) encoder {
	switch procedure {
	case 1:
		var reply encoder
		reply.u32(0)
		attributes(&reply, r)

		return reply
	case 3:
		return s.lookup(ctx, r, d)
	case 4:
		return access(r, d)
	case 5:
		return operationError(22, procedure)
	case 6:
		return s.read(ctx, r, d)
	case 16, 17:
		return s.readDirectory(ctx, r, d, procedure == 17)
	default:
		return filesystemInfo(r, procedure)
	}
}

func (s *Server) lookup(ctx context.Context, parent record, d *decoder) encoder {
	name := string(d.opaque(255))

	if !parent.info.Directory {
		return operationError(20, 3)
	}

	if name == "" || strings.ContainsAny(name, "/\x00") || d.err != nil {
		return operationError(13, 3)
	}

	switch name {
	case ".":
		name = parent.name
	case "..":
		name = path.Dir(parent.name)
		if name == "." {
			name = ""
		}
	default:
		name = path.Join(parent.name, name)
	}

	r, err := s.describe(ctx, name)
	if err != nil {
		return operationError(status(err), 3)
	}

	if err = s.handles.save(ctx, []record{r}); err != nil {
		return operationError(5, 3)
	}

	var reply encoder
	reply.u32(0)
	reply.opaque(r.id)
	postAttributes(&reply, r)
	postAttributes(&reply, parent)

	return reply
}

func access(r record, d *decoder) encoder {
	mask := uint32(1)
	if r.info.Directory {
		mask |= 2 | 32
	}

	requested := d.u32()

	var reply encoder
	reply.u32(0)
	postAttributes(&reply, r)
	reply.u32(mask & requested)

	return reply
}

func (s *Server) read(ctx context.Context, r record, d *decoder) encoder {
	offset, count := d.u64(), min(d.u32(), uint32(maxRead))

	if r.info.Directory {
		return operationError(21, 6)
	}

	if offset > math.MaxInt64 || d.err != nil {
		return operationError(22, 6)
	}

	rep, err := s.provider.OpenVersion(ctx, r.name, r.info.Revision)
	if err != nil {
		return operationError(status(err), 6)
	}

	if rep.ID() != r.info.Revision || rep.Size() != r.info.Size {
		return operationError(70, 6)
	}

	data := make([]byte, min(int64(count), max(0, rep.Size()-int64(offset))))

	n, err := rep.ReadAt(ctx, data, int64(offset))
	if (err != nil && !errors.Is(err, io.EOF)) || n != len(data) {
		return operationError(5, 6)
	}

	var reply encoder
	reply.u32(0)
	postAttributes(&reply, r)
	reply.u32(size32(n))

	end := uint32(0)
	if int64(offset)+int64(n) >= r.info.Size {
		end = 1
	}

	reply.u32(end)
	reply.opaque(data)

	return reply
}

func filesystemInfo(r record, procedure uint32) encoder {
	var reply encoder
	reply.u32(0)
	postAttributes(&reply, r)

	switch procedure {
	case 18:
		for range 6 {
			reply.u64(1 << 40)
		}

		reply.u32(0)
	case 19:
		for _, value := range []uint32{maxRead, maxRead, 4096, 0, 0, 4096, 32768} {
			reply.u32(value)
		}

		reply.u64(math.MaxInt64)
		reply.u32(1)
		reply.u32(0)
		reply.u32(8)
	case 20:
		for _, value := range []uint32{1, 255, 1, 1, 0, 1} {
			reply.u32(value)
		}
	}

	return reply
}
