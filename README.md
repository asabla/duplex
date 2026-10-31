# duplex

Watch YouTube channels in Plex. Channels become shows, recordings become
episodes, and playlists become seasons. Media is fetched as you watch and
cached for playback and seeking.

- Select uploads, Shorts, and completed streams, up to 4K.
- Set recording limits, keep individual videos, and preview changes before saving.
- Use original audio and local artwork, with optional caption fetching.

## Docker Compose

Requires Linux amd64 or arm64, Docker Engine 28+, NFS client support, and Compose v2.
Save this service-only example as `compose.yaml`:

```yaml
services:
  duplex:
    image: ghcr.io/asabla/duplex:latest
    command: [serve, --config, /state/config.yaml, --nfs-listen, "0.0.0.0:2049"]
    user: "10001:10001"
    restart: unless-stopped
    init: true
    read_only: true
    tmpfs:
      - /tmp:size=256m,mode=1777
    cap_drop: [ALL]
    security_opt:
      - no-new-privileges:true
    ports:
      - 127.0.0.1:${DUPLEX_PORT:-8765}:8765
      - 127.0.0.1:${DUPLEX_NFS_PORT:-2049}:2049
    volumes:
      - duplex-state:/state
    stop_grace_period: 30s
    healthcheck:
      test: [CMD, /usr/local/bin/duplex, health]
      interval: 5s
      timeout: 6s
      retries: 12

volumes:
  duplex-state:
```

The same example is in [compose.yaml](compose.yaml). To include Plex, use the
complete [compose.plex.yaml](compose.plex.yaml) instead. Run
`export COMPOSE_FILE=compose.plex.yaml` before the commands below.

Initialize once, then start:

```sh
docker compose pull
docker compose run --rm --no-deps duplex init \
  --config /state/config.yaml --state-dir /state --listen 0.0.0.0:8765
docker compose up -d --wait
```

Read the initial password:

```sh
docker compose exec duplex cat /state/data/secrets/initial-admin-password
```

Open [duplex](http://localhost:8765) and sign in as `admin`. Settings and data stay
in the `duplex-state` volume. Both ports bind to localhost; use the
[remote connection steps](docs/installation.md#connect-from-another-machine)
for another machine.

Duplex runs without added capabilities, a FUSE device, or a custom AppArmor
profile. Docker mounts its read-only NFS library for Plex. GitHub Actions builds
and publishes the [image](https://github.com/asabla/duplex/pkgs/container/duplex).

## Connect Plex

[Mount the library](docs/installation.md#use-an-existing-plex-server) in your Plex
container. The combined example already mounts it at `/data/media`.
Create a dedicated **TV Shows** library with **Plex NFO Series** as its agent.
Enable local assets and season titles; disable automatic trash emptying and
optional media analysis. The [Plex setup guide](docs/installation.md#plex-library)
lists the exact settings and optional automatic scan connection.

## Follow a channel

1. Open **Channels → Follow channel** and enter a public YouTube channel URL.
2. Start with a small **Latest videos** selection. Add playlist seasons when needed.
3. Wait for **Prepared** in Videos, then open the show in Plex and play an episode.

Use recording selection to set separate rules for uploads, Shorts, and completed
streams, or to keep and exclude individual recordings. Preview before saving.
See [collection setup](docs/collections.md) for selection rules and limitations.
Active live streams and authenticated sources are outside the current scope.

## Documentation

- [Installation](docs/installation.md) · [Collection setup](docs/collections.md)
- [Administration](docs/administration.md) · [Configuration](docs/configuration.md)
- [Operations and backups](docs/operations.md) · [Troubleshooting](docs/troubleshooting.md)
- [Development](docs/development.md) · [Architecture](docs/architecture.md) · [API](docs/api.md)
- [Performance](docs/performance.md) · [Validation](docs/validation.md)
- [Contributing](CONTRIBUTING.md) · [Security](SECURITY.md) · [Licensing](docs/licensing.md)

Experimental. See [current limits](docs/validation.md) before relying on it for daily use.

## License

[MIT](LICENSE). Bundled tools and assets retain their [own licenses](THIRD_PARTY_NOTICES.txt).
