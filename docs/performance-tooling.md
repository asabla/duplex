# Repeatable catalogue profiling

Run the directory workload against two committed versions. The runner extracts
both versions into a new private directory. Working-tree edits are excluded.
It checks that the directory benchmark and its fixture helpers match.

Requirements are Go 1.27.3, Git, tar, Docker, and a running Linux Docker daemon.
The default Docker context is `colima`. Set `DUPLEX_DOCKER_CONTEXT` to use
another context; the runner does not change the global context.

```sh
./scripts/profile-catalogue.sh BEFORE_REF AFTER_REF \
  "$PWD/.cache/catalogue-comparison-new"
```

Replace the two references with commit IDs. The output path must be absolute
and must not exist. Failed runs retain their evidence. Use a new output path
for the next run.

The runner installs the official Go `benchstat` tool in `.cache/tools` when
the pinned version is missing. Its version is
`golang.org/x/perf@v0.0.0-20250813145418-2f7363a06fe1`. This does not change
application dependencies or install a global binary.

## Measurements

The runner builds Linux test binaries for the Docker daemon's architecture.
Each version runs in a disposable container with two CPUs, 1 GiB of memory,
GOMAXPROCS=2, no network, and an anonymous volume for temporary SQLite data.
The image is pinned by digest in the script. Container removal also removes
the temporary data volume.

Upload-year and playlist directory benchmarks each have ten one-second samples.
The playlist catalogue contains both uploads and a playlist that references the
same canonical videos. Before and after runs are sequential. Do not run quality
gates or other profiling workloads at the
same time. `benchstat` reports medians, 95% confidence intervals, and a test
for differences at the default 0.05 significance threshold. A `~` result does
not establish a change.

CPU and allocation profiles are captured after the timing runs, in separate
processes. CPU capture uses a five-second workload at 10,000 recordings.
Allocation capture uses exactly 100 iterations and a 64 KiB sampling interval.
Profile totals include fixture setup; use them to locate work. Use the repeated
benchmarks to compare latency and allocation per operation.

## Output

- `environment.txt`: exact commits, tool pin, architecture, image, and limits.
- `comparison.txt` and `comparison.csv`: statistical results.
- `before.txt` and `after.txt`: all timing samples.
- `profiles/`: private CPU and allocation profiles.
- `*-cpu-top.txt` and `*-allocs-top.txt`: profile summaries.
- `*-source/` and `*-linux.test`: the source and binary used for each version.

Keep raw profiles and source snapshots outside Git. For the default private
output location, `.cache` is already ignored. Review and sanitize measurements
and source identities before including a result in project documentation.

These are catalogue query measurements inside Linux containers. They do not
measure native-host Linux storage, HTTP response time, Plex scanning, live
source requests, concurrent playback, or first rendered frame.

## Filesystem metadata workloads

Provider metadata benchmarks cover 1,000 and 10,000 recordings, with uploads
and shared playlist membership. They measure channel, season, MP4, episode-NFO,
and show-NFO attributes. The update benchmark measures a canonical timestamp
change in a 10,000-recording catalogue, including its projection maintenance:

```sh
go test ./internal/app -run '^$' \
  -bench '^BenchmarkCatalogueFilesystemMetadata$' \
  -benchmem -count=10 -benchtime=1s
go test ./internal/app -run '^$' \
  -bench '^BenchmarkCatalogueProjectionUpdate$' \
  -benchmem -count=10 -benchtime=1s
```

On Linux, use the FUSE privileges required by the quality gate to measure complete
mounted path lookups. This workload creates a disposable mount and a local
reference tree with the same episode filenames. The reference files are empty;
the workload measures metadata calls, not media or NFO reads:

```sh
DUPLEX_FUSE_TEST=1 go test ./internal/app -run '^$' \
  -bench '^BenchmarkMountedFilesystemMetadata$' \
  -benchmem -count=3 -benchtime=500x
```

Each result includes p50, p95, and p99 in nanoseconds. These use the nearest-rank
method on the measured calls and include timing overhead. Use the fixed 500-call
command above; the fixture retains at most 4,096 samples. Record the storage
type, CPU and memory limits, cache state, and concurrency for each comparison.
Fixture creation and migration are outside the timed read loops. Compare equal
fixture files at the two commits and measure catalogue write overhead separately.

## Initial readiness and writer load

