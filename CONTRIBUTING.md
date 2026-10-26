# Contributing

Read [AGENTS.md](AGENTS.md), [Code standards](docs/code-standards.md),
[Development](docs/development.md), and [Architecture](docs/architecture.md)
before a change.

Keep each change focused. Describe the problem, the resulting behavior, and the
checks that ran. Preserve source identities, stored formats, cache limits, and
open-handle behavior unless the change explicitly addresses that contract.

Use the pinned development tools. For code changes, run the relevant behavioral
tests and `make check`. Validate Compose changes with `docker compose config --quiet`.
For prose changes, check links and source claims. Report failures and skipped
checks. A controlled test does not qualify live Plex playback.

Use Conventional Commits for commit messages. Include a regression scenario when
a behavior change needs one. Do not split complete test scenarios to meet a size
metric or add tests that only repeat formatting rules.

Do not include passwords, Plex tokens, signed source URLs, raw resolver output,
profiles, screenshots of private installations, media payloads, or local state.
Use synthetic data for reproducible reports. See [Security](SECURITY.md) before
reporting a possible vulnerability.

Unless you state otherwise, contributions are provided under the project's
[MIT License](LICENSE). Preserve all third-party copyright and license notices.
