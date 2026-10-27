# Validation requirements

## Source status

This is an experimental source snapshot. It is not a qualified application
release. Local quality checks, hosted CI, live playback, and deployment
acceptance are separate results. A passing build does not establish all of them.

Private development records and screenshots are not part of this repository.
They must not be used as acceptance evidence for a new revision. Record each new
run against an exact commit and source fingerprint, with tool versions, workload,
commands, failures, and skipped checks. Keep raw profiles, signed source URLs,
credentials, and installation state outside Git.

Repository fixtures must use controlled data that represents the required
workload shape: catalogue size, recording duration, codecs, language tags,
caption timing, cache state, and access patterns. Keep real channel identifiers,
live-run reports, and installation-specific results local. A successful live run
can identify a missing regression case; add a controlled case for that behavior.

## Local quality gate

Run `make check` with the pinned tools described in [Development](development.md).
The gate checks layout, lint, secrets in files and Git history, Go vulnerabilities,
race behavior, controlled media comparisons, Linux filesystem and cache behavior,
at least 80% statement coverage per production package, and Linux amd64/arm64
builds. Validate production Compose with `docker compose config --quiet`.

Controlled media tests compare packet payloads, timestamps, decode results,
distant reads, and cache behavior against independently generated references.
Linux tests cover FUSE handles, write rejection, source failures, physical disk
pressure, pinned reads, interrupted publication, and mount recovery. Inspect
skipped tests; live Plex qualification needs a separate controlled installation.

These checks establish the tested code contracts. They do not establish browser
rendering, provider availability, conversion capacity, or household acceptance.

## Live media and Plex gate

Use a dedicated test library and preserve existing user state. Verify:

- A public 2160p upload and a completed long replay, from preparation through
  rendered Plex Web playback, distant seeking, and resume.
- Original audio and available original-language subtitles, including after seeks.
- Sustained playback for four independent viewers with the documented host,
  quality, cache limit, source workload, and Plex conversion settings.
- Restart with retained catalogue and indexes and a new empty media cache;
  unchanged refresh; exact media identity and saved Plex progress.
- Channel and playlist identities, reorder, removal and return, separate occurrence
  history, named seasons, title changes, and artwork refresh.
- Independent upload, Shorts, and replay windows; individual Keep/Exclude and
  return to Automatic; preview and save through HTML and JSON; retained Plex
  identity and progress across these selection changes.
- Source expiry and truncated reads, disk exhaustion, cache pressure, and recovery
  after a killed mount process. Failed bytes must reach the client as errors.
- Read-only projection and stable-parent mount propagation while Plex stays running.
- Installation, backup, restore, upgrade, and service recovery on the target host.

Earlier concurrent decoder and restart trials returned incomplete reads and a
timeout. A later cache reservation fix passed scoped upload, short concurrent
frame, and empty-cache restart probes. That result does not close the sustained
four-viewer, completed-replay, browser rendering, or final deployment gates.
Repeat those scenarios on the final revision before a release acceptance claim.

## Scope boundaries

The current scope is recorded uploads and completed broadcast replays on Linux
amd64/arm64, with Plex Web as the client under test. Active live playback,
authenticated sources, global YouTube search, explicit playlist URL import, WebM
packaging, rclone, native macOS FUSE, and TV/mobile client qualification are outside
this release scope. Native amd64 performance needs its own measurement.

Source publication does not establish public binary or image redistribution
acceptance, broad device compatibility, or official authorization by an external
service. See [Licensing](licensing.md) for the source license and third-party scope.
