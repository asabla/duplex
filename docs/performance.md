# Performance evidence

For playback start, distant seeks, and source binding through Plex and YouTube,
see the [media-path measurements](playback-performance.md). The catalogue
benchmarks below measure separate query and allocation work.

Each measurement states its platform and workload. The historical cleanup
measurements use an isolated Colima container. They do not measure a native
Linux installation. Raw profiles and source responses remain outside Git
because they can contain local data or signed source URLs.

## Local catalogue read profile, 2026-10-25

This separate measurement uses native macOS, an Apple M3 Max, Go 1.27.3,
and GOMAXPROCS=16. Each fixture has one channel and 1,000 or 10,000 synthetic
ready recordings in one year season. It has no playlists or optional assets.
The Latest 50 case selects 50 rows from the same durable catalogue.

The baseline supplies `internal/catalog/query.go` from `0bc73ba` through a Go
build overlay. Both versions use the new benchmark fixtures. Before and after
benchmarks run in sequence, without profiles, for three one-second samples.
The table shows median time per application call, including the other queries
in `App.Summary`. These are not HTTP or browser measurements.

| Operation | Recordings | Before | After | Reduction |
| --- | ---: | ---: | ---: | ---: |
| Summary, all selected | 1,000 | 2.238 ms | 1.067 ms | 52.3% |
| Summary, all selected | 10,000 | 23.463 ms | 10.608 ms | 54.8% |
| Summary, Latest 50 | 1,000 | 0.807 ms | 0.309 ms | 61.8% |
| Summary, Latest 50 | 10,000 | 6.550 ms | 2.065 ms | 68.5% |
| Selected video page, 25 rows | 10,000 | 24.612 ms | 24.713 ms | No material change |
| All video page, 25 rows | 10,000 | 7.840 ms | 7.934 ms | No material change |

The original summary plan built the selected-video set, then used three
correlated membership checks across the video table. The new query joins the
unique selected IDs to video records and counts them once. A separate scalar
count retains the complete discovered total in the same SQLite statement.
Shared playlist occurrences still count as one video. Tests cover empty
catalogues, empty visibility, overlapping uploads and playlists, skipped
playlists, failed preparation, and unavailable media.

Summary allocation at 10,000 selected recordings fell from a median 8,544 to
8,125 bytes per call. Allocation count stayed near 164. This is a query latency
improvement; it does not establish a service memory reduction.

An exploratory change from `EXISTS` to `IN` for the selected-video filter
increased its 10,000-row median from 25.24 to 29.24 ms. It was removed.

### Work identified in this profile

- A page of 128 filesystem entries took a median 3.123 ms at 1,000 recordings
  and 21.776 ms at 10,000. Both allocated about 275 KB. The query plan sorts
  formatted file names in a temporary B-tree. The filesystem also reads season
  aggregates. Test an indexed lexical file key and fewer aggregate reads while
  retaining exact names, playlist visibility, and order above episode 9,999.
- The selected video page remains about three times as slow as the all-video
  page. Test the count and page query plans separately, including sparse
  selections, shared playlists, and later pages, before changing membership
  queries or indexes.
- Allocation profiling attributes the largest filesystem allocation to loading
  episode rows, followed by child entries and path construction. A directory
  query that returns only identity and file-number fields is a candidate for a
  separate measured change.

CPU captures locate the application work in SQLite. macOS runtime wakeups and
SQLite system calls contribute substantially to these captures. Profile totals
are not a matched whole-process CPU comparison. Background writes, mixed
playlists, long descriptions, artwork, Linux FUSE, resolver subprocesses,
concurrent playback, and first rendered frame remain outside this measurement.
The running containers were not changed.

Run the current read benchmarks with:

```sh
go test ./internal/app -run '^$' \
  -bench 'BenchmarkCatalogueReads|BenchmarkCatalogueSummaryLatest50' \
  -benchmem -count=3 -benchtime=1s
go test ./internal/app -run '^$' \
  -bench 'BenchmarkCatalogueFilesystemPage|BenchmarkPublish' \
  -benchmem -count=3 -benchtime=1s
```

The [structured results](evidence/catalogue-profile.json) retain sample ranges,
allocation counts, fixture scope, and the rejected experiment. Raw local
profiles and logs are in `.cache/profiling-20261025/` and are not committed.

## Directory read follow-up, 2026-10-25

