package artwork

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"os"
	"path/filepath"

	"duplex/internal/config"
)

// Save stores only normalized, content-addressed assets outside the media cache.
func Save(dir string, data []byte) (string, error) {
	info, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || format != "jpeg" || info.Width != Width || info.Height != Height {
		return "", ErrImage
	}

	hash := sha256.Sum256(data)
	ref := hex.EncodeToString(hash[:]) + ".jpg"

	if err = os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}

	if err = config.AtomicWrite(filepath.Join(dir, ref), data, 0o600); err != nil {
		return "", err
	}

	return ref, nil
}

// Path validates a YAML reference before constructing a local asset path.
func Path(dir, reference string) (string, error) {
	if !config.ValidArtworkReference(reference) {
		return "", fmt.Errorf("invalid artwork reference")
	}

	return filepath.Join(dir, reference), nil
}
