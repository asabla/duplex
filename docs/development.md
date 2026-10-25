# Development

Go code follows the [Google Go Style Guide](https://google.github.io/styleguide/go/).
Production functions must have cyclomatic complexity at most 15 and cognitive
complexity at most 20. Use small stages with explicit inputs, outputs, cancellation,
and errors. Suppressions must name one check, sit at the relevant line, and explain
the established safety boundary.

## Tools

Use the versions in `.tool-versions` and `go.mod`:

- Go 1.27.3.
- golangci-lint 2.15.0, with formatting and imports enforced.
- gitleaks 8.30.1.
- govulncheck 1.9.0.
- FFmpeg and ffprobe for controlled media comparisons. The retained local evidence uses 9.0.2.

`make` prints the four supported targets. It performs no setup or downloads.

```sh
make build
make test
make check
```

`make check` checks formatting without rewriting files, runs strict lint, scans
working files and Git history for secrets, checks Go vulnerabilities, runs race
and controlled media tests, enforces package coverage, and builds Linux amd64
and arm64 binaries. Use `golangci-lint fmt` to apply formatting, then review the diff.

Each production package needs at least 80% statement coverage. The gate merges
unique statement blocks from the same source fingerprint across native and Linux
runs. On Linux, it needs FUSE and root or sudo for isolated mount tests. On macOS,
it uses the `colima` Docker context, a disposable container, bounded tmpfs fixtures,
and the FUSE mount exception. Set `DUPLEX_DOCKER_CONTEXT` to use another Linux
daemon; the script does not change the global Docker context. CI runs the same
gate on Linux, including both architecture builds.
A skipped test is not evidence that a release requirement passed.

## Frontend

Use readable Go templates, semantic HTML, native controls, compact CSS, embedded
HTMX, and small amounts of plain JavaScript. There is no npm installation or
frontend build. Protect unsaved forms and active controls before a background swap. Filters,
sorting, and pagination belong in URLs and bounded database queries. Follow the
[WAI table guidance](https://www.w3.org/WAI/tutorials/tables/) for table names and
header relationships. Use the [GOV.UK table](https://design-system.service.gov.uk/components/table/)
and [pagination](https://design-system.service.gov.uk/components/pagination/) conventions
as interaction references; no component library is imported.

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
