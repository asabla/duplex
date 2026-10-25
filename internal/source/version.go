package source

import (
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"strconv"
	"strings"
)

// assetVersion uses the authenticated provider's signed object version, not its
// changing lease URL. The HTTP adapter must also verify size and fresh ranges
// before using it as a reusable namespace. Missing evidence disables reuse.
func assetVersion(raw, format string) (string, int64) {
	u, err := url.Parse(raw)
	if err != nil || !AllowedURL(raw) || !strings.HasSuffix(strings.ToLower(u.Hostname()), ".googlevideo.com") || u.Path != "/videoplayback" {
		return "", 0
	}
	q := u.Query()
	if q.Get("sig") == "" && q.Get("signature") == "" {
		return "", 0
	}
	signed := map[string]bool{}
	for _, key := range strings.Split(q.Get("sparams"), ",") {
		signed[key] = true
	}
	for _, key := range []string{"clen", "lmt", "mime", "dur"} {
		if !signed[key] || q.Get(key) == "" {
			return "", 0
		}
	}
	size, err := strconv.ParseInt(q.Get("clen"), 10, 64)
	if err != nil || size <= 0 {
		return "", 0
	}
	modified, err := strconv.ParseUint(q.Get("lmt"), 10, 64)
	if err != nil || modified == 0 {
		return "", 0
	}
	if q.Get("itag") != format {
		return "", 0
	}
	data := strings.Join([]string{format, q.Get("clen"), q.Get("lmt"), q.Get("mime"), q.Get("dur")}, "\x00")
	h := sha256.Sum256([]byte(data))
	return hex.EncodeToString(h[:]), size
}
