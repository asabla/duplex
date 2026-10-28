# Development

Read the root [repository instructions](../AGENTS.md) before changes. The
[code standards](code-standards.md) define readability rules, manual review, and
automated checks and their limits. [Architecture](architecture.md) defines
package responsibilities and runtime boundaries. This document owns tool setup,
commands, and verification procedures.

## Tools

Use the versions in [`.tool-versions`](../.tool-versions) and [go.mod](../go.mod):

- Go 1.27.3.
- golangci-lint 2.15.0, with formatting and imports enforced.
- gitleaks 8.30.1.
- govulncheck 1.9.0.
- Node.js 26.7.0, shfmt 3.14.1, and Ruff 0.17.2 for development checks.
- Prettier 3.9.10, prettier-plugin-go-template 0.0.15, and html-validate 11.17.0,
  pinned in the development-only `tools/readability/package-lock.json`.
- Python 3 for the existing scripts and their controlled behavioral tests.
- Git and ripgrep (`rg`) for source fingerprints and history checks. CI installs
  ripgrep with its system prerequisites; these tools must also be on the local PATH.
- FFmpeg and ffprobe for controlled media comparisons. Record the installed version in each result.

Install the pinned tools through the existing mise configuration or make those
exact versions available on PATH. Check their versions before running the gate.
The gate checks the Go toolchain and analysis tools; it needs FFmpeg and ffprobe
on PATH but does not pin their version.

Install the development-only Node packages from the repository root:

```sh
npm ci --ignore-scripts --prefix tools/readability
```

The quality gate does not install tools or dependencies. CI installs the same
locked packages before it calls `make check`. Node.js is not required to build or
run the application.

`make` prints the four supported targets. It performs no setup or downloads.

```sh
make
make build
make test
make check
```

`make check` checks formatting without rewriting files, runs strict lint, scans
working files and Git history for secrets, checks Go vulnerabilities, runs race
and controlled media tests, enforces package coverage, and builds Linux amd64
and arm64 binaries. It also checks the handwritten templates, CSS, JavaScript,
shell, and Python files without rewriting them, runs script behavioral tests, and
validates controlled rendered pages and HTMX fragments. The gate is defined in [`scripts/check.sh`](../scripts/check.sh).
[`CI`](../.github/workflows/ci.yml) calls `make check` and also validates production
Compose configuration. Use `golangci-lint fmt` only for an authorized formatting
change, then review the diff. It can rewrite Go files across the checkout; keep
concurrent work outside its scope.

CI runs for pull requests and pushes to `main`; a pull request update does not
also start a branch-push run. New updates cancel obsolete runs for the same pull
request or branch. Manual runs are available through GitHub Actions.
Go module, compiler, and lint caches use the runner platform and tool/dependency
versions. Tests and coverage evidence are regenerated; cached results do not
replace checks. Package index and archive downloads have a 120-second limit per
command. A failed download retries against the official Ubuntu archives with
archive signature checks retained. Package installation uses downloaded archives
without a timeout that could interrupt `dpkg`.

After the quality gate passes, CI builds the Docker image on native Linux amd64
and arm64 runners. Each image must pass `scripts/test-image.sh`: pinned tool
versions, bundled JavaScript solver, license files, initialization, FUSE startup,
mount health, and the sign-in page, with external networking disabled. The test
runs as root on an AppArmor-enabled Docker host, loads the helper profile, and
checks zero service capabilities, enforced AppArmor/seccomp, rejected helper
mounts and network sockets, read-only consumer propagation, and recovery after
service and helper crashes. It removes only its own disposable installation.
Pull requests run these checks without publishing. Successful runs on `main` publish
`ghcr.io/asabla/duplex:latest` and `:sha-<full-commit>` as multi-platform images.
Image layers use separate architecture caches. Publication uses the repository
`GITHUB_TOKEN`; it needs no personal registry credential.

For an intentional local development build, use `docker build -t duplex:local .`
and set `image: duplex:local` in a local Compose copy. The committed Compose
examples use the image built by GitHub Actions.

