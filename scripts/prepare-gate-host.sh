#!/bin/sh
set -eu

test "$(uname -s)" = Linux || { echo 'Run this on the Linux Docker daemon host (inside Colima on macOS).' >&2; exit 1; }
test "$(id -u)" = 0 || { echo 'Root is required to prepare mount propagation.' >&2; exit 1; }
test -c /dev/fuse || { echo '/dev/fuse is missing.' >&2; exit 1; }
root=${1:-/var/lib/duplex-gate}
case "$root" in
  /var/lib/duplex-gate|/var/lib/duplex-gate-*) ;;
  *) echo 'Use a dedicated /var/lib/duplex-gate or /var/lib/duplex-gate-* directory.' >&2; exit 1 ;;
esac
suffix=${root#/var/lib/}
case "$suffix" in
  *[!A-Za-z0-9_-]*) echo 'The dedicated directory name contains invalid characters.' >&2; exit 1 ;;
esac
test ! -L "$root" || { echo 'The dedicated directory must not be a symbolic link.' >&2; exit 1; }
if test -d "$root" && ! test -f "$root/.duplex-gate-owner"; then
  test -z "$(ls -A "$root")" || { echo 'Refusing to use a nonempty unmarked directory.' >&2; exit 1; }
fi
mkdir -p "$root/library/media"
touch "$root/.duplex-gate-owner"
if ! mountpoint -q "$root"; then mount --bind "$root" "$root"; fi
mount --make-rshared "$root"
findmnt "$root"
