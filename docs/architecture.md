# Architecture

duplex separates source identity, collection selection, verified media, and Plex
state. YouTube IDs do not depend on filenames or Plex rating keys. YAML controls
settings; SQLite stores catalogue and work state; the media cache is evictable.

## Current code organization

The package map below describes production imports rechecked on 2026-10-26,
including platform-specific files. It does not describe test imports or a new
package layout. Concurrent changes can alter the source; recheck affected
boundaries before starting a refactor. [Code standards](code-standards.md) defines
readability policy. This document owns responsibility and dependency boundaries.

| Package | Current responsibility | Direct internal dependencies |
| --- | --- | --- |
| `cmd/duplex` | CLI dispatch, startup, HTTP service lifetime, backup, and qualification commands. | `admin`, `app`, `cache`, `catalog`, `config`, `diagnostics`, `library`, `media`, `plex`, `source`. |
| `internal/admin` | HTTP routing, authentication, request parsing, HTML and JSON responses, embedded templates and assets. | `app`, `catalog`, `config`. |
| `internal/app` | Coordinates discovery, preparation, publication, runtime bindings, Plex integration, artwork, and worker lifetime. | `artwork`, `cache`, `catalog`, `config`, `diagnostics`, `library`, `media`, `plex`, `source`. |
| `internal/catalog` | SQLite schema, migrations, identities, selections, durable work, bounded queries, and projection state. | `source`. |
| `internal/config` | YAML schema, policy validation, revision-checked saves, and reload snapshots. | None. |
| `internal/source` | Resolver process adapter, collection enumeration, metadata, candidate selection, and transient source locators. | `config`. |
| `internal/media` | HTTP range sources, verification, MP4 mapping, and representation indexes. | `cache`, `config`. |
| `internal/cache` | Chunk storage, reservations, pins, eviction, exclusive ownership, and recovery. | None. |
| `internal/library` | Read-only FUSE mechanics and the filesystem provider contract. | `media`. |
| `internal/plex` | Plex protocol requests, library checks, observations, and metadata operations. | None. |
| `internal/artwork` | Image composition, normalization, fonts, and output files. | `config`. |
| `internal/diagnostics` | Bounded operation measurements, counters, and private profiling. | None. |

Dependencies point from commands and HTTP handlers through application operations
to storage and adapters. The current graph also contains direct command access
for backup and qualification, admin use of catalogue/configuration types,
catalogue use of source metadata, and media use of cache/configuration. These are
existing dependencies to review; this map does not introduce a universal layer
rule or require new packages.

The `library.Provider` interface is defined by the filesystem consumer. The
application implements it with bounded catalogue reads and representation binding.
This permits FUSE callbacks to request data without importing `app`. The
`media.Representation` contract keeps identity, exact size, and offset reads
separate from the application that publishes them. Preserve these boundaries.

Administration server assembly and routing remain in `server.go`. Template
formatting and rendering live in `templates.go`, with the page model in `view.go`.
Request protection, session endpoints, and response handling have separate code
locations. The HTML templates and transport contracts are unchanged by this
responsibility extraction.

## Current state and resource ownership

| Owner | State and synchronization | Lifetime and persistence |
| --- | --- | --- |
| Service command | Configuration manager, authentication, application, listener, and profiling session. | Startup constructs them; HTTP serving returns before the deferred application close. |
| `app.App` | Shared runtime maps and status fields use `mu`. It also owns queues, wake channels, preparation slots, library-update serialization, cancellation, and worker waiting. | `Open` acquires cache ownership, opens the catalogue, mounts the provider, and starts workers. `Close` cancels and waits, then closes the mount, cache, and catalogue. |
| `config.Manager` | `fileMu` serializes file operations; `mu` protects the active snapshot. | YAML saves use expected revisions and atomic replacement. Invalid reloads keep the last valid snapshot. |
| `catalog.Store` | Owns one write connection and at most two read-only projection connections. Catalogue operations manage rows and transactions. | SQLite retains identities, selections, preparation work, and projection state through restart. Readers open after migration and recovery and see committed WAL state. Callers must close rows and finish transactions; shutdown closes both pools. |
| `cache.Store` | A mutex protects reservations, entries, pins, and close state; a directory lock enforces one process owner. | Chunk reads hold pins until release. Publication and recovery preserve accounting; the application closes the store. |
| `library.Mount` and open handles | The mount owns the FUSE server and provider. Each handle retains one representation; static qualification snapshots use atomic replacement. | The application unmounts after its workers stop. Cache eviction or source renewal must not change an open handle's representation. |
| Source adapter and diagnostics | The resolver bounds listing and metadata work with slots. The recorder protects its aggregates with a mutex. | Process calls have contexts and deadlines. Locators remain transient; measurements retain bounded operation data. |
| `admin.Auth` | A mutex protects session and login-attempt state. | Protected files hold credentials; sessions and rate limits are process state. |

