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

Each directory benchmark has ten one-second samples. Before and after runs
are sequential. Do not run quality gates or other profiling workloads at the
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
output location, `.cache` is already ignored. Retain sanitized measurements
and source identities in `docs/evidence` when recording a result.

These are catalogue query measurements inside Linux containers. They do not
measure native-host Linux storage, HTTP response time, Plex scanning, live
source requests, concurrent playback, or first rendered frame.
