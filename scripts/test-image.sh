#!/bin/sh
# Run as root on a Linux Docker host with AppArmor. All state is disposable.
set -eu

test "$(uname -s)" = Linux
test "$(id -u)" = 0
image=${1:?Usage: test-image.sh IMAGE}
command -v apparmor_parser >/dev/null
apparmor_parser -r deployment/duplex-mount.apparmor
root=$(mktemp -d /var/lib/duplex-image-XXXXXX)
name=$(basename "$root")
helper=$name-helper
service=$name-service
observer=$name-observer
control=$name-control
cleanup() {
  result=$?
  trap - EXIT HUP INT TERM
  if test "$result" -ne 0; then
    docker logs "$service" 2>/dev/null || true
    docker logs "$helper" 2>/dev/null || true
  fi
  docker rm -f "$observer" "$service" "$helper" >/dev/null 2>&1 || true
  docker volume rm "$control" >/dev/null 2>&1 || true
  if mountpoint -q "$root/library/media"; then umount -l "$root/library/media" || result=1; fi
  if mountpoint -q "$root"; then umount "$root" || result=1; fi
  rm -rf -- "$root"
  exit "$result"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM
sh scripts/prepare-host.sh "$root"
sh scripts/check-host.sh "$root"
docker volume create "$control" >/dev/null

# Local source checks can overlay a cross-compiled binary, without building or
# publishing an image. CI tests the exact built image with this variable unset.
set --
if test -n "${DUPLEX_TEST_BINARY:-}"; then
  set -- --mount "type=bind,src=$DUPLEX_TEST_BINARY,dst=/usr/local/bin/duplex,readonly"
fi
# Keep the optional bind as separate arguments instead of expanding shell text.
docker run --rm --network none --read-only --user 10001:10001 --cap-drop ALL \
  --security-opt no-new-privileges:true --mount "type=bind,src=$root,dst=/state" \
  "$@" "$image" init --config /state/config.yaml --state-dir /state --listen 127.0.0.1:8765

docker run -d --name "$helper" --network none --read-only --user 0:0 \
  --cap-drop ALL --cap-add SYS_ADMIN --device /dev/fuse \
  --security-opt apparmor=duplex-mount-helper --security-opt no-new-privileges:true \
  --mount "type=volume,src=$control,dst=/run/duplex" \
  --mount "type=bind,src=$root/library,dst=/library,bind-propagation=rshared" \
  "$@" "$image" mount-helper >/dev/null
attempts=0
until docker exec "$helper" test -S /run/duplex/mount.sock; do
  attempts=$((attempts + 1))
  test "$attempts" -lt 30
  sleep 1
done
docker exec -i "$helper" python3 - helper <scripts/test-image-runtime.py

docker run -d --name "$observer" --network none --read-only --user 65534:65534 --cap-drop ALL \
  --security-opt no-new-privileges:true --entrypoint sleep \
  --mount "type=bind,src=$root/library,dst=/data,readonly,bind-propagation=rslave" \
  "$image" infinity >/dev/null
start_service() {
  docker run -d --name "$service" --network none --read-only --init --user 10001:10001 \
    --cap-drop ALL --security-opt no-new-privileges:true \
    --tmpfs /tmp:size=256m,mode=1777 \
    --mount "type=bind,src=$root,dst=/state,bind-propagation=rslave" \
    --mount "type=volume,src=$control,dst=/run/duplex,readonly" \
    "$@" "$image" serve --config /state/config.yaml --mount-socket /run/duplex/mount.sock >/dev/null
  attempts=0
  until docker exec "$service" duplex health; do
    attempts=$((attempts + 1))
    test "$attempts" -lt 30
    sleep 1
  done
  test "$(docker exec "$observer" stat -f -c %t /data/media)" = 65735546
}
start_service "$@"
docker exec -i "$service" python3 - service <scripts/test-image-runtime.py
# The consumer remains running across a service crash and a replacement mount.
docker kill "$service" >/dev/null
docker rm "$service" >/dev/null
start_service "$@"
# A helper crash must stop the service; the next pair must recover the mount.
docker kill "$helper" >/dev/null
attempts=0
while test "$(docker inspect -f '{{.State.Running}}' "$service")" = true; do
  attempts=$((attempts + 1))
  test "$attempts" -lt 30
  sleep 1
done
docker rm "$service" >/dev/null
docker start "$helper" >/dev/null
start_service "$@"
docker stop "$service" >/dev/null
printf '%s\n' 'Image security, FUSE handoff, consumer propagation, crash recovery, and tool checks passed.'
