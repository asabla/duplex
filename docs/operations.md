# Operation and recovery

## Settings and catalogue

YAML is the settings authority. UI and API saves validate a complete settings
object, compare its disk revision, write a temporary file, sync it, rename it,
and sync the parent directory. External edits are checked every two seconds.
Invalid edits keep the last valid in-memory settings. Fix invalid YAML before a
restart. The UI rejects stale saves and reports settings that need a restart.

Channel enumeration must finish before a new catalogue snapshot can commit.
Metadata and selected representations are staged; the transaction applies the
window after the complete enumeration. A failed enumeration or metadata request
retains the previous complete catalogue. Unavailable sources are explicit states.
Upcoming, active, and processing broadcasts have no exposed media file. Existing
YouTube identity records and assigned season/episode slots remain in SQLite even
when a recording leaves the window or a channel is unfollowed.

A new recording must have verified container details and stable range reads
before it is exposed. Index generation reads MP4 headers and fragment metadata;
long replays can require thousands of small requests. Source header requests have
bounded concurrency and pacing. Each candidate has a five-minute budget; channel
jobs have a two-hour budget. These are work limits, not playback latency promises.

On restart, durable metadata and exact file sizes remain available. Source
locators are resolved again in bounded background jobs. Media handles that need
restoration return a real error until a verified representation is restored.
`/readyz` returns 503 during this condition. Do not use that response as a reason
to delete Plex library items. `/mountz` checks the filesystem separately.

## Cache and representation rules

The configured limit covers media chunk data and unfinished-write reservations.
Fetches are deduplicated. A read pins its chunk; eviction only removes unpinned
published chunks. Before a fetch, reservations also check actual filesystem free
space, include unfinished writers, and evict cold chunks under physical pressure.
They leave up to 64 MiB of headroom (1% on small volumes) for filesystem and
durable-state writes. The configured cache size is a maximum; it does not create
physical capacity. Files are published by atomic rename after sync and include a
checksum. The cache directory has a single-process lock. Physical disk exhaustion
and a fully pinned cache return errors. They do not produce placeholder bytes.

Open handles keep one immutable representation. New signed URLs do not alter
that handle. Expired URLs, changed validators, incorrect lengths, ignored ranges,
and truncated responses fail. The next scheduled or manual refresh can establish
a replacement. Cached old chunks can remain usable until eviction, but their
identity is never reused for an incompatible representation. Without a strong
upstream validator, data is not reused across source resolutions.

Packaging indexes, representation descriptions, SQLite, local artwork/subtitles,
and Plex transcodes are separate from the media cache. Durable indexes and assets
can grow with catalogue history; monitor those directories separately. Do not
remove packaging indexes while a release qualification or investigation needs
their evidence. Plex may read ahead or analyse a file, so bytes fetched after
playback begins can exceed the selected segment duration. Disable optional Plex
background analysis to reduce this traffic. The scan health check requires the
dedicated library thumbnail, intro, credits, and loudness flags to be disabled.

## Backup

Protect backups as credentials. Stop duplex for a consistent backup of YAML,
SQLite, secrets, asset manifests, and packaging indexes as a single installation.
The cache is evictable and does not need a backup. Back up Plex's configuration
volume separately with Plex stopped; it contains watch history and account data.
Do not back up the FUSE projection itself.

For an online SQLite-only snapshot, write a new destination:

```sh
duplex backup --config /etc/duplex/config.yaml \
  --output /secure-backups/duplex-catalogue.sqlite
```

For Docker, choose a new file inside the mounted state directory:

```sh
docker compose -f deployment/compose.yaml exec duplex-mount \
  duplex backup --config /state/config.yaml --output /state/catalogue-backup.sqlite
```

The command uses SQLite `VACUUM INTO`, does not mark running jobs as interrupted,
and sets the output file to mode `0600`. A database-only copy is not a complete
installation backup. Do not copy a live SQLite file without its transactional
backup method. Restore settings, secrets, data, and indexes while duplex is
stopped, using the same configured paths and file permissions. Start the mount,
check readiness, then start Plex. A restart ends administrator sessions.

## Restart and failure handling

Keep the public parent bind mounted into Plex with `rslave` propagation and the
FUSE child under it. A normal shutdown unmounts FUSE. After a killed process,
duplex detaches only a disconnected mount whose exact path, filesystem type
`fuse.duplex`, and source `duplex` match its configured mount. It refuses a live
mount or any other mounted filesystem. Old open handles fail; new opens use the
replacement representation. Automatic Plex trash emptying must remain disabled.

If readiness remains false, inspect Diagnostics and recent job errors. Check
source connectivity and the pinned tool versions. Request a manual channel
refresh after resolving the failure. A failed refresh keeps the prior complete
catalogue; continuous immediate retries are not used.

Check both state storage and Docker/Plex storage with `df`. The qualification
found that a full Docker disk caused Plex item requests to return HTTP 500 and
stopped its logs, although the duplex cache disk had free space. Reclaim only
identified disposable data. Do not use a global volume or image prune command on
a shared Docker host. Check Plex health and replay/resume after storage recovery.

For native Linux, inspect `journalctl -u duplex` and service readiness. The daemon
logs do not include resolver output or signed media URLs. Full diagnostics require
administrator authentication. Keep raw resolver JSON and Plex tokens out of bug
reports. The qualification `gate` and `fixture-http` commands are local test tools,
not production administration endpoints.

## Administration lists

Videos, followed channels, and refresh history use 25 rows per page by default.
Use the controls above or below each table to move between pages, or select 50
or 100 rows and press Apply. Video search remains active while changing pages.
Changing the search or row count starts at page 1. Channel polling retains the
current page and row count. A page beyond the current result set opens the last
available page, including after an unfollow or a narrower search.

The overview and Diagnostics can browse the complete stored refresh history.
The JSON status snapshot and `/api/v1/jobs` keep their existing latest-100
contract. This update changes the administration lists, not the JSON response
format.
