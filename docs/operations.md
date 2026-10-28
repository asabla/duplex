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
for up to four recent selected ready recordings are verified in the background.
This uses the existing two preparation workers and yields to requested playback.
Other retained recordings bind on demand. Warming moves source work before a
later open; it does not remove that work or guarantee immediate playback after
mount readiness. Check preparation state as well as Diagnostics.
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

The media cache and exported library are not backup sources. Protect backups as
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
docker compose exec duplex \
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
docker compose pull duplex
docker compose up -d --no-deps --wait duplex
curl --fail http://127.0.0.1:8765/mountz
```

`ghcr.io/asabla/duplex:latest` follows checked commits on `main`. For a fixed
deployment or rollback, use `ghcr.io/asabla/duplex:sha-<full-commit>` or the image
digest from the [publication workflow](../.github/workflows/ci.yml).
Keep the NFS port and the durable data directory unchanged across upgrades.

### Migrate from the FUSE Compose deployment

Keep a complete backup and the same Compose project name. Using the old Compose
file, stop Plex, Duplex, and its helper before changing the deployment. Remove
only the stopped old Duplex containers; retain all state and Plex volumes:

```sh
docker compose stop plex duplex-mount mount-helper
docker compose rm -f duplex-mount mount-helper
```

For a separate Plex installation, stop that server through its own service
manager instead of naming `plex` here. Then update the Compose examples and add
an override to retain the existing state bind, rather than creating an empty
named volume:

```yaml
services:
  duplex:
    volumes:
      - type: bind
        source: /var/lib/duplex
        target: /state
        bind:
          create_host_path: false
```

Use the actual Linux daemon-host state directory. Keep its existing configuration,
credentials, ownership (UID/GID 10001), catalogue, indexes, and media cache. Do not
initialize over it. Start with the new Compose file and this override. Replace
Plex's old library bind with the documented NFS volume, keeping `/data/media` as
the Plex library path and retaining its existing configuration volume.

No catalogue or occurrence identity migration is required. The NFS handle database
is created in the private data directory on first use. Validate playback and
saved Plex progress before retiring the old FUSE host setup. Keep the old backup
and deployment files for rollback. Do not remove volumes to repair a mount.

Catalogue migrations are transactional. Existing numeric upload limits and
excluded-playlist behavior remain until a policy is changed. Year paths, episode
identities, Plex bindings, and artwork overrides are retained. A rollback across
a schema migration requires the matching durable-state backup, not only an older
binary. Do not point an old binary at an unsupported schema.

## Recover a failed export

A hard NFS client mount waits while Duplex is unavailable. Restore Duplex at the
same host address and port, with the same data directory and `nfs.sqlite` identity
database. Unchanged file handles can then reconnect. A changed or unavailable
representation fails rather than returning different bytes through an old handle.

The `/mountz` endpoint reports the export listener, not the Plex client's mount.
Check both service health and access from the Plex container. Do not empty Plex
trash during recovery. If the endpoint or durable identity database changes,
stop Plex and recreate its NFS volume mount; retain the Plex configuration and
library path. See [Troubleshooting](troubleshooting.md).
