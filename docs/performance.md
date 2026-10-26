# Performance measurement

Measure catalogue work and media playback separately. A faster SQLite query does
not establish faster Plex startup or a lower total service memory requirement.

## Catalogue workloads

The application benchmarks generate controlled catalogue fixtures. They cover
summary counts, selected and complete video pages, sparse selections, shared
playlists, filesystem directory pages, and publication.

Run the current workloads from the repository root:

```sh
go test ./internal/app -run '^$' \
  -bench 'BenchmarkCatalogueReads|BenchmarkCatalogueSummaryLatest50|BenchmarkCatalogueSelectedPages|BenchmarkCatalogueFilesystemPage|BenchmarkCataloguePlaylistFilesystemPage|BenchmarkPublish' \
  -benchmem -count=3 -benchtime=1s
```

For a repeated Linux comparison of two committed versions, use the
[profiling runner](performance-tooling.md). Compare equal fixtures, tool versions,
platforms, CPU limits, and initial state. Capture CPU and allocation profiles
separately from timing runs. Record failed experiments as well as improvements.

The implementation counts selected videos once in summaries and reads only the
episode fields and rows needed for a directory page. Directory queries seek
through lexical file-number indexes. Selected-video membership queries remain
an area to measure before changing them. Preserve lexical names, playlist
visibility, and ordering above episode 9,999 when comparing alternatives.

## Indexed directory reads, 2026-10-26

A matched Linux arm64 comparison used one synthetic channel with 1,000 or 10,000
recordings. Uploads and a playlist containing the same recordings were measured
separately. Each call returned 128 directory entries. Both versions used identical
fixtures, two CPUs, 1 GiB of memory, GOMAXPROCS=2, and ten one-second samples.
The table shows median time per provider call:

| Season | Recordings | Before | After | Time reduction |
| --- | ---: | ---: | ---: | ---: |
| Upload year | 1,000 | 1.390 ms | 0.255 ms | 81.63% |
| Upload year | 10,000 | 10.207 ms | 0.256 ms | 97.49% |
| Playlist | 1,000 | 1.909 ms | 0.409 ms | 78.60% |
| Playlist | 10,000 | 15.349 ms | 0.401 ms | 97.38% |

`benchstat` reports p < 0.001 for each time comparison. The previous query
formatted full filenames and sorted a season on every page. The replacement
seeks through partial indexes on the same zero-padded file-number strings.
Year and playlist queries keep their distinct visibility rules and immutable
scanner slots. Cursor and reorder tests cover the episode 9,999 boundary.
Query-plan tests reject a temporary sort on these pages.

The indexes are derived on catalogue open. No source identity, file name,
episode slot, or schema version changes. Existing schema-5 catalogues receive
the indexes without re-preparing media. Index construction and fixture setup
are outside the timed read loop.

The [structured results](directory-read-performance.json) retain source trees,
sample ranges, allocation results, and limits. These are provider measurements
inside Linux containers. They do not measure mounted FUSE latency, Plex scan
completion, or playback. Read callbacks, metadata generation, asset readiness,
and source binding still need separate work.

## Prepared metadata timestamps, 2026-10-26

Channel and season attributes previously aggregated every published episode.
Schema 6 maintains timestamp indexes as catalogue records change. Uploads use
one indexed maximum per year. Playlist occurrences retain the maximum of their
member and canonical video timestamps in a separate index. Channel and playlist
metadata join at read time, including current YAML visibility rules.

A matched Linux arm64 comparison used 10,000 recordings, two CPUs, 1 GiB of
memory, GOMAXPROCS=2, a 512 MiB tmpfs, and no network. The playlist fixture also
included all recordings as uploads. Ten one-second provider samples reduced
upload channel attributes from 4.145 ms to 0.080 ms and shared-playlist show-NFO
attributes from 32.120 ms to 0.297 ms. All six provider time comparisons have
`benchstat` p < 0.001.

Complete mounted paths were measured separately, including parent lookups.
Each workload had three samples of 500 `stat` calls. Values below are the median
of each sample's p95, not a pooled percentile or a confidence interval:

| Catalogue | Attribute | Before p95 | After p95 |
| --- | --- | ---: | ---: |
| Uploads | Channel | 9.221 ms | 0.330 ms |
| Uploads | Season | 14.961 ms | 0.472 ms |
| Uploads | MP4 | 10.472 ms | 0.906 ms |
| Uploads | Episode NFO | 10.414 ms | 0.995 ms |
| Uploads | Show NFO | 28.510 ms | 0.943 ms |
| Uploads + playlist | Channel | 27.421 ms | 0.279 ms |
| Uploads + playlist | Playlist season | 33.656 ms | 0.650 ms |
| Uploads + playlist | Playlist MP4 | 24.246 ms | 1.013 ms |
| Uploads + playlist | Playlist episode NFO | 24.324 ms | 1.084 ms |
| Uploads + playlist | Show NFO | 80.235 ms | 0.985 ms |

The work moves cost to writes. A canonical timestamp update increased from
12.39 to 32.15 microseconds with uploads and from 12.33 to 44.63 microseconds
with shared playlist membership. These ten-sample comparisons also have
`benchstat` p < 0.001. The derived playlist rows and indexes add persistent storage
per selected verified membership. Total catalogue size, resident memory,
migration time, and concurrent writer latency were not measured.

The [structured measurements](metadata-read-performance.json) retain exact source
trees, fixture hashes, sample ranges, allocations, and mounted p50/p95/p99.
The local reference contains the same episode filenames in the measured season
with empty files. The [metadata workloads](performance-tooling.md#filesystem-metadata-workloads)
describe repeatable commands. These are warm, single-reader metadata results.
They do not establish Plex scan time, payload throughput, cold-cache latency,
or concurrent service capacity.

Episode NFO generation and asset manifest reads remain in filesystem callbacks.
An uncached media open can still resolve and verify remote sources. Preparing
local scan artifacts, measuring useful scan ranges for bounded prefetch, and
revision-safe kernel caching remain separate implementation work.

## Media path

Use the [playback profiler](playback-performance.md) to measure reads through Plex,
FUSE, the cache, and the upstream source. Record preparation separately from
first open, seeks, warm reads, and restart with an empty media cache. Include
transferred bytes, request counts, cache allocation, source failures, and decode
results. A cache hit and a first source binding are different workloads.

Short decoder probes do not establish browser rendering, conversion startup,
sustained playback, or four-viewer capacity. Follow the [validation requirements](validation.md)
for those checks. This source snapshot publishes no latency or capacity guarantee.

## Current measurement fields

Authenticated status and diagnostics include cumulative `media.http_requests`
and `media.upstream_bytes` counters. They count media-client request attempts
and received response payload bytes. They exclude resolver subprocess and artwork
traffic and reset with the process.

`preparation.queue` measures time from durable enqueue to claim. Its enqueue
timestamp has one-second resolution. `preparation.wait` measures the wait for
shared preparation slots. Operation measurements use nanoseconds. Concurrent
operation totals can overlap and must not be added as sequential wall time.

## Reporting

Identify the exact commit, source fingerprint, workload, platform, tool versions,
cache state, and resource limits. Use repeated samples for comparisons and retain
sample ranges. Report total resources as well as latency. Keep raw profiles,
signed source responses, local installation details, and credentials outside Git.
Review and sanitize any results before publication.
