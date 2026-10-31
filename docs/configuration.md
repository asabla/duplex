# Configuration reference

`config.yaml` is the settings authority. `duplex init` creates it. The UI and JSON
API validate the same schema and replace the file atomically. Secrets are kept in
protected files outside YAML.

The Docker image serves NFS with `serve --nfs-listen 0.0.0.0:2049`; Compose binds
that port to the Docker host's loopback address. Native systemd uses
`--nfs-listen 127.0.0.1:2049`. This selects the transport at startup and leaves the
YAML storage, channel, and media settings unchanged. `mountpoint` applies only
when the legacy FUSE transport is selected.

## Service settings

| Field | Default | Meaning |
| --- | --- | --- |
| `version` | `1` | YAML schema version. |
| `listen` | `127.0.0.1:8765` | Administration listener. Compose initialization uses `0.0.0.0:8765` inside the container; the host port stays on loopback. |
| `data_dir` | `<state>/data` | SQLite, credentials, episode assets, and durable artwork. |
| `index_dir` | `<state>/indexes` | Packaging indexes and representation descriptions. |
| `cache_dir` | `<state>/media` | Evictable media chunks. |
| `mountpoint` | `<state>/library/media` | Legacy FUSE mount path; unused by the NFS export. |
| `cache_gib` | `50` | Media cache maximum in GiB, including unfinished reservations. |
| `refresh_minutes` | `30` | Scheduled channel refresh interval. |
| `workers` | `2` | Channel discovery workers, from 1 to 4. Media preparation has a separate limit of 2; metadata operations have a limit of 4. |
| `max_height` | `2160` | Maximum verified video height. Unsupported candidates fall back visibly. |
| `fetch_captions` | `false` | Fetch available captions for the original audio language during optional asset work. Off by default to reduce source rate limits. Stored captions remain available when disabled. |
| `plex.url` | empty | Plex origin, such as `http://plex:32400`. No embedded credentials, query, or path. |
| `plex.section` | empty | The dedicated NFO TV library's numeric section ID. |
| `plex.token_file` | empty | Protected token file accessible to duplex. The token itself does not belong in YAML. |

Storage directories must be absolute, dedicated paths. They cannot overlap.
Listener, storage, mountpoint, and worker-count changes require a restart. Other
valid settings reload. A cache reduction can fail while chunks are pinned; the
last applicable limit remains active and Diagnostics reports the failure.

Enable **Fetch captions** in Settings or set `fetch_captions: true` in YAML to
fetch captions. An omitted field keeps caption fetching off, including in existing
installations. Changes apply to subsequent optional asset work without a restart.
Enabling this setting does not start a caption download for every stored video.

## Channel and playlist policies

Use the [collection setup guide](collections.md) for worked selections and the
limits of playlist discovery, cache storage, and Plex history. This section
defines the YAML fields.

```yaml
channels:
  - id: UCaaaaaaaaaaaaaaaaaaaaaa
    url: https://www.youtube.com/channel/UCaaaaaaaaaaaaaaaaaaaaaa
    limit: 50
    enabled: true
    indexing:
      mode: latest
      limit: 50
    playlists:
      enabled: true
      overrides:
        PLexample_playlist_id:
          mode: skip
```

`indexing.mode` is `latest` or `all`. `latest` requires a positive `limit`. `all`
omits the count. A playlist override is `inherit`, `latest`, `all`, or `skip`.
An omitted override inherits the upload policy. Disabling `playlists.enabled`
skips all playlists, including their overrides.

Latest N selects eligible recordings by publication time. A selected playlist
is displayed in YouTube playlist order. Repeated references within a playlist
collapse to the first source occurrence. All has no numeric collection limit;
work remains queued in durable storage when discovery finishes.

Existing YAML with only `limit` keeps its numeric upload window and skips
playlists. Migration does not enable new collections without a settings edit.
Newly followed channels write explicit policies. The web form starts with
playlists disabled; select **Include this channel's playlists** to enable them.

### Separate content and individual recordings

Optional `content` rules split the channel's recording collection into uploads,
Shorts, and completed streams. Each rule accepts `latest` with a positive `limit`,
`all`, or `skip`. Without `content`, the existing shared `indexing` rule applies.
Playlist inheritance continues to use `indexing`; these content rules do not
change playlist selections.

```yaml
indexing:
  mode: latest
  limit: 20
content:
  uploads: {mode: latest, limit: 20}
  shorts: {mode: skip}
  replays: {mode: latest, limit: 2}
video_overrides:
  video000001: keep
  video000002: exclude
```

These are controlled example IDs. Replace them with the channel's actual video
IDs. `keep` adds an eligible recording beyond the automatic window, including a
skipped content type. A kept recording already inside the window appears once.
`exclude` removes a recording before the automatic window is counted, so the
next eligible recording fills its place. Remove an override to restore automatic
selection. Overrides affect only this channel's recording collection; playlists
retain their independent membership and history.

Shorts are identified from source tab membership, not duration or image shape.
Older catalogue records can have an unknown type until discovery reaches them.
Separate automatic rules omit unknown types; explicit Keep can still select an
eligible recording. A completed-broadcast state identifies a replay. No rule
makes an active, processing, unavailable, or unsupported recording playable.

The administration preview reads the last discovered catalogue and returns at
most 25 selected rows with complete counts. It performs no source or media work.
Saving applies changed rules to known records and queues discovery when enabled.
Discovery can find additional recordings. Successful enumeration reconciles its
window; a failed refresh retains the prior complete window and safe additions.
Applied rules survive restart without restoring withdrawn source entries.

`enabled: false` pauses a channel and removes it from the library projection.
Durable identities remain. `unlisted_videos` is an optional list of explicit
video IDs belonging to the followed channel; no source credentials are used.

## Artwork references

```yaml
artwork:
  poster: <64-lowercase-hex-digits>.jpg
  seasons:
    year:2026: <64-lowercase-hex-digits>.jpg
    playlist:PLexample_playlist_id: <64-lowercase-hex-digits>.jpg
```

This fragment belongs inside a channel entry. The references point to normalized
JPEG assets in `data_dir/artwork`; they are not filenames supplied by an upload.
Use the artwork page to create valid references. The show scope is independent
of titles. Playlist scopes use stable IDs; year scopes use the assigned year.
An override survives refreshes, reorder, renaming, and restarts. Reset removes
only the reference and restores automatic selection.

## External edits and stale saves

The service checks for external edits every two seconds. A valid edit reloads.
An invalid edit keeps the last valid in-memory settings and appears in Diagnostics.
Fix invalid YAML before restarting; startup requires a valid file.

Each UI/API settings save includes the revision read with that form. A changed
file causes HTTP 409 for JSON saves. Reload before retrying. Do not paste an old
complete YAML snapshot over new settings merely to clear a conflict.
