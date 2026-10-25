#!/bin/sh
# Run only inside the dedicated Linux qualification environment as root.
set -eu
test "$(uname -s)" = Linux
test "$(id -u)" = 0
test -c /dev/fuse
output=$1
test -d "$output"
owned_root=1
if test "${DUPLEX_CONTAINER_TESTS:-0}" = 1; then
  qualification_root=/qualification
  owned_root=0
else
  qualification_root=$(mktemp -d /tmp/duplex-linux.XXXXXX)
fi
full_mounted=0
pressure_mounted=0
cleanup() {
  result=$?
  trap - EXIT HUP INT TERM
  if test "$pressure_mounted" = 1; then umount "$qualification_root/pressure" || result=1; fi
  if test "$full_mounted" = 1; then umount "$qualification_root/full" || result=1; fi
  if test "$owned_root" = 1; then rmdir "$qualification_root/full" "$qualification_root/pressure" "$qualification_root" || result=1; fi
  exit "$result"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM
if test "$owned_root" = 1; then
  mkdir "$qualification_root/full" "$qualification_root/pressure"
  mount -t tmpfs -o size=2m tmpfs "$qualification_root/full"
  full_mounted=1
  mount -t tmpfs -o size=2m tmpfs "$qualification_root/pressure"
  pressure_mounted=1
else
  test "$(stat -f -c %T "$qualification_root/full")" = tmpfs
  test "$(stat -f -c %T "$qualification_root/pressure")" = tmpfs
fi
DUPLEX_FUSE_TEST=1 "$output/library-linux.test" -test.v -test.timeout=180s -test.coverprofile="$output/library-linux.cover"
DUPLEX_DISK_FULL_DIR="$qualification_root/full" DUPLEX_PRESSURE_DIR="$qualification_root/pressure" DUPLEX_CACHE_CRASH_TEST=1 "$output/cache-linux.test" -test.v -test.timeout=180s -test.coverprofile="$output/cache-linux.cover"
DUPLEX_FUSE_TEST=1 DUPLEX_APP_MEDIA_FIXTURES="$output/linux-fixtures" "$output/app-linux.test" -test.v -test.timeout=180s -test.coverprofile="$output/app-linux.cover"