`App.mu` is shared by several responsibilities; this table is not a complete lock
ordering proof. Some state belongs to a single worker or the scheduler. Before
moving it, inspect all readers and writers, the channel or lock that serializes
them, cancellation paths, and startup failure cleanup. Preserve acquisition and
release order. Keep each SQLite transaction's commit, rollback, row closure, and
restart boundary visible when extracting operations.

The application now groups mutable state by responsibility: `schedulerState`
owns discovery queue bookkeeping and applied configuration, `preparationState`
owns preparation capacity and active runs, `representationState` owns runtime
bindings and leases, and `libraryState` owns scan and hint publication bookkeeping.
`assetState` owns the optional asset wake channel and the single active asset
operation. Its mutex only protects channel creation; it does not span I/O.
They remain inside `App` and use the existing shared mutex; transitions across
these groups retain their original atomicity. The scheduler alone uses its
`lastIntegration` timestamp. Lifecycle code owns acquisition, worker startup, and
cancel/wait/close ordering.

Configuration publication has a separate mutex and retains the last applied
visibility policies and configuration revision. A visibility change advances
the affected channel's durable directory time and requests a managed scan,
without waiting for source discovery. Pausing, unfollowing, playlist exclusion,
and return retain media revisions and occurrence identities. Startup republishes
retained visible directories. Unchanged publication and unrelated settings do
not advance directory times. Publication does not hold `App.mu` during database
work or wait for a Plex request.

Channel configuration operations live in `channels.go`; compatibility status is
in `status.go`; publication and scan scheduling are in `publication.go`. Durable
asset I/O is separate from asset composition and queue work. Discovery keeps its
run boundary in `discovery.go`, with upload cursors, playlist selection, and
metadata resolution next to their corresponding operations. Season artwork
rendering is separate from channel branding and final publication.

Catalogue connection setup stays in `catalog.go`; canonical video and prepared
records are in `records.go`, discovery job operations in `jobs.go`, and online
backup in `backup.go`. Queries and Plex binding writes remain with their existing
catalogue operations. `Prepared` retains `source.Metadata` to preserve the current
boundary types; this review did not add a shared types package or change APIs.

The source adapter keeps resolver operation order in `youtube.go`, subprocess
limits and cleanup in `process.go`, boundary metadata in `metadata.go`, and
streaming collection operations in `collection.go`. Channel decoding, candidate
selection, and network policy have separate code locations within `source`.

Cache ownership and public operations stay in `store.go`; restart recovery,
capacity/reservation accounting, and chunk file I/O are separate from the
deduplicated fetch state machine. Media verification, request-local seeking,
combined-track inspection, durable index storage, and fragment prefetch have
separate code locations. Their byte mapping, limits, and cancellation order are
unchanged. Linux FUSE mount lifecycle stays separate from static snapshots and
node callbacks; provider paging retains its existing contract. No new package,
cache lock, or handle-lifetime rule was introduced by these moves.

CLI dispatch stays in `main.go`; service assembly and lifetime are in `service.go`.
Initialization, backup, health, Plex checks, and local media fixtures have their
own command operations. Profiling and qualification remain explicit commands.
The command interfaces and exit behavior are unchanged.

Configuration keeps schema and decode operations in `config.go`, value and path
validation in `validation.go`, canonical channel URL policy in `channel_url.go`,
snapshot/revision ownership in `manager.go`, and atomic file publication in
`atomic_write.go`. The manager retains its file-operation lock and snapshot lock;
validation callbacks and publication keep their existing scope and order.

Plex protocol requests stay in `client.go`; capability and library policy checks
are in `capabilities.go`, observations and pagination in `library.go`, and durable
source/occurrence identity parsing in `identity.go`. Artwork composition stays in
`artwork.go`; bounded decode/normalization, text layout, and output storage have
separate files. The image decoder registrations remain in `images.go`.
Diagnostics retains its existing recorder, counter, and capture owners. Capture
stop, callback, file closure, and completion notification retain their order.

