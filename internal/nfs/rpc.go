package nfs

import "context"

func accepted(xid, status uint32, body []byte) encoder {
	var response encoder
	for _, value := range []uint32{xid, 1, 0, 0, 0, status} {
		response.u32(value)
	}

	return append(response, body...)
}

func (s *Server) rpc(ctx context.Context, request []byte) encoder {
	d := &decoder{data: request}
	xid, kind, version := d.u32(), d.u32(), d.u32()
	program, protocol, procedure := d.u32(), d.u32(), d.u32()
	flavor := d.u32()
	d.opaque(400)
	d.u32()
	d.opaque(400)

	if d.err != nil || kind != 0 || version != 2 || (flavor != 0 && flavor != 1) {
		return accepted(xid, 4, nil)
	}

	if program != 100003 && program != 100005 {
		return accepted(xid, 1, nil)
	}

	if protocol != 3 {
		var versions encoder
		versions.u32(3)
		versions.u32(3)

		return accepted(xid, 2, versions)
	}

	if procedure == 0 {
		return accepted(xid, 0, nil)
	}

	var body encoder
	if program == 100005 {
		body = s.mount(ctx, procedure, d)
	} else {
		body = s.operation(ctx, procedure, d)
	}

	if body == nil {
		return accepted(xid, 3, nil)
	}

	if d.err != nil || len(d.data) != 0 {
		return accepted(xid, 4, nil)
	}

	return accepted(xid, 0, body)
}

func (s *Server) mount(ctx context.Context, procedure uint32, d *decoder) encoder {
	var reply encoder

	switch procedure {
	case 1:
		name := string(d.opaque(1024))
		if name != "/media" || d.err != nil {
			reply.u32(13)
			return reply
		}

		root, err := s.describe(ctx, "")
		if err != nil || s.handles.save(ctx, []record{root}) != nil {
			reply.u32(5)
			return reply
		}

		reply.u32(0)
		reply.opaque(root.id)
		reply.u32(1)
		reply.u32(1) // AUTH_SYS. Access is restricted by the deployment network.
	case 2:
		reply.u32(0)
	case 4:
		return encoder{}
	case 3:
		d.opaque(1024)
		return encoder{}
	case 5:
		reply.u32(1)
		reply.opaque([]byte("/media"))
		reply.u32(0)
		reply.u32(0)
	default:
		return nil
	}

	return reply
}
