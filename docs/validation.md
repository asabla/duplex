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
The gate also enforces production import boundaries, Python and JavaScript
semantic lint, ShellCheck, and Actionlint. Bounded fuzz tests cover retained MP4
layouts, structured legacy sample-table mutations, and NFS RPC.
Valid retained layout seeds must reach a complete legacy upgrade and match the
current layout. Repacking must not mutate a retained input or return an invalid
range map. Coordinated sample-size and offset mutations must reject overlapping
track sources. A successful repack must reload with the same identity, header,
and size. Embedded assets and test fixtures are part of the coverage fingerprint.

Preparation tests must prove that failed combined formats cannot consume the
separate-track fallback budget and that each class has at most six attempts.
Candidate ordering must be strict and preserve equal-ranked input order.
The gate also runs the focused recovery tests in a shuffled order and records
the seed so a failed order can be reproduced.

Controlled media tests compare packet payloads, timestamps, decode results,
distant reads, and cache behavior against independently generated references.
Separate-track mux tests must bound chunk duration and size at sample boundaries,
preserve variable sample timing and source gaps, and retain the exact identity,
bytes, and saved format of legacy version 1 indexes.
Linux tests cover FUSE handles, write rejection, source failures, physical disk
pressure, pinned reads, interrupted publication, and mount recovery. Inspect
skipped tests; live Plex qualification needs a separate controlled installation.

A warm four-byte payload read must allocate at most 4,096 bytes and 32 objects
per operation, with no new source resolution or HTTP request. One hundred
repeated header opens with a cold runtime binding must load the local index
once and make no source requests, including when uncached reads are deferred.
These count and memory budgets run with the controlled media tests. They do not
set a host throughput or latency target.

Recovery tests must retain the canonical video ID across concurrent binding
eviction. Rejected fresh locators must share the one-second failure delay;
repeated and late readers must not erase or extend it. After the delay expires,
the same retained handle must recover without changing durable media identity.
HTTP 429 and 503 tests must cover retry seconds and dates, invalid and excessive
values, shared delays across readers and source-URL renewals, cached reads,
cancellation, unrelated sources, and recovery after expiry. Temporary deferrals
must not mark prepared media failed. Initial preparation must wait without a
worker retry loop. Source 401/403 and 412 responses retain their existing error
classes.
Cancellation and timeout types must survive HTTP probe and payload body-read
failures, including a client timeout while the request context remains live.
Error messages must not expose source locators.
After cache close and reopen, calls through the closed store must not change
limits, remove the new owner's pinned files, start a fetch, or release its lock.

NFS overload tests must fill all 24 payload-read slots and prove that excess
READs return JUKEBOX before provider access. GETATTR and directory requests must
still complete on the same or a separate connection while payloads remain
blocked. Released reads must restore capacity. Malformed read-only RPCs must
return GARBAGE_ARGS without provider access. File-handle fuzz seeds must reach
successful payload reads, and mutations must retain this rejection invariant.

These checks establish the tested code contracts. They do not establish browser
rendering, provider availability, conversion capacity, or household acceptance.

## Container publication

The [CI workflow](../.github/workflows/ci.yml) also builds and tests the packaged
runtime on native Linux amd64 and arm64. It publishes a multi-platform image to
GitHub Container Registry only after the quality gate and both image tests pass.
Image tests must verify non-root NFS server and consumer containers with zero
capabilities and no FUSE device, default AppArmor/seccomp, a Docker-managed NFS
mount through the fixed loopback host port, private-state export refusal, write
rejection, and recovery across graceful and forced service restarts while the
consumer stays running. Retained legacy FUSE tests separately verify the enforced
helper profile, rejected mount types and targets, denied network sockets and
unauthorized peers, and replacement mount propagation. These checks establish
filesystem deployment contracts, not Plex rendering or saved watch state.
Verify that a new package is public with an anonymous registry request; repository
visibility alone does not establish package visibility. Check both platforms and
the source revision on the published manifest. These checks establish image
availability and controlled startup, and do not replace the live gates below.

