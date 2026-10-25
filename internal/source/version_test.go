package source

import (
	"net/url"
	"testing"
)

func TestSignedAssetVersionDoesNotDependOnLease(t *testing.T) {
	raw := "https://rr1.googlevideo.com/videoplayback?itag=401&clen=1000000&lmt=1774133434281302&mime=video%2Fmp4&dur=120&sig=signature&sparams=clen,lmt,mime,dur&expire=1&id=lease-a"
	key, size := assetVersion(raw, "401")
	if key == "" || size != 1000000 {
		t.Fatal("signed provider version missing")
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("expire", "2")
	q.Set("id", "lease-b")
	q.Set("sig", "new-signature")
	u.RawQuery = q.Encode()
	if next, n := assetVersion(u.String(), "401"); next != key || n != size {
		t.Fatal("lease became object identity")
	}
	for _, field := range []string{"sig", "sparams", "lmt", "clen", "mime", "dur"} {
		v := q.Clone()
		v.Del(field)
		u.RawQuery = v.Encode()
		if k, _ := assetVersion(u.String(), "401"); k != "" {
			t.Fatal("accepted missing evidence", field)
		}
	}
	for _, field := range []string{"lmt", "clen", "mime", "dur"} {
		v := q.Clone()
		v.Set(field, "123")
		u.RawQuery = v.Encode()
		if k, _ := assetVersion(u.String(), "401"); k == key {
			t.Fatal("version change ignored", field)
		}
	}
	if k, _ := assetVersion(raw, "140"); k != "" {
		t.Fatal("format identity mismatch")
	}
	for _, bad := range []string{"https://example.com/videoplayback", "http://rr1.googlevideo.com/videoplayback", "https://rr1.googlevideo.com/other"} {
		if k, _ := assetVersion(bad, "401"); k != "" {
			t.Fatal("untrusted provider version")
		}
	}
}
