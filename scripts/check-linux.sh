#!/bin/sh
# Compile the current source and qualify Linux FUSE, media, disk and crash paths.
set -eu
GO=${GO:-go}
export GOTOOLCHAIN=go1.27.3
mkdir -p .cache/linux-fixtures
source_id=$(./scripts/coverage-source.sh)
case "$(uname -s)" in
Linux) architecture=$("$GO" env GOARCH) ;;
Darwin)
  context=${DUPLEX_DOCKER_CONTEXT:-colima}
  architecture=$(docker --context "$context" info --format '{{.Architecture}}')
  ;;
*)
  echo 'Linux qualification requires Linux or a Linux Docker daemon.' >&2
  exit 1
  ;;
esac
case "$architecture" in
aarch64 | arm64) architecture=arm64 ;;
x86_64 | amd64) architecture=amd64 ;;
*)
  echo 'Unsupported qualification architecture.' >&2
  exit 1
  ;;
esac
for package in library cache app; do
  rm -f ".cache/$package-linux.cover"
  CGO_ENABLED=0 GOOS=linux GOARCH="$architecture" "$GO" test -c -covermode=atomic -coverpkg=./... -o ".cache/$package-linux.test" "./internal/$package"
done
ffmpeg -y -hide_banner -loglevel error -f lavfi -i testsrc2=size=1920x1080:rate=25 -t 2 -an -c:v libx264 -preset ultrafast -pix_fmt yuv420p -movflags +faststart .cache/linux-fixtures/video.mp4
ffmpeg -y -hide_banner -loglevel error -f lavfi -i sine=frequency=440:sample_rate=48000 -t 2 -vn -c:a aac -movflags +faststart .cache/linux-fixtures/audio.m4a
if test "$(uname -s)" = Linux; then
  if test "$(id -u)" = 0; then
    ./scripts/run-linux-tests.sh "$PWD/.cache"
  else
    sudo ./scripts/run-linux-tests.sh "$PWD/.cache"
  fi
else
  docker --context "$context" run --rm --read-only --cap-add SYS_ADMIN --device /dev/fuse --pids-limit 256 --security-opt apparmor=unconfined --security-opt no-new-privileges:true \
    --tmpfs /tmp:rw,nosuid,nodev,size=512m \
    --tmpfs /qualification/full:rw,nosuid,nodev,size=2m \
    --tmpfs /qualification/pressure:rw,nosuid,nodev,size=2m \
    --env DUPLEX_CONTAINER_TESTS=1 \
    --mount "type=bind,src=$PWD/.cache,dst=/evidence" \
    --mount "type=bind,src=$PWD/scripts/run-linux-tests.sh,dst=/run-linux-tests.sh,readonly" \
    debian:bookworm-slim@sha256:7c7b2c966bc9ee8cedfeef67e0e279108992c77681fa595db4a9d65c06ccc587 \
    /bin/sh /run-linux-tests.sh /evidence
fi
test "$source_id" = "$(./scripts/coverage-source.sh)" || {
  echo 'Source changed during Linux qualification.' >&2
  exit 1
}
printf '%s\n' "$source_id" >.cache/linux-coverage-source
