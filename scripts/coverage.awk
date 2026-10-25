# Statement coverage is checked independently for every production package.
NR > 1 {
  split($1, location, ":")
  package = location[1]
  sub(/\/[^/]+$/, "", package)
  statements[package] += $2
  if ($3 > 0) covered[package] += $2
}
END {
  count = 0
  for (package in statements) {
    count++
    percentage = 100 * covered[package] / statements[package]
    printf "%s: %.1f%% statements covered\n", package, percentage
    if (percentage < 80) failed = 1
  }
  if (count == 0) {
    print "Coverage report contains no production packages" > "/dev/stderr"
    failed = 1
  }
  exit failed
}
