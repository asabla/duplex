#!/bin/sh
set -eu
test "$(uname -s)" = Linux || {
  echo 'Run inside the Linux Docker daemon host.' >&2
  exit 1
}
test "$(id -u)" = 0 || {
  echo 'Root is required for mount propagation.' >&2
  exit 1
}
test -c /dev/fuse || {
  echo '/dev/fuse is missing.' >&2
  exit 1
}
root=${1:-/var/lib/duplex}
case "$root" in /var/lib/duplex | /var/lib/duplex-*) ;; *)
  echo 'Use a dedicated /var/lib/duplex directory.' >&2
  exit 1
  ;;
esac
name=${root#/var/lib/}
case "$name" in *[!A-Za-z0-9_-]*)
  echo 'Invalid directory name.' >&2
  exit 1
  ;;
esac
test ! -L "$root" || {
  echo 'Refusing a symbolic link.' >&2
  exit 1
}
if test -d "$root" && ! test -f "$root/.duplex-owner"; then
  test -z "$(ls -A "$root")" || {
    echo 'Refusing a nonempty unmarked directory.' >&2
    exit 1
  }
fi
mkdir -p "$root/library/media"
chmod 0755 "$root/library"
touch "$root/.duplex-owner"
chmod 0700 "$root"
if ! mountpoint -q "$root"; then mount --bind "$root" "$root"; fi
mount --make-rshared "$root"
findmnt "$root"
