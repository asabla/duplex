package source

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestPersistentThumbnailExcludesPrivateLocators(t *testing.T) {
	if got := PersistentThumbnail("https://i.ytimg.com/vi/example/hqdefault.jpg?signature=private#private"); got != "https://i.ytimg.com/vi/example/hqdefault.jpg" {
		t.Fatal(got)
	}

	for _, raw := range []string{"http://i.ytimg.com/poster", "https://i.ytimg.com.evil.test/poster", "https://user:secret@i.ytimg.com/poster", "https://r1.googlevideo.com/videoplayback?signature=private", "https://i.ytimg.com:444/poster", "invalid"} {
		if got := PersistentThumbnail(raw); got != "" {
			t.Fatal("private or unsupported locator retained", got)
		}
	}
}

func TestArtworkProfileNeedsNoFormatsAudioOrCompleteCatalogue(t *testing.T) {
	y, dir := resolverFixture(t)
	writeSourceFixture(t, filepath.Join(dir, "metadata-response"), `{"id":"aqz-KE-bpKQ","thumbnail":"https://i.ytimg.com/poster?signature=private"}`, 0o600)

	url, err := y.ArtworkURL(t.Context(), "aqz-KE-bpKQ")
	if err != nil || url != "https://i.ytimg.com/poster" {
		t.Fatal(url, err)
	}

	arguments, err := os.ReadFile(filepath.Join(dir, "arguments")) //nolint:gosec // G304: This reads only the controlled resolver log in t.TempDir.
	if err != nil || !bytes.Contains(arguments, []byte("player_skip=js,configs")) || bytes.Contains(arguments, []byte("formats=duplicate")) {
		t.Fatal("artwork used full playback resolution", string(arguments), err)
	}

	writeSourceFixture(t, filepath.Join(dir, "metadata-response"), `{"id":"aqz-KE-bpKQ"}`, 0o600)

	if _, err = y.ArtworkURL(t.Context(), "aqz-KE-bpKQ"); err == nil {
		t.Fatal("missing artwork was accepted")
	}

	if _, err = y.ArtworkURL(t.Context(), "invalid"); err == nil {
		t.Fatal("invalid identity was accepted")
	}
}
