#!/bin/sh
# Test the unprivileged deployment and retain legacy native FUSE coverage.
set -eu
image=${1:?Usage: test-image.sh IMAGE}
sh scripts/test-image-nfs.sh "$image"
sh scripts/test-image-fuse.sh "$image"
