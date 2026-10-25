package cache

import (
	"errors"
	"io"
	"os"
)

func (s *Store) writeChunk(data []byte, reservation int64) (name string, allocation int64, err error) {
	file, err := os.CreateTemp(s.dir, ".part-")
	if err != nil {
		return "", 0, err
	}

	name = file.Name()

	written, err := file.Write(data)
	if err == nil && written != len(data) {
		err = io.ErrShortWrite
	}

	if err == nil {
		err = file.Sync()
	}

	err = errors.Join(err, file.Close())
	if err != nil {
		return name, 0, err
	}

	info, err := os.Stat(name) // #nosec G703 -- The writer selects this private temporary chunk path.
	if err != nil {
		return name, 0, err
	}

	allocation = s.allocation(info)
	if allocation > reservation {
		return name, allocation, errors.New("cache allocation exceeds reservation")
	}

	return name, allocation, nil
}

func syncChunkDirectory(path string) error {
	directory, err := os.Open(path) //nolint:gosec // G304: The cache store selects this private chunk directory.
	if err != nil {
		return err
	}

	return errors.Join(directory.Sync(), directory.Close())
}

func removeChunkFile(path string) error {
	if path == "" {
		return nil
	}

	err := os.Remove(path) // #nosec G703 -- Cleanup paths are constructed by the cache store; no client path is accepted.
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}

	return err
}

func cleanupChunk(temporary, published string) (retained string, err error) {
	if failure := removeChunkFile(temporary); failure != nil {
		retained = temporary
		err = failure
	}

	if failure := removeChunkFile(published); failure != nil {
		retained = published
		err = errors.Join(err, failure)
	}

	return retained, err
}

func readChunkFile(path string, length int) (data []byte, err error) {
	file, err := os.Open(path) //nolint:gosec // G304: The cache store constructs chunk paths from hashed immutable identities.
	if err != nil {
		return nil, err
	}

	defer func() { err = errors.Join(err, file.Close()) }()
	// The chunk length is bounded by Get. Keep one extra byte to detect a
	// corrupted oversized file without growing and copying a read buffer.
	data = make([]byte, length+1)

	count, failure := io.ReadFull(file, data)
	if errors.Is(failure, io.EOF) || errors.Is(failure, io.ErrUnexpectedEOF) {
		failure = nil
	}

	return data[:count], failure
}
