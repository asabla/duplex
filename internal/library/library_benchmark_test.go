package library

import (
	"fmt"
	"testing"
)

// BenchmarkEntries measures projection metadata separately from media payloads.
// It exposes the cost of retaining all episode paths in a filesystem snapshot.
func BenchmarkEntries(b *testing.B) {
	for _, count := range []int{1000, 10000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			episodes := make([]Episode, count)
			data := blob{id: "fixture", data: []byte("controlled media")}
			for n := range episodes {
				episodes[n] = Episode{ChannelID: "channel", ChannelTitle: "Channel", VideoID: fmt.Sprintf("video%06d", n), Title: "Controlled recording", Season: 2026, Number: n + 1, Media: data}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				entries, err := Entries(episodes)
				if err != nil {
					b.Fatal(err)
				}
				if len(entries) != count*2+1 {
					b.Fatal("projection lost entries")
				}
			}
		})
	}
}