The notice script separates linked-module collection, notice rendering, and file
publication. The coverage script separates block collection, package reporting,
and merged-profile publication. Controlled script tests retain byte-level output
and failure checks. Development formatters and rendered-markup validation live in
`tools/readability`; they are outside the binary and container runtime.

## Intended direction for later refactors

These are design constraints for future work, not completed extractions:

- Keep command startup and HTTP request/response handling separate from
  application operations. HTML and JSON must use the same operation authority.
- Keep `App` as the application entry point while separating coherent
  responsibilities and their state within the package. Review discovery,
  preparation/runtime binding, publication/Plex integration, and artwork/assets
  independently. A file split alone does not establish ownership.
- Keep SQL, migrations, durable work transitions, and bounded read queries in
  `catalog`. Keep resolver processes and transient locators in `source`. Review
  their shared types before considering any interface change.
- Keep immutable byte mapping in `media`, chunk accounting in `cache`, FUSE
  mechanics in `library`, and protocol requests in `plex`. Application code owns
  the order in which these operations run.
- Keep related types and operations together. Add a package or interface only
  when a concrete dependency boundary needs it. Avoid generic utility packages.
- Preserve interfaces, error behavior, worker limits, security, stored formats,
  and all identity and playback invariants below. Implement structural work in
  separately verified changes.

## Data and identities

| Object | Durable identity | Role |
| --- | --- | --- |
| Channel | YouTube channel ID | Plex show and collection owner. |
| Video | YouTube video ID | Canonical metadata and shared media preparation. |
| Playlist | YouTube playlist ID | Owner, policy, ordered membership, persistent season number. |
| Playlist occurrence | Owner + playlist + video IDs | Separate Plex identity and progress for shared media. |
| Representation | Source object identities + packaging revision | Exact immutable output bytes. |
| Plex binding | Occurrence + server | Rating key and GUID, independent of path. |

An uploader and a playlist owner can differ. A previously inaccessible curated
entry does not establish the playlist owner as its uploader. Source metadata must
supply and preserve the canonical uploader once it is known.

Year seasons keep their assigned paths and episode numbers. Playlist seasons
allocate from 1, skip 1000–9999 for years, and never reuse an assigned number.
Titles are metadata. A title change does not rename identity paths. Playlist
selection is ranked by publication time, then placed in source order. Reorder
changes episode positions while retaining occurrence IDs and immutable scanner
file slots. A file slot is separate from its current display episode number.

## Discovery and preparation

The source adapter runs a pinned yt-dlp release with its matching EJS code and
Deno runtime. Public channel URLs resolve to stable IDs. Listing work uses flat
entries; playback resolution returns transient format and caption locators.
Those locators are not persisted in catalogue metadata.

The pipeline has separate boundaries:

1. Discover lightweight IDs and commit safe additions.
2. Resolve lifecycle and publication metadata under a global limit of four operations.
3. Apply upload and playlist policies. Keep destructive reconciliation behind a
   successful enumeration boundary.
4. Queue canonical media work in SQLite. Two preparation workers share videos
   used by several collections; explicit requests receive higher priority.
5. Verify a representation and save its index and exact size. Its cached runtime
   binding is available for playback before optional asset work starts.
6. For a recording's first preparation, retain a local thumbnail or write a
   deterministic fallback. Allow up to 250 ms for optional source artwork and
   original-language captions, including any wait for the single asset slot.
   Then request the prepared-recording scan. Discovery can request an earlier
   scan before any media is ready; subsequent requests are coalesced.

Optional asset jobs have a durable `warming` state during the initial attempt.
The background worker cannot consume these jobs. An interrupted attempt returns
to `queued`, wakes the worker, and retains its fallback and any completed assets.
Restart also returns interrupted `warming` jobs to `queued`. The background
worker drains pending jobs without a per-item polling delay and keeps its
one-minute operation deadline. Initial warming and background work share one
asset slot. Optional source errors remain separate from media readiness.

Asset manifests retain a modification timestamp for each content-addressed file.
Changed content advances that timestamp even within the same second; unchanged
content retains it. Older manifests without a timestamp use the file timestamp.

