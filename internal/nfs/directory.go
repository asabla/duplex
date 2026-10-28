package nfs

import (
	"context"
	"path"
)

func (s *Server) readDirectory(ctx context.Context, directory record, d *decoder, plus bool) encoder {
	cursor, verifier, count := d.u64(), d.u64(), min(d.u32(), uint32(maxRead))
	maximum := count
	procedure := uint32(16)

	if plus {
		maximum = min(d.u32(), uint32(maxRead))
		procedure = 17
	}

	if !directory.info.Directory {
		return operationError(20, procedure)
	}

	if count < 512 || maximum < 512 {
		return operationError(10005, procedure)
	}

	current := unsigned(directory.info.Modified)
	after := ""

	if cursor != 0 {
		if verifier != current {
			return operationError(10003, procedure)
		}

		var err error

		after, err = s.handles.after(ctx, directory.id, cursor)
		if err != nil {
			return operationError(10003, procedure)
		}
	}

	return s.directoryPage(ctx, directory, after, current, min(count, maximum), plus)
}

func (s *Server) directoryPage(ctx context.Context, directory record, after string, verifier uint64, maximum uint32, plus bool) encoder {
	procedure := uint32(16)
	if plus {
		procedure = 17
	}

	limit := min(128, int(maximum-128)/256)

	children, err := s.provider.ReadDir(ctx, directory.name, after, limit+1)
	if err != nil {
		return operationError(status(err), procedure)
	}

	eof := len(children) <= limit
	if !eof {
		children = children[:limit]
	}

	records := make([]record, 0, len(children))

	var entries encoder

	for _, child := range children {
		r, err := s.describe(ctx, path.Join(directory.name, child.Name))
		if err != nil {
			return operationError(status(err), procedure)
		}

		entry := directoryEntry(r, plus)
		if len(entries)+len(entry)+128 > int(maximum) {
			if len(entries) == 0 {
				return operationError(10005, procedure)
			}

			eof = false

			break
		}

		entries = append(entries, entry...)
		records = append(records, r)
	}

	if err = s.handles.save(ctx, records); err != nil {
		return operationError(5, procedure)
	}

	var reply encoder
	reply.u32(0)
	postAttributes(&reply, directory)
	reply.u64(verifier)
	reply = append(reply, entries...)
	reply.u32(0)

	if eof {
		reply.u32(1)
	} else {
		reply.u32(0)
	}

	return reply
}

func directoryEntry(r record, plus bool) encoder {
	var entry encoder
	entry.u32(1)
	entry.u64(cookie(r.id))
	entry.opaque([]byte(path.Base(r.name)))
	entry.u64(cookie(r.id))

	if plus {
		postAttributes(&entry, r)
		entry.u32(1)
		entry.opaque(r.id)
	}

	return entry
}