The baseline is `bb897d5`, which includes the summary improvement above.
The comparison uses the same machine, Go version, GOMAXPROCS, and three
one-second samples per workload. Isolated source copies exclude concurrent
UX edits. Both copies use the same benchmark source. Before, candidate, and
final benchmarks run in sequence without profiles.

| Recordings | Directory page before | Directory page after | Time reduction | Bytes before | Bytes after |
| ---: | ---: | ---: | ---: | ---: | ---: |
| 1,000 | 2.951 ms | 1.804 ms | 38.9% | 274,789 | 185,711 |
| 10,000 | 20.769 ms | 11.214 ms | 46.0% | 274,747 | 185,670 |

Each page contains 128 directory entries. Allocation fell by 32.4%, from 4,523
to 2,730 allocations per page. This measures directory enumeration in a single
synthetic year season on macOS, not Plex scanning or playback.

Directory listing now loads only the video ID, playlist ID, and stable file
number. It checks that the season is visible with `EXISTS`, which can stop at
the first match. The first page uses its returned playlist identity to include
`.plexmatch`, instead of reading the season again. File information requests
still use the timestamp aggregate. Filename comparison and visibility rules
remain the same.

Tests cover sidecars at the episode 9,999 boundary, one-entry pages, playlist
hints after reorder, skipped seasons with an end cursor, invalid paths, and
cancellation. Directory latency with many playlists, optional assets, or long
descriptions still needs measurement. The formatted filename sort remains a
candidate for an indexed lexical key.

### Rejected video-page changes

A direct join to selected video IDs reduced the uploads-only page time from
about 24.7 to 17.1 ms in the first experiment. A new shared-playlist benchmark
found a regression: the last page median increased from 47.64 to 53.42 ms.
That fixture has 10,000 selected uploads also selected through two playlists,
with distinct publication times. The join change was removed. The benchmark
also retains first and last pages for a sparse Latest 50 selection.

Forcing the selected CTE to be materialized also stayed out of production.
One exploratory sample increased the join candidate's uploads-only video page
from about 17.1 to 21.0 ms. This was not a repeated comparison.

Run all retained catalogue workloads with:

```sh
go test ./internal/app -run '^$' \
  -bench 'BenchmarkCatalogueReads|BenchmarkCatalogueSummaryLatest50|BenchmarkCatalogueSelectedPages|BenchmarkCatalogueFilesystemPage' \
  -benchmem -count=3 -benchtime=1s
```

The [follow-up results](evidence/catalogue-profile-followup.json) include sample
ranges, source hashes, and both rejected candidates. Private source copies and
logs are in `.cache/profiling-followup/`. No running container was changed.

## Repeated Linux directory measurements

The [profiling runner](performance-tooling.md) compares committed sources with
ten samples per workload and separate CPU and allocation captures. The final
comparison used `bb897d5` and `dd4e2e3` on Linux arm64 in Colima, with two CPUs,
1 GiB of memory, GOMAXPROCS=2, and an anonymous Linux Docker data volume.

| Recordings | Before | After | Time reduction | Allocation reduction |
| ---: | ---: | ---: | ---: | ---: |
| 1,000 | 2.764 ms | 1.429 ms | 48.31% | 67.30% |
| 10,000 | 20.36 ms | 10.45 ms | 48.68% | 67.30% |

The final allocation is about 83,632 bytes and 1,394 allocations per 128-entry
page. `benchstat` reports p<0.001 with ten samples for each time and allocation
comparison. These are query measurements inside a Linux VM.

The CPU and allocation profiles identified work left after `f424d51`: a page
still queried up to 129 episode rows, although every episode supplies an MP4
and NFO. `dd4e2e3` reduces the query to the rows needed for the page, allows one
extra row for a cursor inside an episode, and stops reading assets once enough
entries are available. The filename-order tests cover page sizes 1, 2, 3, 25,
128, and 256, with sidecars and the episode 9,999 boundary.

The complete `make check` gate passed on isolated sources that match all
committed Go and module files in `dd4e2e3`. This includes secret scans,
vulnerability checks, race and controlled media tests, Linux qualification,
coverage thresholds, lint, and both Linux architecture builds. The missing
pinned vulnerability checker was installed in the private tool directory.

The [Linux result record](evidence/catalogue-linux-profile.json) retains the
statistical report, source identities, limits, and validation. Raw profiles are
private in `.cache/catalogue-linux-final-20261025/`. SQLite filename formatting
and sorting remain visible in CPU profiles. Playback start and seek latency
through Plex, Duplex, and YouTube require separate end-to-end measurements.

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