Caption lookup uses the selected original audio language. Explicit subtitle
tracks take priority. Automatic caption lookup checks exact and base-language
original tracks before other matching tracks: for example, `en-US` can use
`en-orig`. It does not fall back to an unrelated language.

Schema 7 adds a durable queue for changed episode asset digests. The asset worker
records content identities after manifest publication; unchanged assets retain
the current queue state. The metadata worker refreshes bounded pages of observed
upload and playlist occurrences under the existing library-update lock. Each
request verifies the current Plex section, episode type, and occurrence identity.
Completion acknowledges only the queued digest. Interrupted work resumes after
restart; failed work retains a redacted error and a fifteen-minute retry delay.
Each successful occurrence records its asset digest and Plex key so a partial
failure resumes remaining work. A changed digest or Plex key requires a refresh.
Queue counts and errors share the existing metadata diagnostics fields.

Preparation state is independent of source lifecycle. An upcoming broadcast is
not a failed MP4 preparation. Completed discovery also does not mean all selected
media is ready. Long work and item results remain durable through restarts.

## Virtual MP4

Finite combined MP4 files can be read directly after inspection. Separate tracks
use mp4ff for box and codec structures. duplex constructs progressive sample
tables and an interleaved output range map. Generated headers have fixed size;
output offsets map to exact upstream track ranges. Media payloads are not copied
into a complete prepared file.

Fragment metadata is read under size, concurrency, pacing, and work limits.
Encrypted tracks, unsupported descriptions, discontinuous timelines, invalid
bounds, and ambiguous addressing fail verification. A lower supported quality
can be selected with a visible reason. If no candidate works, the recording is
explicitly unavailable for playback.

Preparation retains the pinned resolver's duplicate format alternatives. If one
client rejects a media URL, another client can still supply the same resolution.
Video and audio from the same client are paired only when the audio has the same
original-language preference and codec class. All candidates still pass range,
size, and container checks; preparation attempts remain bounded. This uses the
resolver's documented [`formats=duplicate` option](https://github.com/yt-dlp/yt-dlp/blob/2026.10.21/README.md#extractor-arguments).

A saved index can be rebound only when fresh sources establish the same identities.
A strong ETag is one source of evidence. The YouTube version path also checks the
signed object-version fields, exact size, and fresh beginning, middle, and end
range fingerprints. Sampled fingerprints are not a complete-file hash. Missing
or changed evidence prevents reuse and creates a new namespace.

After source verification, index and descriptor publication compares existing
regular files with the generated bytes. An exact byte and permission match
retains the file. Comparison uses at most 32 KiB of scratch space per file.
Missing files, changed content, incorrect permissions, and symlinks use the
existing synced temporary-file replacement. This avoids redundant syncs during
a runtime rebind; it does not skip source identity or range verification.

Each open handle keeps one representation. Renewing a URL cannot change its byte
mapping. Expiry and real read errors reach Plex; absent bytes are not replaced by
zeroes.

## Cache

The chunk store reserves configured and physical capacity before fetching. It
counts unfinished writes, deduplicates concurrent requests, publishes by synced
atomic rename, and validates content hashes. Reads pin chunks. Eviction considers
only unpinned data. Failed cleanup stays accounted for until it can be removed.

The cache has one owner process. Startup checks its directory and recovers
unfinished writes. Disk exhaustion and a fully pinned cache return errors.
Packaging indexes, catalogue data, artwork, credentials, and Plex transcodes are
separate storage classes.

## Filesystem and Plex

The Linux FUSE projection is read-only. Attribute queries use durable verified
sizes. Directory reads use lexical keyset pages. Publication does not retain a
complete path or episode map. New media handles bind a cached representation or
verify a fresh source against the saved revision before reads begin.

Episode directory pages seek through partial indexes on the formatted file
number. Upload years and playlist seasons use separate queries; playlist season
allocation excludes the reserved year range. This keeps lexical ordering above
episode 9,999 without sorting the full season for each page. These derived
indexes do not change representation identities or scanner slots.

Schema 6 prepares timestamp indexes for filesystem metadata. Upload years use
an indexed maximum over visible verified videos. Playlist membership changes
and canonical video changes maintain a separate timestamp index in the same
SQLite transaction. Directory reads look up one maximum per collection instead
of aggregating its episodes. Channel and playlist metadata remain joined at
read time, and YAML visibility rules apply immediately. Migration backfills the
derived records and retains existing representation revisions and scanner slots.