Each production package needs at least 80% statement coverage. The gate merges
unique statement blocks from the same source fingerprint across native and Linux
runs. On Linux, it needs FUSE and root or sudo for isolated mount tests. On macOS,
it uses the `colima` Docker context, a disposable container, bounded tmpfs fixtures,
and the FUSE mount exception. Set `DUPLEX_DOCKER_CONTEXT` to use another Linux
daemon; the script does not change the global Docker context. CI runs the same
gate on Linux, including both architecture builds.
A skipped test is not evidence that a release requirement passed.

## Verification by change

- For prose-only changes, check local links, commands, policy consistency, and
  source claims. Review each file's diff. Do not run the application quality gate
  only to verify prose.
- For code changes, run the affected behavioral tests and `make check`. Keep test
  scenarios complete. Add behavioral coverage when the change creates a meaningful
  risk; do not add formatting-only tests.
- For rendered interface changes, run relevant Go handler and template tests,
  inspect pages and fragments with the built-in Browser, and check keyboard
  operation and focus. Follow the root instructions for browser tools.
- For Linux filesystem, media, or cache changes, include the applicable Linux,
  controlled media, and race checks. Use the validation register for additional
  live-source and Plex requirements.
- Record exactly what passed, failed, or was skipped. Keep local checks, CI,
  live qualification, and release acceptance distinct.

Templates, CSS, JavaScript, and HTMX are embedded in the binary. There is no
frontend build. The npm installation above supplies development checks only.
Third-party HTMX, generated notices, licenses, and retained evidence are outside
source formatting scope.

For an authorized layout change, limit writes to the affected files. For example:

```sh
golangci-lint fmt ./internal/admin
shfmt -w -i 2 -ln posix scripts/check.sh
ruff format scripts/profile-playback.py
tools/readability/node_modules/.bin/prettier \
  --config tools/readability/prettier.json \
  --plugin "$PWD/tools/readability/node_modules/prettier-plugin-go-template/lib/index.js" \
  --parser go-template --write internal/admin/templates/settings.html
```

Run the template formatter again if its first expansion changes layout, then
confirm that a further check makes no changes. Keep the narrow picker text
exception described in Code standards. For CSS, JavaScript, and development
`.mjs` files, use the same Prettier command with the appropriate file path and
omit `--parser go-template`.

`./scripts/check-readability.sh` runs the non-Go checks separately. It creates 28
controlled pages and fragments in `.cache/markup` through
`DUPLEX_MARKUP_TEST=1 go test -race -count=1 -run '^TestRenderedMarkupFixtures$' ./internal/admin`.
The fixture adapter uses no external source or Plex service. The HTML validator
checks labels and native elements, and writes `.cache/markup/markup-validation.json`.
CI retains this summary. Do not upload the raw HTML, session values, or local
fixture configuration. These checks complement the Browser and keyboard review.
The artifact step includes hidden directories so its explicit `.cache` paths
are uploaded. Keep that path list limited to the reviewed check outputs above.

## Tests and evidence

Controlled MP4 tests compare packet data and timing against independently prepared
references, decode original audio, read distant ranges concurrently, and check
end-of-file behavior. Real-source and Plex tests are separate release gates.
FFprobe 6.1 can omit a nonterminal AAC packet duration. The comparison derives
only that absent field from adjacent integer decode timestamps and the stream
time base. Explicit durations, payload hashes, PTS, DTS, decoded audio, and
seeking remain checked. A missing terminal duration or unproven interval fails.
Record dependency versions and retain failed experiments as well as successful
results. Never commit signed source URLs, tokens, profiles, credentials, or local
installation state.

The [validation requirements](validation.md) define the release gates. Keep results
for each build separate. Active-live playback and explicit playlist URL import
remain outside the current scope; discovered playlist seasons are implemented.

## Commits and documentation

Use [Conventional Commits](https://www.conventionalcommits.org/en/v1.0.0/), with a
focused reason and a reviewable change. Keep commits local unless a remote is
explicitly configured and publication is authorized.

The README follows [GitHub's README guidance](https://docs.github.com/en/repositories/managing-your-repositorys-settings-and-features/customizing-your-repository/about-readmes).
Documentation uses the [Diátaxis](https://diataxis.fr/) distinction between an
installation tutorial, operating procedures, reference material, and architecture
explanations. Keep raw qualification output outside Git. Publish only reviewed, sanitized
results that identify their exact revision and test scope.
