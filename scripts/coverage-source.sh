#!/bin/sh
# Include application sources, embedded assets, test fixtures, and Go dependencies.
set -eu
rg --files cmd internal >.cache/coverage-files
printf '%s\n' go.mod go.sum >>.cache/coverage-files
LC_ALL=C sort .cache/coverage-files >.cache/coverage-files-sorted
while IFS= read -r file; do
  printf '%s ' "$file"
  git hash-object -- "$file"
done <.cache/coverage-files-sorted | git hash-object --stdin
