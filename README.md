# duplex

Watch followed YouTube channels in Plex. A channel is a show, recorded videos are
episodes, and playlists are named seasons. Plex handles playback and conversion.

duplex runs on Linux. It keeps a catalogue in SQLite and exposes verified media
through a read-only FUSE mount. It fetches byte ranges for playback and seeking;
it does not prepare a complete video file first.

**Status:** the cleanup release is under qualification. The implementation and
its acceptance evidence are tracked separately in the [validation register](docs/validation.md).
Historical test results do not qualify the current build.

## Requirements

- Linux amd64 or arm64 with `/dev/fuse` and Docker Compose v2.
- Plex Media Server 1.43.1 or later, with the **Plex NFO Series** agent available.
  The server must be claimed; a version number alone does not prove agent support.
- Enough local storage for the media cache, catalogue, indexes, artwork, and Plex
  transcodes. The default media cache limit is 50 GiB; it does not reserve disk capacity.
- Network access to public YouTube content. YouTube sign-in is not used.

On macOS, run Docker, FUSE, Plex, and persistent storage inside Colima. Follow the
[Colima installation steps](docs/installation.md#colima-on-macos).

## Compose quick start

Run these commands from the repository on the **Linux Docker daemon host**:

```sh
sudo ./scripts/prepare-host.sh /var/lib/duplex
sudo ./scripts/check-host.sh /var/lib/duplex
docker compose build
docker compose run --rm --no-deps duplex-mount init \
  --config /state/config.yaml --state-dir /state --listen 0.0.0.0:8765
docker compose up -d --wait duplex-mount
docker compose ps
```

The mount service should become healthy. Open `http://localhost:8765`, sign in as
`admin`, and read the initial password from the protected file on the daemon host:

```sh
sudo cat /var/lib/duplex/data/secrets/initial-admin-password
```

Keep that password private. Initialization refuses to replace existing settings
or credentials. For an existing installation, use the [upgrade procedure](docs/operations.md#upgrade).

## Connect Plex

Use a dedicated TV library. Mount `/var/lib/duplex/library` into the Plex container
read-only, with `rslave` propagation, and select its `media` child as the library
folder. The optional Plex service in [compose.yaml](compose.yaml) already has this mount:

```sh
docker compose --profile plex up -d plex
```

In Plex, select **Plex NFO Series**, enable **Use local assets** and **Use season
titles**, and disable automatic trash emptying and optional thumbnail, intro,
credits, and loudness analysis. Keep remote access disabled for the household setup.
See [Plex setup](docs/installation.md#plex-library) for the token file, library
section, and claim procedure.

## Follow a channel and play a video

1. Open **Channels**, enter a YouTube channel URL, and select **Follow**.
2. Keep **Latest N**, `50`, and **Include playlists** for the defaults. Use **All**
   for the complete collection. Individual playlists can inherit, override, or skip.
3. Open **Videos**. Discovered entries and preparation state appear separately.
   **Prepare now** gives a selected video priority over background work.
4. Wait for **ready**. duplex publishes each verified recording and requests a
   Plex scan. Open its show in Plex Web and play the episode.

If a video does not become ready, inspect its preparation details and **Diagnostics**.
Use [troubleshooting](docs/troubleshooting.md) before repeating a failed operation.

## What is included

- Recorded uploads and completed broadcast replays, with verified quality up to 2160p.
- Stable year seasons and playlist seasons. Shared videos use the same media data,
  with separate Plex progress for each playlist occurrence.
- Original audio and available original-language sidecar subtitles.
- A compact administration panel with filters, sorting, pagination, and artwork controls.
- One writable YAML file. UI edits use revision checks; invalid external edits keep
  the last valid settings active.

Active live playback, authenticated sources, global YouTube search, explicit
playlist URL import, WebM packaging, rclone, native macOS FUSE, and TV/mobile
client qualification are outside this release. YouTube can remove or change a
source. An open handle keeps its representation and returns an error if that
representation can no longer be read.

## Documentation

- [Install](docs/installation.md): Linux, Colima, and Plex setup.
- [Operate](docs/operations.md): policies, artwork, storage, backups, upgrades, and recovery.
- [Administration panel](docs/administration.md): channels, filtering, preparation failures, policies, and artwork.
- [Configuration](docs/configuration.md) and [JSON API](docs/api.md): field and endpoint references.
- [Troubleshooting](docs/troubleshooting.md): symptoms and corrective actions.
- [Architecture](docs/architecture.md): identities, selection, packaging, and cache rules.
- [Development](docs/development.md): code standards and quality gates.
- [Performance evidence](docs/performance.md) and [validation](docs/validation.md).

## Development commands

The repository pins Go 1.27.3 and golangci-lint 2.15.0. FFmpeg and ffprobe are used
for controlled media tests. See [development setup](docs/development.md) for the
remaining pinned check tools.

```sh
make         # Show help; do not build, test, or change files.
make build   # Build bin/duplex.
make test    # Run race tests and controlled media tests.
make check   # Run the local quality gate.
```

There is no frontend build step. Templates, CSS, JavaScript, and HTMX are embedded
in the Go binary. Commits use [Conventional Commits](https://www.conventionalcommits.org/en/v1.0.0/).
