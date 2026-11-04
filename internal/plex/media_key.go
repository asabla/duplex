package plex

import (
	"crypto/sha256"
	"encoding/hex"
	"path"
	"regexp"
	"strings"
)

var managedMediaName = regexp.MustCompile(`^Channel-[A-Za-z0-9_-]+ - S[0-9]+E[0-9]+\.mp4$`)

// MediaKey matches an opaque Duplex file slot across different mount roots.
// Paths, media titles, users, and player identifiers are not retained.
func MediaKey(file string) string {
	name := path.Base(strings.ReplaceAll(file, `\`, "/"))
	if len(name) > 255 || !managedMediaName.MatchString(name) {
		return ""
	}

	digest := sha256.Sum256([]byte(name))

	return hex.EncodeToString(digest[:])
}
