# duplex

YouTube channels in Plex. Channels become shows, recordings become episodes, and
playlists become seasons. Media is fetched as you watch and cached for reuse.

## Docker Compose

Run on Linux amd64 or arm64 with Docker Engine 28+, NFS client support, and Compose v2.
Use [compose.yaml](compose.yaml) for Duplex, or [compose.plex.yaml](compose.plex.yaml)
to include Plex.

Duplex serves a read-only NFS library as a non-root user with all capabilities
dropped. Docker mounts the library for Plex. No FUSE device, privileged helper,
or custom AppArmor profile is required.

GitHub Actions publishes [ghcr.io/asabla/duplex:latest](https://github.com/asabla/duplex/pkgs/container/duplex)
after checks pass on `main`. Initialize once, then start:

```sh
docker compose pull
docker compose run --rm --no-deps duplex init \
  --config /state/config.yaml --state-dir /state --listen 0.0.0.0:8765
docker compose up -d
```

Read the initial password:

```sh
docker compose exec duplex cat /state/data/secrets/initial-admin-password
```

Open [duplex](http://localhost:8765) and sign in as `admin`. State stays in a Docker
volume. The web and NFS ports bind to localhost.

[Connect your Plex library](docs/installation.md#use-an-existing-plex-server), then
open **Channels → Follow channel** in duplex. Choose recordings, preview the
selection, and save.

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
