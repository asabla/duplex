# duplex household acceptance register

Recorded 2026-10-25. The application is implemented. The media and mount gates
passed for the profiles below. Acceptance applies to this private household
installation and Plex Web. It does not establish all-device compatibility,
four simultaneous 4K conversions, or public distribution readiness.

The original research register's V01–V12 identifiers and topics are retained in
[validation.json](validation.json). The supplied PDF and JSON are research evidence;
the approved implementation plan defines the requirements. Exact tool versions
are recorded in [dependency-versions.json](dependency-versions.json), linked Go
modules in [go-dependencies.json](go-dependencies.json), and image system packages
in the architecture-specific runtime package records.

## Media gate

Controlled progressive and finite fragmented AVC/AAC fixtures passed independent
FFmpeg reference comparisons. Each track's ordered packet payload hashes, PTS,
DTS, and durations match the reference. Full fixture decode, start and end seeks,
end-before-middle reads, repeated distant reads, four random readers, and bounded
cache use passed. Full reference files exist only in controlled test generation;
the application has no full-file preparation fallback.

Actual public-source results:

| Source and profile | Index work | Source bytes | Total fetched in range/probe/decode test | Result |
| --- | ---: | ---: | ---: | --- |
| Big Buck Bunny upload, 2160p AV1/AAC, 635 seconds | 7.677 s | 722,716,776 | 23,197,802 | Passed |
| NASA Crew-11 completed replay, 1080p AVC/AAC, 17,325 seconds | 184.428 s | 3,174,744,457 | 32,884,119 | Passed |
| Same replay, 2160p AV1/AAC | 208.524 s | 7,132,272,976 | 39,116,126 | Passed |
| Same replay, finite combined 360p AVC/AAC | No separate-track index | 757,509,646 | 13,038,928 | Passed |

The upload was repeated with fresh locators from the final pinned Docker tools.
The replay tests read the end and arbitrary offsets, repeat those reads, inspect
the streams, and decode near 1 second and 17,270 seconds. They do not fetch a full
source file. These measurements are observations, not performance promises.

The final followed-channel catalogue contains 33 exposed recordings: 30 jackfrags
and 3 Blender videos, with 5,806 durable identity records retained. Twenty-five
selected sources are 2160p. A 12,038,858,451-byte followed-channel recording also
played and rendered after a seek to 53:24 in Plex Web.

The first 90-second replay deadline failed. Unpaced header requests also received
upstream 401/403 responses. Changing to HTTP/1 alone did not fix the failures.
The design now bounds header concurrency and paces requests, allows bounded
background index work, retains immutable indexes, and exposes verified lower
quality when necessary. Failed logs remain with the passing evidence.

## Plex and mount gate

The isolated Plex 1.43.5 server initially exposed no NFO Series agent. Claiming it
and restarting enabled the installed agents. The application checks the actual
provider and the dedicated library agent, rather than relying on version alone.

The tested NFO layout imports channel and episode titles, generic YouTube IDs,
local artwork, year seasons, persistent episode slots, and English sidecar
subtitles. A real Blender recording played at Original quality with rendered
English subtitles and sought near its end. The 4K NASA replay played and sought
to 4:35:39 in Plex Web; Plex converted its AV1 video for this browser. Explicit
0.7 Mbps conversion of the NFO fixture produced 480x270 AVC/AAC and played after
a seek. Conversion capacity belongs to the Plex host; the maximum-quality AV1
conversion reported speed below 1 during one observation. It is not qualified as
four real-time 4K conversions.

Saved resume, watched count, source identity, locked Plex title, source-title
changes, and backfill were checked. Invalid fixture refresh data retained the last
complete mounted snapshot. Catalogue tests also prove stable numbering, limits,
metadata rollback, source failure retention, and broadcast state transitions.

A bind of the FUSE root into Plex failed the remount test: Plex could retain an
old disconnected mount. The final deployment binds a stable public parent, with
FUSE mounted at its `media` child. Plex stayed running during a killed mount
process and read the replacement mount. The same final episode rating key, GUID,
resume position, watched count, and locked title survived. The earlier layout
migration is qualification history; it is not the deployment layout.

