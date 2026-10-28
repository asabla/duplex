// Package nfs exports the read-only library through bounded NFSv3 TCP requests.
package nfs

import (
	"encoding/binary"
	"errors"
)

var errEncoding = errors.New("invalid XDR request")

type decoder struct {
	data []byte
	err  error
}

func (d *decoder) take(count int) []byte {
	if count < 0 || count > len(d.data) || d.err != nil {
		d.err = errEncoding
		return nil
	}

	value := d.data[:count]
	d.data = d.data[count:]

	return value
}

func (d *decoder) u32() uint32 {
	value := d.take(4)
	if value == nil {
		return 0
	}

	return binary.BigEndian.Uint32(value)
}

func (d *decoder) u64() uint64 { return uint64(d.u32())<<32 | uint64(d.u32()) }

func (d *decoder) opaque(limit uint32) []byte {
	count := d.u32()
	if count > limit {
		d.err = errEncoding
		return nil
	}

	value := d.take(int(count))
	d.take(int((4 - count%4) % 4))

	return value
}

type encoder []byte

func (e *encoder) u32(value uint32) { *e = binary.BigEndian.AppendUint32(*e, value) }
func (e *encoder) u64(value uint64) { *e = binary.BigEndian.AppendUint64(*e, value) }

func (e *encoder) opaque(value []byte) {
	e.u32(size32(len(value)))
	*e = append(*e, value...)
	*e = append(*e, make([]byte, (4-len(value)%4)%4)...)
}

func size32(value int) uint32 {
	if value < 0 || value > maxRequest {
		panic("NFS encoding exceeds the bounded record size")
	}

	return uint32(value)
}

func unsigned(value int64) uint64 {
	if value < 0 {
		return 0
	}

	return uint64(value)
}
