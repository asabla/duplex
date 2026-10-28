#!/bin/sh
# Run on the Linux Docker host. Only Docker performs the NFS client mount.
set -eu
test "$(uname -s)" = Linux
image=${1:?Usage: test-image-nfs.sh IMAGE}
root=$(mktemp -d /var/lib/duplex-nfs-image-XXXXXX)
name=$(basename "$root")
service=$name-service
observer=$name-observer
volume=$name-library
cleanup() {
  result=$?
  trap - EXIT HUP INT TERM
  if test "$result" -ne 0; then docker logs "$service" 2>/dev/null || true; fi
  docker rm -f "$observer" "$service" >/dev/null 2>&1 || true
  docker volume rm "$volume" >/dev/null 2>&1 || true
  docker network rm "$name" >/dev/null 2>&1 || true
  rm -rf -- "$root"
  exit "$result"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM
chown 10001:10001 "$root"
chmod 0700 "$root"
docker network create "$name" >/dev/null
port=$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()')
set --
if test -n "${DUPLEX_TEST_BINARY:-}"; then
  set -- --mount "type=bind,src=$DUPLEX_TEST_BINARY,dst=/usr/local/bin/duplex,readonly"
fi
docker run --rm --network none --read-only --user 10001:10001 --cap-drop ALL \
  --security-opt no-new-privileges:true --mount "type=bind,src=$root,dst=/state" \
  "$@" "$image" init --config /state/config.yaml --state-dir /state --listen 127.0.0.1:8765
docker run -d --name "$service" --network "$name" --read-only --init --user 10001:10001 \
  --cap-drop ALL --security-opt no-new-privileges:true --tmpfs /tmp:size=256m,mode=1777 \
  --publish "127.0.0.1:$port:2049" --mount "type=bind,src=$root,dst=/state" \
  "$@" "$image" serve --config /state/config.yaml --nfs-listen 0.0.0.0:2049 >/dev/null
wait_service() {
  attempts=0
  until docker exec "$service" duplex health; do
    attempts=$((attempts + 1))
    test "$attempts" -lt 30
    sleep 1
  done
}
wait_service
docker exec -i "$service" python3 - service <scripts/test-image-runtime.py
docker exec -i "$service" python3 - nfs <scripts/test-image-runtime.py
docker volume create --driver local --opt type=nfs --opt device=:/media \
  --opt "o=addr=127.0.0.1,port=$port,mountport=$port,nfsvers=3,tcp,mountproto=tcp,nolock,noacl,ro,hard,timeo=10,retrans=2" "$volume" >/dev/null
docker run -d --name "$observer" --network none --read-only --user 65534:65534 --cap-drop ALL \
  --security-opt no-new-privileges:true --entrypoint sleep --tmpfs /tmp:size=8m,mode=1777 \
  --mount "type=volume,src=$volume,dst=/data/media,readonly,volume-nocopy" "$image" infinity >/dev/null
docker exec -i "$observer" python3 - consumer <scripts/test-image-runtime.py
# The same running consumer must recover after both graceful and abrupt restarts.
for action in restart kill; do
  if test "$action" = restart; then
    docker restart "$service" >/dev/null
  else
    docker kill "$service" >/dev/null
    docker start "$service" >/dev/null
  fi
  wait_service
  docker exec -i "$observer" python3 - consumer <scripts/test-image-runtime.py
done
printf '%s\n' 'NFS image: zero capabilities, default security profiles, Docker loopback mount, read-only export, and restart recovery passed.'
