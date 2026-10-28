package nfs

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"math"

	"duplex/internal/media"
)

func (s *Server) describe(ctx context.Context, name string) (record, error) {
	info, err := s.provider.Stat(ctx, name)
	if err == nil && !info.Directory && (info.Revision == "" || info.Size < 0) {
		return record{}, errors.New("file has no verified immutable identity")
	}

	return s.handles.describe(name, info), err
}

func status(err error) uint32 {
	switch {
	case err == nil:
		return 0
	case errors.Is(err, fs.ErrNotExist):
		return 2
	case errors.Is(err, fs.ErrPermission):
		return 13
	case errors.Is(err, fs.ErrInvalid):
		return 22
	case errors.Is(err, media.ErrChanged), errors.Is(err, sql.ErrNoRows):
		return 70
	default:
		return 5
	}
}

func attributes(e *encoder, r record) {
	kind, mode, links, size := uint32(1), uint32(0o444), uint32(1), unsigned(r.info.Size)
	if r.info.Directory {
		kind, mode, links, size = 2, 0o555, 2, 4096
	}

	for _, v := range []uint32{kind, mode, links, 10001, 10001} {
		e.u32(v)
	}

	e.u64(size)
	e.u64(size)
	e.u32(0)
	e.u32(0)
	e.u64(1)
	e.u64(cookie(r.id))

	for range 3 {
		e.u32(uint32(max(0, min(r.info.Modified, math.MaxUint32))))
		e.u32(0)
	}
}

func postAttributes(e *encoder, r record) {
	e.u32(1)
	attributes(e, r)
}

func operationError(code uint32, procedure uint32) encoder {
	var reply encoder
	reply.u32(code)

	if procedure == 1 {
		return reply
	}

	reply.u32(0)

	if procedure == 2 || (procedure >= 7 && procedure <= 15) || procedure == 21 {
		reply.u32(0)
	}

	if procedure == 14 {
		reply.u32(0)
		reply.u32(0)
	}

	if procedure == 15 {
		reply.u32(0)
	}

	return reply
}
