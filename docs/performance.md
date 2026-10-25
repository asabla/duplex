# Performance evidence

The cleanup measurements below use an isolated Colima container. They do not
measure a native Linux installation. Raw profiles and source responses remain
outside Git because they can contain local data or signed source URLs.

## Baseline

The original sequential pipeline indexed two jackfrags recordings. A cold sync
took 111.987 seconds. Enumeration took 28.500 seconds, six metadata operations
used 12.872 seconds, and two preparations used 70.561 seconds. A 180-second CPU
profile recorded 2.20 seconds of Go CPU time. Most elapsed time was spent waiting.

A repeated instrumented run took 115.469 seconds. Building two container indexes
used 70.024 seconds; the longest used 51.227 seconds. Probing, verification, and
optional assets used 0.384, 0.382, and 0.126 seconds respectively. The old source
identity changed at every resolution, so saved indexes could not be rebound.

The household catalogue contained 5,806 durable video records. The status JSON
response was about 1.54 MB. Status took about 18–19 ms; a 539-byte progress fragment
still took about 12 ms because it loaded the complete catalogue. These are local
observations, not latency guarantees.

## Verified index reuse

A completed NASA replay (`k0ILMhbWHNQ`, formats 401 and 140) was resolved twice.
The test independently opened both leases, checked the provider version and
exact size, and fetched fresh beginning, middle, and end range fingerprints.
It then loaded the saved progressive MP4 index and checked representation identity,
exact size, and output ranges.

| Operation | Elapsed | Upstream requests |
| --- | ---: | ---: |
| Cold progressive index | 246.445 s | 5,215 |
| Fresh verification and index rebind | 1.839 s | 11, including output verification |

The rebind test transferred 2,349,394 bytes from 7,132,272,976 bytes of source
tracks. The same test passed under the Go race detector. This proves the tested
index can be reused across renewed leases. It does not establish Plex startup
latency or complete the media qualification gate. Missing version evidence still
uses a fresh namespace. A changed size or fingerprint prevents index reuse.

## Rejected experiment

A lazy fragmented MP4 prototype passed controlled packet and decoded-audio
comparisons. With real 4K sources, clients read too many fragment headers while
seeking; one replay timed out. Adding a front index did not resolve the failure.
The prototype was removed. The product retains the progressive MP4 range mapper.

## Bounded filesystem reads

The static projection benchmark allocated 5,970,680 bytes for 1,000 episode
entries and 60,165,448 bytes for 10,000. The database-backed projection removes
that complete snapshot from publication. It reads directory entries in pages
and binds a verified media index when Plex opens a recording.

Three runs on an Apple M3 Max with Go 1.27.3 produced these results:

| Catalogue size | First page of 128 directory entries | Allocation per page |
| --- | ---: | ---: |
| 1,000 recordings | 2.13–2.54 ms | 218,930–219,268 bytes |
| 10,000 recordings | 15.42–16.28 ms | 218,900–218,981 bytes |

Publication itself allocated no memory in this benchmark. This measures the
change in publication work; filesystem reads now perform the queries. It is not
an end-to-end speedup measurement. Page allocation stayed stable, but query time
increased with the catalogue size. Heap profiles and matched Plex workloads are
still required for release qualification.

The runtime cache holds at most 128 representations and uses a 64 MiB index
budget estimate. One larger index can remain cached within the parser's separate
size bound. Open handles retain their own representation references. Media chunk
storage still uses the independently configured disk cache limit.

## Catalogue metadata profile

Flat entries from three current channel uploads omitted uploader IDs and
publication timestamps. They are insufficient for exact Latest N selection.
The catalogue request profile uses documented yt-dlp extraction options to omit
manifest and player JavaScript work. Missing required fields trigger full
resolution. Playback formats are resolved separately. See the
[yt-dlp extractor arguments](https://github.com/yt-dlp/yt-dlp#extractor-arguments).

One paired public-source check produced these observations:

| Source | Full resolution | Catalogue profile | Required fields |
| --- | ---: | ---: | --- |
| Upload `bnBpJkC1AME` | 2.703 s | 1.632 s | Equal |
| Completed replay `k0ILMhbWHNQ` | 1.955 s | 1.309 s | Equal |

The compared fields were video and channel IDs, title, publication timestamp,
upload date, duration, lifecycle, and availability. These are two samples, not a
latency guarantee. The complete discovery-to-playback comparison remains open.

## Matched cold comparison

The same two public 4K uploads were followed with Latest 2 and playlists skipped.
Both versions used the same documented yt-dlp media client. Test identifiers
kept this workload separate from household history. The before version was
`02b444b`; the after version was `74e9adb`.

| Observation from follow | Before | After |
| --- | ---: | ---: |
| First catalogue entry | 111.848 s | 4.797 s |
| First verified file and Plex discovery | 111.848 s | 25.944 s |
| Both verified files | 111.848 s | 58.961 s |

Plex discovery means its first library entry, before complete media analysis.
The poll interval was 500 ms. This does not measure first rendered frame.

Separate CPU, allocation, and blocking captures found a trade-off. Whole-container
CPU, including resolver children, fell from 18.032 to 12.990 CPU seconds. Go CPU
samples increased from 1.930 to 3.110 seconds. Container peak memory increased
from 194,002,944 to 357,711,872 bytes; sampled Go allocation space increased from
239.88 to 1,026.03 MiB. New artwork work contributes to the latter result.
Summed goroutine blocking was 1,190.07 versus 1,902.05 seconds; this includes idle
workers and is not elapsed latency. See the [scoped evidence](evidence/cleanup-performance.json).

These profiles precede the cache read optimization. Its separate 100-iteration,
three-run benchmark reduced a published chunk read from 28 to 4 allocations and
about 2.23 MB to 1.06 MB allocated. It does not establish a new whole-application
memory result.

The matched restart check returned an incomplete initial mounted header read on
both versions. A later direct mounted header read worked on the after version.
Restart, unchanged refresh, and matched first-frame comparison remain incomplete.
No release acceptance is claimed by the cold measurements.

## Current measurement fields

Authenticated status and diagnostics include bounded cumulative counters for
`media.http_requests` and `media.upstream_bytes`. These count media-client request
attempts and received response payload bytes. They exclude resolver subprocess
and artwork traffic. They restart with the process. Private profile captures
also retain a separate counters JSON file when media traffic occurred.

`preparation.queue` measures the interval from the durable enqueue timestamp to
claim. Its timestamp resolution is one second. `preparation.wait` measures a
request's wait for the shared preparation slots. Source-operation counts record
resolver calls separately. CPU, allocations, and blocking use separate profile
captures; no profiling listener is exposed.
