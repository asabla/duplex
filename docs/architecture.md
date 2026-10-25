# Architecture

duplex separates source identity, collection selection, verified media, and Plex
state. YouTube IDs do not depend on filenames or Plex rating keys. YAML controls
settings; SQLite stores catalogue and work state; the media cache is evictable.

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
5. Verify a representation, save its index and exact size, then publish it promptly.
6. Request the first Plex scan immediately and coalesce subsequent requests.

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

A saved index can be rebound only when fresh sources establish the same identities.
A strong ETag is one source of evidence. The YouTube version path also checks the
signed object-version fields, exact size, and fresh beginning, middle, and end
range fingerprints. Sampled fingerprints are not a complete-file hash. Missing
or changed evidence prevents reuse and creates a new namespace.

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
