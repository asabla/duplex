# Operate and recover duplex

Use the same Compose example and project for every command. Keep `COMPOSE_FILE`
set to `compose.yaml` or `compose.plex.yaml`, as selected during installation.
Do not switch examples or use a different project name to upgrade an installation.

## Change a collection policy

See [Configure a collection](collections.md) for initial setup and selection limits.

Open a channel's **Settings** to select **Latest videos** or **All videos**, or
to include or skip playlists. In **Playlists**, open **Actions** then
**Edit selection** to choose **Inherit**, **Latest videos**, **All videos**, or
**Skip playlist** for one collection. A channel-wide playlist skip takes precedence
over individual overrides.

Latest N uses publication time. Playlist episodes keep YouTube playlist order
after a complete refresh. A video shared by several playlists uses one canonical
media revision and cache data, with separate Plex progress for each occurrence.
Repeated references inside one playlist appear once.

Discovery and preparation are separate. A completed discovery job can still have
pending media tasks. Inspect **Videos** and open a recording for preparation
details. Use **Retry preparation** when it is available for a failed recording.
Unfollowing removes the channel from the projection and retains its identity records. Pausing retains its settings without publishing its media.

## Replace artwork

Open **Show and season artwork** from a channel, or **Artwork** from a playlist.
Search by playlist title or year, then choose the show or season before uploading.
The picker is paginated and retains the current scope across searches. JPEG and PNG inputs are limited to
10 MiB and 24 megapixels. duplex validates dimensions before decoding, removes
metadata by re-encoding, and fits the full image into a 2:3 poster.

A manual override takes precedence and survives renaming, refresh, reorder, and
restarts. **Reset to automatic** removes that override. Automatic show posters use
channel branding; playlist posters use collection artwork and the channel name;
year posters use channel branding and the year. Episodes keep their own video
thumbnails. Missing source imagery produces a labelled fallback; retrieval failure
retains the last valid automatic poster.

Artwork assets live in durable storage outside the media cache. Preserve them
with YAML in a backup. An artwork change requests a targeted Plex metadata
refresh. **Use local assets** and **Use season titles** must remain enabled.

## Storage and cache pressure

The media cache limit includes published chunks and unfinished reservations.
Concurrent requests share a fetch. Reads pin chunks until their copy completes;
eviction removes only unpinned data. The cache also checks physical free space
and leaves filesystem headroom. A full pinned cache or exhausted disk returns a
real error. It does not exceed the limit or manufacture missing bytes.

SQLite, packaging indexes, representation descriptions, artwork, subtitles, Plex
configuration, and transcodes are outside this limit. Monitor them separately.
The cache is evictable. Durable indexes and assets can grow with catalogue history.
Do not delete files from a running cache directory; it has a single-process lock
and its own recovery rules.

An open handle stays bound to one representation. Range responses with changed
content, incorrect lengths, or an expired locator fail that handle. A new locator
must preserve the verified object identity before its index can be reused. Without
strong identity evidence, a new resolution receives a new cache namespace.

## Refresh and restart

Scheduled refresh uses `refresh_minutes`; the default is 30. **Refresh** queues one
channel and deduplicates existing work. Discovery commits safe additions early.
Removals and playlist reorder wait for successful enumeration. A partial refresh
retains the prior selection and does not renumber year episodes.

On restart, the catalogue, identities, and verified indexes remain. Source leases
are re-established for new reads through bounded work. Mount readiness is separate
from individual video availability. Check preparation state as well as Diagnostics.
Keep automatic Plex trash emptying disabled during restoration.

Valid external YAML edits reload. Invalid edits retain the last valid settings
and appear in Diagnostics. Correct invalid YAML before restarting. Stale UI/API
saves are rejected; reload before retrying.

## Back up

Back up these together:

- `config.yaml`.
- `data`, including SQLite, secrets, artwork, and asset manifests.
- `indexes`.
- Plex configuration, including its claim and watch history.

The media cache and FUSE projection are not backup sources. Protect backups as
credentials. For a complete installation backup, stop Plex first, then duplex,
and use your protected host backup system to copy durable state. Keep the same
paths and file ownership when restoring.

For an **online SQLite-only** snapshot on Linux:

```sh
duplex backup --config /etc/duplex/config.yaml \
  --output /secure-backups/duplex-catalogue.sqlite
```

For Compose, choose a new destination inside the state bind:

```sh
docker compose exec duplex-mount \
  duplex backup --config /state/config.yaml --output /state/catalogue-backup.sqlite
```

The command refuses an existing destination, uses SQLite `VACUUM INTO`, sets mode
`0600`, and does not interrupt running jobs. This snapshot alone is not a complete
installation backup. Do not copy a live SQLite file without a transactional backup.

## Upgrade

1. Save a complete backup and record the current image or binary revision.
2. Keep existing YAML, data, indexes, secrets, and Plex configuration. Do not run
   initialization over them or replace settings with an example file.
3. Stop playback and allow active reads to finish.
4. Build or install a qualified replacement, then restart duplex.
5. Check `/mountz`, Diagnostics, and representative original playback and seeking.
6. Keep the backup until the upgraded installation passes its acceptance checks.

With Compose:

```sh
docker compose build duplex-mount
docker compose up -d --no-deps --wait duplex-mount
curl --fail http://127.0.0.1:8765/mountz
```

Catalogue migrations are transactional. Existing numeric upload limits and
excluded-playlist behavior remain until a policy is changed. Year paths, episode
identities, Plex bindings, and artwork overrides are retained. A rollback across
a schema migration requires the matching durable-state backup, not only an older
binary. Do not point an old binary at an unsupported schema.

## Recover a failed mount

A normal shutdown unmounts FUSE. After a process crash, duplex detaches only a
disconnected mount whose path, filesystem identity, and source match its own
configuration. It refuses a live or unrelated mount.

Keep Plex's read-only bind on the stable parent. Old handles fail; new opens use
the replacement mount. Correct a stale root bind before retrying playback. Never
empty Plex trash as a mount repair step. See [troubleshooting](troubleshooting.md).