Retain the baseline before changing production code. Use committed source exports
for each version and compare the benchmark and helper bytes before accepting a
result. Keep the same media files for both versions. The
[readiness results](readiness-performance.json) identify the measured commits,
fixture hashes, image, resource limits, all samples, and statistical comparisons.

Generate controlled media once with FFmpeg. Use a new private fixture directory:

```sh
mkdir -p .cache
readiness_fixtures=$(mktemp -d "$PWD/.cache/readiness-fixtures.XXXXXX")
ffmpeg -hide_banner -loglevel error -f lavfi \
  -i testsrc2=size=1920x1080:rate=25 -t 2 -an -c:v libx264 \
  -preset ultrafast -pix_fmt yuv420p -movflags +faststart \
  "$readiness_fixtures/video.mp4"
ffmpeg -hide_banner -loglevel error -f lavfi \
  -i sine=frequency=440:sample_rate=48000 -t 2 -vn -c:a aac \
  -movflags +faststart "$readiness_fixtures/audio.m4a"
export DUPLEX_APP_MEDIA_FIXTURES="$readiness_fixtures"
export GOMAXPROCS=2 GOTOOLCHAIN=go1.27.3
```

Record SHA-256 hashes of both media files before and after the paired runs.
Do not regenerate or replace them between versions.

Run these commands separately from each source export. Retain each output under
its exact commit and tree. The fixed initial-readiness and writer workloads need
`-benchtime=1x`; their fixtures reject additional iterations within a sample.

```sh
go test ./internal/app -run '^$' -bench '^BenchmarkInitialScanArtifacts$' \
  -benchtime=1x -count=10 -benchmem
go test ./internal/app -run '^$' -bench '^BenchmarkMediaOpenState$' \
  -benchtime=10x -count=10 -benchmem
go test ./internal/app -run '^$' -bench '^BenchmarkMetadataUnderWrites$' \
  -benchtime=1x -count=10 -benchmem
go test ./internal/app -run '^$' \
  -bench '^BenchmarkCatalogueFilesystemMetadata$/10000/' \
  -benchtime=200ms -count=10 -benchmem
go test ./internal/app -run '^$' -bench '^BenchmarkCatalogueProjectionUpdate$' \
  -benchtime=1s -count=10 -benchmem
```

The first two commands need the media fixtures. Source artwork and captions use
a controlled HTTP transport; media uses a loopback HTTP origin. No real source
resolver or Plex service participates. The initial-readiness fixture drains the
earlier discovery notification and measures the prepared-recording scan request.

For the measured Linux environment, cross-compile each source export:

```sh
mkdir -p .cache
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go test -c \
  -o "$PWD/.cache/readiness-linux.test" ./internal/app
```

Use a disposable container with `--read-only --network none --cpus 2 --memory 1g
--pids-limit 256 --security-opt no-new-privileges:true`, the pinned Debian image
in the structured results, `GOMAXPROCS=2`, and
`--tmpfs /tmp:rw,nosuid,nodev,size=512m`. Bind the
binary read-only as `/probe.test` and the same media directory read-only as
`/fixtures`; set `DUPLEX_APP_MEDIA_FIXTURES=/fixtures`. Use `--cap-drop ALL` for
provider, media, and control workloads. Invoke `/probe.test` with the same flags
above, adding the `-test.` prefix to each Go test flag.

Mounted writer-load runs instead use the quality gate's FUSE exception:
`--cap-add SYS_ADMIN --device /dev/fuse --security-opt apparmor=unconfined`,
without `--cap-drop ALL`. Set `DUPLEX_FUSE_TEST=1` and run:

```sh
/probe.test -test.run '^$' \
  -test.bench '^Benchmark(MetadataUnderWrites|MountedMetadataUnderWrites)$' \
  -test.benchtime 1x -test.count 10 -test.benchmem
```

This last command runs inside the container. Keep native APFS cold-open results
separate from Linux tmpfs results. Do not run quality gates or other profiles
during timing samples. Capture profiles in a later process if they are needed.

Each writer-load sample must finish all 800 offered reads and ten write
transactions. Compare both service and planned-arrival p95/p99. Its `ns/op` is
the complete workload window, not per-request latency. Preserve the range of
per-sample percentiles and report allocation and write costs. Do not pool
percentiles or remove late arrivals. If worker logs split benchmark names and
numeric result lines, join them before giving the output to `benchstat`; keep
the original logs privately. The structured results retain these normalized
sample values without local paths or source locators.
