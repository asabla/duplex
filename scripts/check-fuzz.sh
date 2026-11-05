#!/bin/sh
# Bound active mutation testing; ordinary go test also runs retained seeds.
set -eu

GO=${GO:-go}
"$GO" test ./internal/media -run '^$' -fuzz '^FuzzLayout$' -fuzztime=10s -parallel=2 -timeout=45s
"$GO" test ./internal/nfs -run '^$' -fuzz '^FuzzRequest$' -fuzztime=10s -parallel=2 -timeout=45s
