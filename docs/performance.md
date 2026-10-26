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
  -bench 'BenchmarkCatalogueReads|BenchmarkCatalogueSummaryLatest50|BenchmarkCatalogueSelectedPages|BenchmarkCatalogueFilesystemPage|BenchmarkPublish' \
  -benchmem -count=3 -benchtime=1s
```

For a repeated Linux comparison of two committed versions, use the
[profiling runner](performance-tooling.md). Compare equal fixtures, tool versions,
platforms, CPU limits, and initial state. Capture CPU and allocation profiles
separately from timing runs. Record failed experiments as well as improvements.

The implementation counts selected videos once in summaries and reads only the
episode fields and rows needed for a directory page. Formatted filename sorting
and selected-video membership queries remain areas to measure before changing
them. Preserve lexical names, playlist visibility, and ordering above episode
9,999 when comparing alternatives.

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
