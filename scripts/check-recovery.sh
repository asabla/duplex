#!/bin/sh
# Record the shuffle seed and check recovery tests for shared-state dependence.
set -eu

GO=${GO:-go}
DUPLEX_MEDIA_TEST=1 "$GO" test -race -shuffle=on -count=1 -timeout=90s -v \
  -run '^Test(Rejected|RateLimit|Retry|ClosedStore|OpenVersion|PreparationCandidate|CandidateOrdering)' \
  ./internal/app ./internal/cache ./internal/media ./internal/source
