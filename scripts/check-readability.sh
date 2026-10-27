#!/bin/sh
# Check handwritten languages without rewriting source or installing tools.
set -eu

for tool in node python3 shfmt ruff; do
  command -v "$tool" >/dev/null 2>&1 || {
    printf 'Required development tool is missing: %s\n' "$tool" >&2
    exit 1
  }
done
check_version() {
  if test "$2" != "$3"; then
    printf '%s must be %s; found %s. Use .tool-versions.\n' "$1" "$2" "$3" >&2
    exit 1
  fi
}
check_version node v26.7.0 "$(node --version)"
check_version shfmt v3.14.1 "$(shfmt --version)"
check_version ruff 'ruff 0.17.2' "$(ruff --version)"

prettier=tools/readability/node_modules/.bin/prettier
if ! test -x "$prettier"; then
  printf '%s\n' 'Install development tools: npm ci --ignore-scripts --prefix tools/readability' >&2
  exit 1
fi
check_version prettier 3.9.10 "$("$prettier" --version)"
check_layout() {
  "$prettier" --config tools/readability/prettier.json \
    --plugin "$PWD/tools/readability/node_modules/prettier-plugin-go-template/lib/index.js" \
    --check "$@"
}
check_layout --parser go-template internal/admin/templates/*.html
check_layout internal/admin/static/app.css internal/admin/static/app.js tools/readability/*.mjs tools/readability/prettier.json tools/readability/package.json
shfmt -d -i 2 -ln posix scripts/*.sh
for file in scripts/*.sh; do
  sh -n "$file"
done
ruff format --check scripts
python3 scripts/test-notices.py
python3 scripts/test-check.py
python3 scripts/test-ci-prerequisites.py

# These fixtures use a controlled source adapter, without external services.
rm -rf .cache/markup
DUPLEX_MARKUP_TEST=1 "${GO:-go}" test -race -count=1 -run '^TestRenderedMarkupFixtures$' ./internal/admin
node tools/readability/validate-markup.mjs .cache/markup
