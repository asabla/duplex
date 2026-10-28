# duplex

YouTube channels in Plex. Channels become shows, recordings become episodes, and
playlists become seasons. Media is fetched as you watch and cached for reuse.

## Docker Compose

Run on Linux amd64 or arm64 with `/dev/fuse`, AppArmor, and Docker Compose v2.
Use [compose.yaml](compose.yaml) for Duplex, or [compose.plex.yaml](compose.plex.yaml)
to include Plex.

Duplex runs as a non-root user with all capabilities dropped. A separate helper
creates the read-only filesystem under a restricted AppArmor profile. It has no
network access or access to Duplex credentials and catalogue data.

GitHub Actions publishes [ghcr.io/asabla/duplex:latest](https://github.com/asabla/duplex/pkgs/container/duplex)
after checks pass on `main`. Complete the one-time
[host preparation](docs/installation.md#prepare-the-host) and
[initialization](docs/installation.md#initialize-and-start), then start the service:

```sh
docker compose pull
docker compose up -d
```

Open [duplex](http://localhost:8765), sign in as `admin`, and use the password from
`/var/lib/duplex/data/secrets/initial-admin-password` on the Docker host.

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
