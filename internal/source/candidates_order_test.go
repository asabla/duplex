package source

import "testing"

func TestCandidateOrderingIsStrict(t *testing.T) {
	var candidates []Candidate

	for _, height := range []int{360, 1080, 2160} {
		for _, combined := range []bool{false, true} {
			candidates = append(candidates, Candidate{Height: height, Combined: combined})
		}
	}

	for _, a := range candidates {
		if candidateLess(a, a) {
			t.Fatal("candidate sorts before itself", a)
		}

		for _, b := range candidates {
			if candidateLess(a, b) && candidateLess(b, a) {
				t.Fatal("ordering is not asymmetric", a, b)
			}

			for _, c := range candidates {
				if candidateLess(a, b) && candidateLess(b, c) && !candidateLess(a, c) {
					t.Fatal("ordering is not transitive", a, b, c)
				}
			}
		}
	}
}

func TestCandidateOrderingPreservesEqualRank(t *testing.T) {
	metadata := Metadata{}
	for _, id := range []string{"first", "second", "third"} {
		metadata.Formats = append(metadata.Formats, Format{ID: id, URL: "https://r1.googlevideo.com/" + id, Protocol: "https", Ext: "mp4", VCodec: "avc1", ACodec: "mp4a", Height: 1080})
	}

	for range 3 {
		ranked := Candidates(metadata, 2160)
		if len(ranked) != 3 {
			t.Fatal("missing candidates", ranked)
		}

		for i, id := range []string{"first", "second", "third"} {
			if ranked[i].Video.ID != id {
				t.Fatal("equal-rank candidates changed order", ranked)
			}

			metadata.Formats[i] = ranked[i].Video
		}
	}
}
