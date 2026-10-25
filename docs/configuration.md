# Configuration reference

`config.yaml` is the settings authority. `duplex init` creates it. The UI and JSON
API validate the same schema and replace the file atomically. Secrets are kept in
protected files outside YAML.

## Service settings

| Field | Default | Meaning |
| --- | --- | --- |
| `version` | `1` | YAML schema version. |
| `listen` | `127.0.0.1:8765` | Administration listener. Compose initialization uses `0.0.0.0:8765` inside the container; the host port stays on loopback. |
| `data_dir` | `<state>/data` | SQLite, credentials, episode assets, and durable artwork. |
| `index_dir` | `<state>/indexes` | Packaging indexes and representation descriptions. |
| `cache_dir` | `<state>/media` | Evictable media chunks. |
| `mountpoint` | `<state>/library/media` | Read-only FUSE child. Bind its stable parent into Plex. |
| `cache_gib` | `50` | Media cache maximum in GiB, including unfinished reservations. |
| `refresh_minutes` | `30` | Scheduled channel refresh interval. |
| `workers` | `2` | Channel discovery workers, from 1 to 4. Media preparation has a separate limit of 2; metadata operations have a limit of 4. |
| `max_height` | `2160` | Maximum verified video height. Unsupported candidates fall back visibly. |
| `plex.url` | empty | Plex origin, such as `http://plex:32400`. No embedded credentials, query, or path. |
| `plex.section` | empty | The dedicated NFO TV library's numeric section ID. |
| `plex.token_file` | empty | Protected token file accessible to duplex. The token itself does not belong in YAML. |

Storage directories must be absolute, dedicated paths. They cannot overlap.
Listener, storage, mountpoint, and worker-count changes require a restart. Other
valid settings reload. A cache reduction can fail while chunks are pinned; the
last applicable limit remains active and Diagnostics reports the failure.

## Channel and playlist policies

```yaml
channels:
  - id: UCw7FkXsC00lH2v2yB5LQoYA
    url: https://www.youtube.com/channel/UCw7FkXsC00lH2v2yB5LQoYA
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
Newly followed channels write explicit policies and enable playlists.

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
