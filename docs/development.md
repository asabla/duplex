# Development

Read the root [repository instructions](../AGENTS.md) before changes. The
[code standards](code-standards.md) define readability rules, manual review, and
existing and planned automated checks. [Architecture](architecture.md) defines
package responsibilities and runtime boundaries. This document owns tool setup,
commands, and verification procedures.

## Tools

Use the versions in [`.tool-versions`](../.tool-versions) and [go.mod](../go.mod):

- Go 1.27.3.
- golangci-lint 2.15.0, with formatting and imports enforced.
- gitleaks 8.30.1.
- govulncheck 1.9.0.
- FFmpeg and ffprobe for controlled media comparisons. The retained local evidence uses 9.0.2.

Install the pinned tools through the existing mise configuration or make those
exact versions available on PATH. Check their versions before running the gate.
The gate checks the Go toolchain and analysis tools; it needs FFmpeg and ffprobe
on PATH but does not enforce the retained FFmpeg evidence version.

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
and arm64 binaries. The gate is defined in [`scripts/check.sh`](../scripts/check.sh).
[`CI`](../.github/workflows/ci.yml) calls `make check` and also validates production
Compose configuration. Use `golangci-lint fmt` only for an authorized formatting
change, then review the diff. It can rewrite Go files across the checkout; keep
concurrent work outside its scope.

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

Templates, CSS, JavaScript, and HTMX are embedded in the binary. There is no npm
installation or frontend build. See Code standards for their manual review rules
and the status of proposed formatters and validators.

## Refactor baseline status

During planning on 2026-10-25, the installed golangci-lint 2.15.0 accepted the
current configuration schema. An attempt to run `make check` stopped at its tool
prerequisite check because `govulncheck` was missing from PATH. The documentation
preparation also found concurrent source changes. These observations do not
establish a stable or passing refactor baseline.

Before enabling the planned checks, make the pinned tools available, identify a
stable source revision and any local changes, and run the existing gate. Record
failures and environmental blockers separately from proposed-rule findings.
The later integrated-source baseline at `b7e3415` passed the complete existing
gate, including Linux/FUSE tests, both architecture builds, and at least 80%
statement coverage in every package. The pinned vulnerability scanner was
available in the existing local tool cache and was added to the command's PATH.
See the [baseline record](evidence/structure-baseline.json) for versions, source
fingerprint, coverage, and the separate proposed-rule findings. The isolated live
Plex qualification test remained skipped; this is not a release acceptance claim.

## Tests and evidence

Controlled MP4 tests compare packet data and timing against independently prepared
references, decode original audio, read distant ranges concurrently, and check
end-of-file behavior. Real-source and Plex tests are separate release gates.
Record dependency versions and retain failed experiments as well as successful
results. Never commit signed source URLs, tokens, profiles, credentials, or local
installation state.

The V01–V12 register is the release authority. Keep historical results separate
from a new build's results. Provider, active-live, and explicit playlist-import
scope remain deferred; V08 covers discovered playlist seasons in this release.

## Commits and documentation

Use [Conventional Commits](https://www.conventionalcommits.org/en/v1.0.0/), with a
focused reason and a reviewable change. Keep commits local unless a remote is
explicitly configured and publication is authorized.

The README follows [GitHub's README guidance](https://docs.github.com/en/repositories/managing-your-repositorys-settings-and-features/customizing-your-repository/about-readmes).
Documentation uses the [Diátaxis](https://diataxis.fr/) distinction between an
installation tutorial, operating procedures, reference material, and architecture
explanations. Put raw qualification output in evidence, not in installation steps.
