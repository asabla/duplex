# duplex

Watch YouTube channels in Plex. Channels become shows, recordings become episodes,
and playlists become seasons. Media is fetched as you watch, with a shared cache
for playback and seeking.

- Recorded uploads, Shorts, and completed streams, up to 4K.
- Separate content limits, individual Keep/Exclude rules, and previews before saving.
- Original audio, available subtitles, and local artwork.
- A web interface to manage channels, recordings, and playlists.

**Experimental** · Linux amd64/arm64 · [MIT](LICENSE).
See [current limits](docs/validation.md) before relying on it for daily use.

## Quick start

Requires Linux with `/dev/fuse`, Docker Compose v2, and Git. Run these commands
on the Docker host. For macOS, use the [Colima guide](docs/installation.md#colima-on-macos).

This example uses [compose.plex.yaml](compose.plex.yaml) to start duplex with Plex:

```sh
git clone https://github.com/asabla/duplex.git
cd duplex
export COMPOSE_FILE=compose.plex.yaml

sudo ./scripts/prepare-host.sh /var/lib/duplex
sudo ./scripts/check-host.sh /var/lib/duplex
docker compose build
docker compose run --rm --no-deps duplex-mount init \
  --config /state/config.yaml --state-dir /state --listen 0.0.0.0:8765
docker compose up -d --wait
```

Open [duplex](http://localhost:8765) and sign in as `admin`. Read the initial
password on the host:

```sh
sudo cat /var/lib/duplex/data/secrets/initial-admin-password
```

For an existing Plex server, select [compose.yaml](compose.yaml) instead and follow the
[existing Plex setup](docs/installation.md#use-an-existing-plex-server).
For a remote machine, use the [SSH connection steps](docs/installation.md#connect-from-another-machine).

## Connect Plex

Open [Plex](http://localhost:32499/web/) and claim the server. Create a dedicated
**TV Shows** library with these settings:

| Setting | Value |
| --- | --- |
| Folder | `/data/media` |
| Agent | **Plex NFO Series** |
| Local assets and season titles | On |
| Automatic trash emptying | Off |
| Preview thumbnails, intro/credits detection, loudness analysis | Off |

Then [connect duplex to that library](docs/installation.md#connect-duplex-to-plex)
with its section ID and a protected token file. Plex requires version 1.43.1 or
later with the NFO agent available.

## Follow a channel and play a video

1. In duplex, open **Channels → Follow channel** and enter a public channel URL.
2. Choose **Latest videos**, start with a small count, and leave playlists off.
3. Wait for **Prepared** in Videos, then open the show in Plex and play an episode.

Use channel Settings for separate upload, Shorts, and stream rules. Add playlist
seasons after the first playback check. See [collection setup](docs/collections.md)
for selection behavior and [troubleshooting](docs/troubleshooting.md) if an item fails.

## Documentation

- [Installation](docs/installation.md) · [Collection setup](docs/collections.md)
- [Administration](docs/administration.md) · [Configuration](docs/configuration.md)
- [Operations and backups](docs/operations.md) · [Troubleshooting](docs/troubleshooting.md)
- [Development](docs/development.md) · [Architecture](docs/architecture.md) · [API](docs/api.md)
- [Performance](docs/performance.md) · [Validation](docs/validation.md)
- [Contributing](CONTRIBUTING.md) · [Security](SECURITY.md) · [Licensing](docs/licensing.md)

## License

[MIT](LICENSE). Bundled tools and assets retain their [own licenses](THIRD_PARTY_NOTICES.txt).
