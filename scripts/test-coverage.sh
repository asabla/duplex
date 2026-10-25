#!/bin/sh
# Exercise the coverage gate with controlled duplicate and malformed reports.
set -eu
fixture=$(mktemp -d)
trap 'rm -rf "$fixture"' EXIT HUP INT TERM
cat > "$fixture/first" <<'EOF'
mode: atomic
duplex/internal/fixture/a.go:1.1,2.2 4 1
duplex/internal/fixture/a.go:3.1,4.2 1 0
EOF
cat > "$fixture/second" <<'EOF'
mode: atomic
duplex/internal/fixture/a.go:1.1,2.2 4 0
duplex/internal/fixture/a.go:3.1,4.2 1 0
EOF
awk -v profile="$fixture/merged" -f scripts/coverage.awk "$fixture/first" "$fixture/second" > "$fixture/result"
test "$(cat "$fixture/result")" = 'duplex/internal/fixture: 80.0% statements covered'
test "$(wc -l < "$fixture/merged" | tr -d ' ')" = 3
printf '%s\n' 'duplex/internal/fixture/a.go:1.1,2.2 5 1' >> "$fixture/second"
if awk -f scripts/coverage.awk "$fixture/first" "$fixture/second" > /dev/null 2>&1; then
  echo 'Coverage accepted inconsistent blocks.' >&2; exit 1
fi
printf '%s\n' 'mode: atomic' 'duplex/internal/low/a.go:1.1,2.2 100 0' > "$fixture/low"
if awk -f scripts/coverage.awk "$fixture/first" "$fixture/low" > /dev/null 2>&1; then
  echo 'Coverage hid a package below 80 percent.' >&2; exit 1
fi
for invalid in 'mode: set' 'mode: atomic' 'unrecognised content'; do
  printf '%s\n' "$invalid" > "$fixture/invalid"
  if awk -f scripts/coverage.awk "$fixture/invalid" > /dev/null 2>&1; then
    echo 'Coverage accepted an invalid or empty report.' >&2; exit 1
  fi
done
printf '%s\n' 'Coverage gate self-check passed.'
