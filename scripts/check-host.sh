#!/bin/sh
# Read-only setup checks. Run on the Linux daemon host.
set -eu
test "$(uname -s)" = Linux || {
  echo 'Run inside Linux or Colima.' >&2
  exit 1
}
root=${1:-/var/lib/duplex}
test -c /dev/fuse || {
  echo '/dev/fuse is missing.' >&2
  exit 1
}
test -d "$root/library" || {
  echo 'Public mount parent is missing.' >&2
  exit 1
}
propagation=$(findmnt -n -o PROPAGATION -T "$root")
case "$propagation" in *shared*) ;; *)
  echo 'State mount must have shared propagation.' >&2
  exit 1
  ;;
esac
printf 'Mount propagation: %s\n' "$propagation"
findmnt -T "$root"
df -h "$root"
if test -d /var/lib/docker; then df -h /var/lib/docker; fi
printf '%s\n' 'Check that free space covers the configured cache plus durable data, Plex state, transcodes, and build images.'
printf '%s\n' 'Bind the stable library parent into Plex read-only; select its media child as the library location.'
