#!/bin/sh
# Compare committed catalogue implementations without copying working-tree edits.
set -eu
umask 077

if test "$#" -ne 3; then
  printf '%s\n' 'Usage: scripts/profile-catalogue.sh BEFORE_REF AFTER_REF ABSOLUTE_OUTPUT_DIRECTORY' >&2
  exit 1
fi

root=$(git rev-parse --show-toplevel)
before=$(git rev-parse --verify --end-of-options "$1^{commit}")
after=$(git rev-parse --verify --end-of-options "$2^{commit}")
output=$3
case "$output" in
  /*) ;;
  *) printf '%s\n' 'The output directory must be absolute.' >&2; exit 1 ;;
esac
if test -e "$output"; then
  printf '%s\n' 'The output directory must not exist; previous evidence is retained.' >&2
  exit 1
fi

GO=${GO:-go}
export GOTOOLCHAIN=go1.27.3
GOCACHE=${GOCACHE:-"$root/.cache/go-build"}
export GOCACHE
context=${DUPLEX_DOCKER_CONTEXT:-colima}
architecture=$(docker --context "$context" info --format '{{.Architecture}}')
case "$architecture" in
  aarch64|arm64) architecture=arm64 ;;
  x86_64|amd64) architecture=amd64 ;;
  *) printf '%s\n' 'Unsupported Linux daemon architecture.' >&2; exit 1 ;;
esac

benchstat_version=v0.0.0-20250813145418-2f7363a06fe1
benchstat="$root/.cache/tools/benchstat"
mkdir -p "$root/.cache/tools"
if ! test -x "$benchstat" || ! "$GO" version -m "$benchstat" | awk -v version="$benchstat_version" '$1=="mod" && $2=="golang.org/x/perf" && $3==version { found=1 } END { exit !found }'; then
  GOBIN="$root/.cache/tools" "$GO" install "golang.org/x/perf/cmd/benchstat@$benchstat_version"
fi

mkdir -p "$output/profiles"
image=debian:bookworm-slim@sha256:7c7b2c966bc9ee8cedfeef67e0e279108992c77681fa595db4a9d65c06ccc587
printf 'before=%s\nafter=%s\ngo=1.27.3\narchitecture=%s\nimage=%s\ncpus=2\nmemory=1g\ngomaxprocs=2\nsamples=10\nbenchtime=1s\nstorage=anonymous Docker volume on Linux daemon\nbenchstat=%s\n' \
  "$before" "$after" "$architecture" "$image" "$benchstat_version" > "$output/environment.txt"

for label in before after; do
  case "$label" in before) revision=$before ;; after) revision=$after ;; esac
  mkdir "$output/$label-source"
  git -C "$root" archive --output="$output/$label.tar" "$revision"
  tar -xf "$output/$label.tar" -C "$output/$label-source"
  rm "$output/$label.tar"
  # Both references must contain the same directory workload. Reject changes
  # to its fixture rather than comparing different inputs.
  if test "$label" = after; then
    for fixture in internal/app/filesystem_benchmark_test.go internal/app/discovery_test.go; do
      cmp "$output/before-source/$fixture" "$output/after-source/$fixture"
    done
  fi
  (cd "$output/$label-source" && CGO_ENABLED=0 GOOS=linux GOARCH="$architecture" "$GO" test -mod=readonly -c -o "$output/$label-linux.test" ./internal/app)
done

run_linux() {
  label=$1
  shift
  # --rm also removes the anonymous data volume. Profiles are written to the
  # separate private output directory and survive container removal.
  docker --context "$context" run --rm --read-only --network none \
    --cap-drop ALL --security-opt no-new-privileges:true \
    --cpus 2 --memory 1g --pids-limit 256 --env GOMAXPROCS=2 \
    --mount type=volume,dst=/tmp \
    --mount "type=bind,src=$output/$label-linux.test,dst=/profile.test,readonly" \
    --mount "type=bind,src=$output/profiles,dst=/profiles" \
    "$image" /profile.test -test.run '^$' "$@"
}

for label in before after; do
  printf 'Measuring %s\n' "$label"
  run_linux "$label" -test.bench BenchmarkCatalogueFilesystemPage -test.benchmem -test.count 10 -test.benchtime 1s > "$output/$label.txt"
done
(cd "$output" && "$benchstat" before.txt after.txt) > "$output/comparison.txt"
(cd "$output" && "$benchstat" -format csv before.txt after.txt) > "$output/comparison.csv"

for label in before after; do
  printf 'Profiling %s\n' "$label"
  run_linux "$label" -test.bench 'BenchmarkCatalogueFilesystemPage/10000$' -test.benchtime 5s -test.cpuprofile "/profiles/$label.cpu.pprof" > "$output/$label-cpu.txt"
  run_linux "$label" -test.bench 'BenchmarkCatalogueFilesystemPage/10000$' -test.benchtime 100x -test.memprofilerate 65536 -test.memprofile "/profiles/$label.allocs.pprof" > "$output/$label-allocs.txt"
  "$GO" tool pprof -top -nodecount 20 "$output/$label-linux.test" "$output/profiles/$label.cpu.pprof" > "$output/$label-cpu-top.txt"
  "$GO" tool pprof -top -alloc_space -nodecount 20 "$output/$label-linux.test" "$output/profiles/$label.allocs.pprof" > "$output/$label-allocs-top.txt"
done

cat "$output/comparison.txt"