Schema 8 advances show and season directory timestamps when a visible upload
leaves, returns, moves, or changes its published metadata. An index supplies the
latest recording timestamp for that channel. Unchanged upload discovery updates
only membership differences, so it retains directory times. Recording file times,
media revisions, scanner slots, and Plex bindings remain unchanged by selection.

Projection reads retain at most 32 prepared SQL statements and 128 KiB of SQL
text per catalogue store. Additional query variants use ordinary uncached reads.
The cache retains query plans; each call binds current values and reads current
committed data. The database closes driver statement resources. The retention
mutex is released before statement preparation or execution. Materialized
parameter rows prevent SQLite from repeatedly recompiling parameter-sensitive
season queries. This changes no stored format or projection identity.

Projection queries and channel branding use a separate read-only pool with at
most two connections. Each connection sets a five-second busy timeout and
`query_only`, and opens the existing database with `mode=ro`. Reads can use the
last committed WAL state while a write transaction is active. Each query gets
its own snapshot; a complete filesystem operation is not one database snapshot.
Private in-memory databases and backup-only stores retain the original single
connection path. Writes keep one connection with the existing foreign-key and
durability settings.

An unchanged show-NFO digest reads its durable timestamp without an update.
Changed digests retain the existing atomic update and targeted Plex refresh.
The statement retention limits apply across the read pool; driver plans and
SQLite page caches can consume more memory with two reader connections.

The runtime index cache has a fixed entry limit and memory budget estimate.
Eviction removes only its cache reference; an open handle keeps its immutable
representation. A source change creates a new revision and fails an attempt to
open the old revision. Unfinished preparations resume after restart. Other
verified recordings bind when opened.

The Linux FUSE projection is read-only. Channels are shows, year and playlist
collections are seasons, and verified recordings are episodes. NFO files contain
source titles, descriptions, independent IDs, and named seasons. Local artwork
uses show, season, and episode asset naming conventions.

Before a managed scan, duplex records every bounded Plex observation page in
SQLite. It moves changed occurrences to temporary indexes, assigns the desired
indexes, and then scans with matching `.plexmatch` hints. Index fields remain
unlocked. It waits for the scanner and reconciles bindings again. Failed work is
visible and can resume from a fresh observation. A shared library-update lock
serializes duplex scans, reconciliation, and targeted metadata refreshes.

Targeted metadata refreshes check scanner state immediately and wait only when
it is busy. Newly requested scans retain their initial two-second grace before
binding reconciliation, because a queued scan can briefly report idle.

Show NFO changes have durable content identities and a paced metadata-refresh
queue. A completed refresh acknowledges only that version. Newer metadata remains
pending. Local text artifacts and artwork are durable data, outside the media
chunk cache.

Plex sees a stable parent bind with shared mount propagation and opens its FUSE
child. This allows a replacement mount to become visible after a crash. The
mount service alone receives FUSE access and the required mount capability.
Plex receives the public projection read-only.

Scans require mount readiness, the actual NFO series agent, disabled automatic
trash emptying and optional media analysis, and enabled local assets and season
titles. Plex performs any client conversion. Its transcode storage is not part of
the duplex media cache.

## Administration and artwork

Go templates and embedded HTMX serve a compact administration panel. HTML and
JSON handlers call the same services. Read filters, sorting, counts, and page
limits are applied in SQLite. Polling does not need the compatibility endpoint's
complete video array and does not replace editable forms.

UI writes validate and atomically replace YAML using the expected file revision.
External invalid edits retain the last valid settings. One administrator account
uses expiring sessions, login rate limits, and CSRF protection. Secrets and raw
profiles remain outside editable YAML and public diagnostics.

Artwork composition runs separately from media preparation. It preserves source
image proportions and uses embedded licensed fonts. Manual uploads become
normalized content-addressed JPEGs in durable storage. YAML references take
precedence over automatic artwork and survive source and title changes.

## Qualification boundaries

Controlled packet, audio, range, and concurrency tests establish mapper behavior.
They do not establish Plex rendering or a complete installation. The release also
requires real 4K upload and replay seeking, actual Plex identities and watch-state
retention, artwork refresh behavior, four viewers, expiry, cache pressure, and
mount recovery. See the [validation register](validation.md) and
[performance evidence](performance.md) for the current build's status.
