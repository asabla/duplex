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

## Remaining comparison

The incremental pipeline, SQL read pages, separate preparation workers, Plex
publication, and complete playback workloads still require matched before/after
measurements. No release acceptance is claimed by the results above.
