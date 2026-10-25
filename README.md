# duplex

duplex exposes followed YouTube channels as Plex shows. Recorded uploads and
completed broadcast replays appear as episodes. Plex remains the player.

The service uses Go, SQLite, a read-only Linux FUSE filesystem, local NFO metadata,
and an HTMX administration interface. The default window is 50 eligible recordings
per channel, refreshed every 30 minutes. The media cache limit is 50 GiB. Selection
uses the highest verified MP4 representation up to 2160p. A lower selected quality
and source failures are visible in administration.

Separate audio and video tracks are presented as a deterministic virtual MP4.
Headers and sample tables map output offsets to upstream ranges. Playback and
seeking fetch source chunks as required. There is no full-file preparation path.
Finite combined MP4 files are also supported. Plex can convert media for a client.

Use [installation instructions](docs/installation.md),
[operation and backup instructions](docs/operations.md), and the
[versioned API contract](docs/api.md). The measured acceptance results and remaining
release boundaries are in [the validation register](docs/validation.md) and
[its JSON form](docs/validation.json).

## Build and verify

Go 1.27.3 is required. The module and `.tool-versions` pin dependencies. FFmpeg and
ffprobe are needed for controlled media tests, not for normal service operation.

```sh
make verify
make media-test
```

`make verify` runs race tests, vet, a local build, and Linux amd64/arm64 builds.
Linux FUSE and physical disk tests need their explicit opt-in environment variables
and isolated test containers. A skipped test does not establish compatibility.
See the validation register for exact versions, evidence, and real-source tests.

## Scope

Public and unlisted recordings are supported without YouTube credentials.
Channels use stable IDs and paths. Titles and backfill do not renumber existing
episodes. Complete refreshes use a transaction; a partial fetch retains the last
complete catalogue. Records remain durable when media leaves the exposed window.
Available original-language sidecar subtitles are optional.

WebM packaging, active live playback, authenticated sources, playlist import,
global YouTube search, the general provider route, rclone, native macOS FUSE, and
TV/mobile device qualification are deferred. Expired source URLs fail the current
handle. A later refresh resolves a new verified representation; an open handle
never switches to incompatible bytes. This is a private household installation.
Public distribution and official authorization claims require separate review.