## Live media and Plex gate

Use a dedicated test library and preserve existing user state. Verify:

- A public 2160p upload and a completed long replay, from preparation through
  rendered Plex Web playback, distant seeking, and resume.
- Original audio and, with caption fetching enabled, available original-language
  subtitles, including after seeks. Publish a subtitle after the initial NFS
  directory scan and verify detection for the upload and every playlist
  occurrence through targeted refresh. Unchanged assets must not invalidate
  directories or reset pending retries.
- Sustained playback for four independent viewers with the documented host,
  quality, cache limit, source workload, and Plex conversion settings.
  For packaging changes, compare HTTP range counts and transferred bytes using
  the same source tracks, playback duration, client, and Plex settings. Confirm
  that reduced range traffic retains decoded frames, audio, and seek results.
- Playback protection with media in other Plex libraries, including paused
  sessions; preparation and optional asset interruption and queue recovery;
  unavailable monitoring and the continuous idle grace period. Test manual scans
  with a monitoring-only connection and verify that duplex sends no Plex mutation
  requests.
  Interrupt thumbnail and caption requests, initial warming, and an expired
  asset retry. Completed files must remain, deferred jobs must retain no failure
  or retry delay, and shared foreground source requests must continue.
- First episode-details visit and first Play separately, with cold and warm
  bindings. Record filesystem, cache, source, CPU, disk, and Plex conversion
  activity while multiple channels are queued. Local media playback must remain
  usable during the same workload. Controlled admission tests alone do not
  establish this host-level result.
- A 500-recording controlled scan with large range maps: cold and repeated
  header reads, bounded cache eviction, exact bytes, index I/O, allocation and
  CPU totals, upstream requests, and total/unique/duplicate resolutions. Include
  offline headers and cache hits, concurrent uncached payload, independent
  waiter cancellation, source expiry, and unchanged thumbnail artifacts.
- Batched and scoped scans, accepted scans longer than the request deadline,
  new publication during a scan, prerequisite correction without restart, and
  metadata queue recovery after a binding failure. Verify reported Plex
  activities as well as the dedicated scanner flag. An idle activity endpoint
  does not prove that all Plex operating-system processes are idle.
- Restart with retained catalogue and indexes and a new empty media cache;
  unchanged refresh; exact media identity and saved Plex progress.
- Channel and playlist identities, reorder, removal and return, separate occurrence
  history, named seasons, title changes, and artwork refresh.
- Independent upload, Shorts, and replay windows; individual Keep/Exclude and
  return to Automatic; preview and save through HTML and JSON; retained Plex
  identity and progress across these selection changes.
- Source expiry and truncated reads, disk exhaustion, cache pressure, and recovery
  after a killed mount process. Failed bytes must reach the client as errors.
  Reject a locator before its advertised expiry and verify that concurrent
  readers share recovery without changing media identity. A late rejection
  must not invalidate a newer lease.
- Viewer-specific uncached admission: an unrelated scanner read receives a
  temporary NFS retry while active viewers, including a viewer that joins later,
  continue. Verify actual Plex session file matching, free server request slots,
  retry recovery, and source process cancellation. Stop discovery during metadata
  resolution and listing, then resume without failure state or partial removals.
  Include a second Plex viewer with cold media bytes and no runtime binding.
  The local test admits it after the file appears in a supplied session
  observation; live qualification must also prove Plex can reach that observation
  without requiring a blocked payload read first.
- Offline layout maintenance: preview, service-lock refusal, conversion with no
  network requests, interrupted-run recovery, and repeated apply. Retain old
  revision files, exact source bytes, recording slots, Plex keys, and progress
  across the upgrade and a subsequent scan. A cached range may reuse only
  private verified bytes; mutation of a caller buffer must not change them.
- Read-only NFS projection, unchanged file handles after restart, and reconnect
  while Plex stays running. Old representation handles must return their original
  bytes or fail, and must never return replacement content. Legacy FUSE deployments
  retain their stable-parent propagation check.
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
