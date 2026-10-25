# Merge repeated blocks and enforce 80% statements for each production package.
# Set -v profile=FILE to retain the merged profile. Counts use the maximum;
# qualification needs evidence that a block ran, not a sum of separate runs.
function fail(message) {
  print message > "/dev/stderr"
  invalid = 1
  exit 1
}

function collect_totals(    block, package) {
  for (block in statements) {
    package = block
    sub("/[^/]+$", "", package)
    totals[package] += statements[block]
    if (counts[block] > 0) {
      covered[package] += statements[block]
    }
  }
}

function report_coverage(    package, packages, percentage, below) {
  for (package in totals) {
    packages++
    if (totals[package] == 0) {
      fail("Coverage package has no statements: " package)
    }

    percentage = 100 * covered[package] / totals[package]
    printf "%s: %.1f%% statements covered\n", package, percentage
    if (percentage < 80) {
      below = 1
    }
  }
  if (packages == 0) {
    fail("Coverage report contains no production packages")
  }
  return below
}

function write_profile(    block) {
  print "mode: atomic" > profile
  for (block in statements) {
    print block, statements[block], counts[block] + 0 > profile
  }
  close(profile)
}

$1 == "mode:" {
  if (NF != 2 || $2 != "atomic") {
    fail("Coverage must use atomic mode")
  }
  headers++
  next
}

{
  if (NF != 3 || $1 !~ /^duplex\/.+:[0-9]+\.[0-9]+,[0-9]+\.[0-9]+$/ || $2 !~ /^[0-9]+$/ || $3 !~ /^[0-9]+$/) {
    fail("Invalid coverage block")
  }
  if (($1 in statements) && statements[$1] != $2) {
    fail("Coverage block differs between runs: " $1)
  }

  statements[$1] = $2
  if ($3 > counts[$1]) {
    counts[$1] = $3
  }
}

END {
  if (invalid) {
    exit 1
  }
  if (headers == 0) {
    fail("Coverage report has no header")
  }

  collect_totals()
  below = report_coverage()
  if (profile != "") {
    write_profile()
  }
  exit below
}