Four independent Plex Web sessions then played at 40, 67, 95, and 124 seconds in
the corrected layout. Their 42,710,496-byte virtual source used an 8,388,608-byte
cache. The final snapshot records 8,335,360 allocated bytes, below the cache limit,
after repeated seeks. This proves eviction with a working set larger than the
cache; cumulative playback traffic is not startup preparation. One simultaneous resource observation is retained in the evidence,
with background channel sync active. It is not a peak-memory measurement.

Start and seek decode timing was also measured through the final Plex HTTP part
paths. FFmpeg one-second decode took 0.273, 0.439, 0.819, and 1.483 seconds at
fixture offsets 0, 150, 30, and 175 seconds. For the 4K replay it took 0.183, 0.393,
and 0.519 seconds at offsets 0, 16,539, and 17,270 seconds. These warmed-cache
measurements include open and seek work. They are not browser startup latency or
a service-level guarantee. See the raw timing record for the method.

## Storage, failure, and deployment gate

Cache tests cover logical and physical-space reservations, deduplicated fetches, pinned chunks,
checksums, failed writes, exclusive ownership, and restart cleanup. The 50 GiB
default boundary is tested with unfinished logical reservations without allocating
50 GiB of test data. Real disk exhaustion was exercised in an isolated 2 MiB
Linux tmpfs, including a race in which space disappears after the capacity probe.
A separate real-filesystem test proves cold chunks are evicted under physical
pressure and pinned chunks are retained. A cache writer was killed during publication; no partial chunk was
published and restart recovered correctly. Linux FUSE tests prove open-handle
revision binding, removed-entry handles, write rejection, stale-handle errors,
and safe recovery from a killed mount process.

Expired-source reads through Plex failed with zero partial bytes, rather than
successful zero-filled output. The same range returned 206 and 4096 bytes after
the controlled origin recovered. Range tests reject changed lengths, validators,
ignored or malformed ranges, and truncated bodies. Existing handles can fail at
URL expiry; automatic locator renewal is not implemented. Refresh resolves a new
verified representation without changing an open handle's revision.

A full Docker data disk caused Plex HTTP 500 errors and stopped its logs while
the duplex cache disk still had space. Removing only identified disposable duplex
build cache restored Plex. Saved resume and state were retained and then retested.
Media cache capacity, durable storage, Docker images, Plex database, and Plex
transcode storage must be monitored separately. A final audit found that per-library
intro, credits, and loudness flags remained enabled despite other server defaults.
They were disabled in all isolated test libraries. The scan health check now
verifies those library flags, as well as thumbnails and automatic trash emptying.

Both source-built Linux architecture images pass tool startup, administrator and
SQLite initialization, daemon health, and online backup with a read-only root and
noexec temporary storage. arm64 runs natively in Colima. amd64 startup and FUSE
crash tests run under emulation on that arm64 kernel; native amd64 performance was
not measured. The supplied native systemd unit also passed actual start, notify,
mount readiness, restart, and unmount on Ubuntu 24.04.4 LTS, using an unprivileged
VM user and the pinned native tools. Compose and host propagation checks passed.

Online backup tests verify database integrity, mode 0600, refusal to overwrite an
existing destination, and retention of running job state. Authentication tests
cover sessions, CSRF, cross-origin rejection, and logout. Configuration tests cover
external valid/invalid edits and stale-save rejection. The final interface was
checked in the built-in Browser, including local catalogue search and pagination.

## Release boundaries

The provider route, explicit playlists, active live playback and live-edge tests,
authenticated YouTube sources, global/provider search and quota tests, rclone,
native macOS FUSE, and TV/mobile device qualification remain deferred by scope.
The lifecycle gate covers stored states and completed replays, not observation of
a real event through every live transition. Broader codec, language, network, and
hardware coverage needs separate qualification. Public distribution and claims
of official YouTube authorization require separate review.

Evidence files and their SHA-256 values are listed in the JSON register. Raw
signed source JSON, passwords, Plex tokens, and account response bodies are not
part of the evidence set.
