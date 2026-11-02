package source

import (
	"path/filepath"
	"testing"
)

func TestProcessCountersDistinguishInitiatingCauseWithoutArguments(t *testing.T) {
	y, dir := resolverFixture(t)
	complete := `{"id":"aqz-KE-bpKQ","channel_id":"` + fixtureChannel + `","title":"Controlled","duration":60,"timestamp":1700000000}`
	writeSourceFixture(t, filepath.Join(dir, "response"), complete, 0o600)
	writeSourceFixture(t, filepath.Join(dir, "metadata-response"), `{"id":"aqz-KE-bpKQ","thumbnail":"https://i.ytimg.com/poster"}`, 0o600)

	counts := map[string]int64{}

	observed := Observed{SourceAdapter: y, Count: func(name string, n int64) { counts[name] += n }}
	if _, err := observed.Metadata(t.Context(), "aqz-KE-bpKQ"); err != nil {
		t.Fatal(err)
	}

	if counts["resolver.process.started"] != 2 || counts["resolver.process.discovery"] != 2 {
		t.Fatal("catalogue fallback process count was hidden", counts)
	}

	if _, err := observed.Resolve(WithCause(t.Context(), CauseRenewal), "aqz-KE-bpKQ"); err != nil {
		t.Fatal(err)
	}

	reader, ok := ArtworkReader(observed)
	if !ok {
		t.Fatal("artwork capability lost")
	}

	if _, err := reader.ArtworkURL(t.Context(), "aqz-KE-bpKQ"); err != nil {
		t.Fatal(err)
	}

	if counts["resolver.process.started"] != 4 || counts["resolver.process.renewal"] != 1 || counts["resolver.process.asset"] != 1 {
		t.Fatal(counts)
	}

	if CauseName(WithCause(t.Context(), ResolutionCause(255))) != "unspecified" {
		t.Fatal("counter vocabulary was unbounded")
	}
}
