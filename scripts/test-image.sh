#!/bin/sh
# Run only on a Linux Docker host. No external media or installed state is used.
set -eu

image=${1:?Usage: test-image.sh IMAGE}
docker run --rm --network none --read-only --init \
  --tmpfs /tmp:size=256m,mode=1777 --tmpfs /state:size=128m,mode=0700 \
  --device /dev/fuse --cap-drop ALL --cap-add SYS_ADMIN \
  --security-opt apparmor=unconfined --security-opt no-new-privileges:true \
  --entrypoint /bin/sh "$image" -ec '
    test "$(yt-dlp --version)" = 2026.10.21
    deno --version | grep "^deno 2.9.8 "
    python3 -c '\''import sys; sys.path.insert(0, "/opt/yt-dlp/yt-dlp"); import yt_dlp_ejs.yt.solver as s; print(s.lib()); print(s.core())'\'' > /tmp/solver.js
    printf '\''\nconst r = jsc({type: "preprocessed", preprocessed_player: "_result.n = s => s.split(\"\").reverse().join(\"\")", requests: [{type: "n", challenges: ["abc"]}]}); if (r.responses[0].data.abc !== "cba") throw new Error("solver failed");\n'\'' >> /tmp/solver.js
    deno run --no-config --no-remote /tmp/solver.js
    for file in /LICENSE /THIRD_PARTY_NOTICES.txt /licenses/deno-LICENSE.txt /opt/yt-dlp/LICENSE /opt/yt-dlp/THIRD_PARTY_LICENSES.txt /runtime-packages.txt; do
      test -s "$file"
    done
    duplex init --config /state/config.yaml --state-dir /state --listen 127.0.0.1:8765
    test -s /state/data/secrets/initial-admin-password
    duplex serve --config /state/config.yaml &
    service=$!
    trap '\''kill "$service" 2>/dev/null || true; wait "$service" || true'\'' EXIT
    attempts=0
    until duplex health; do
      kill -0 "$service"
      attempts=$((attempts + 1))
      test "$attempts" -lt 30
      sleep 1
    done
    python3 -c '\''import urllib.request; r = urllib.request.urlopen("http://127.0.0.1:8765/login"); assert r.status == 200; assert b"password" in r.read()'\''
  '
