package nfs

// Validate the complete read-only argument shape before any provider or handle
// access. Mutations retain unconditional ROFS refusal; unknown procedures retain
// PROC_UNAVAIL. Semantic checks remain with the operation that owns them.
func validArguments(program, procedure uint32, data []byte) bool {
	d := &decoder{data: data}
	if program == 100005 {
		return validMountArguments(procedure, d)
	}

	if procedure > 21 || mutation(procedure) {
		return true
	}

	if procedure != 0 {
		d.opaque(64)
	}

	switch procedure {
	case 3:
		d.opaque(255)
	case 4:
		d.u32()
	case 6:
		d.u64()
		d.u32()
	case 16, 17:
		d.u64()
		d.u64()
		d.u32()

		if procedure == 17 {
			d.u32()
		}
	}

	return d.err == nil && len(d.data) == 0
}

func validMountArguments(procedure uint32, d *decoder) bool {
	switch procedure {
	case 1, 3:
		d.opaque(1024)
	case 0, 2, 4, 5:
	default:
		return true
	}

	return d.err == nil && len(d.data) == 0
}
