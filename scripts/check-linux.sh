#!/bin/sh
# This gate must run on Linux with a usable FUSE device and mount capability.
set -eu
test "$(uname -s)" = Linux || { echo 'Linux qualification requires Linux.' >&2; exit 1; }
test -c /dev/fuse || { echo 'Linux qualification requires /dev/fuse.' >&2; exit 1; }
export GOTOOLCHAIN=go1.27.3
mkdir -p .cache
root=$(mktemp -d)
cleanup() {
  sudo umount "$root/full" "$root/pressure"
  rmdir "$root/full" "$root/pressure" "$root"
}
mkdir "$root/full" "$root/pressure"
sudo mount -t tmpfs -o size=2m tmpfs "$root/full"
sudo mount -t tmpfs -o size=2m tmpfs "$root/pressure"
trap cleanup EXIT HUP INT TERM
go test -c -covermode=atomic -coverpkg=./internal/library -o .cache/library-linux.test ./internal/library
go test -c -covermode=atomic -coverpkg=./internal/cache -o .cache/cache-linux.test ./internal/cache
sudo -E env DUPLEX_FUSE_TEST=1 .cache/library-linux.test -test.v -test.coverprofile=.cache/library-linux.cover
sudo -E env DUPLEX_DISK_FULL_DIR="$root/full" DUPLEX_PRESSURE_DIR="$root/pressure" DUPLEX_CACHE_CRASH_TEST=1 .cache/cache-linux.test -test.v -test.coverprofile=.cache/cache-linux.cover
