# Installation

Use Linux on amd64 or arm64. On macOS, run Docker, duplex FUSE, and Plex inside
Colima. Store SQLite, packaging indexes, and cache on Linux-local storage. Docker
bind sources belong to the daemon host, not the client computer:
[Docker bind mounts](https://docs.docker.com/engine/storage/bind-mounts/).

## Docker on Linux

Run the following commands from this repository. Install Docker Compose and enable
`/dev/fuse` on the Linux host. Check free space for the media cache, durable data,
Docker images, Plex database, and Plex transcode storage separately. The 50 GiB
cache limit does not reserve that capacity in advance.

```sh
sudo scripts/prepare-host.sh /var/lib/duplex
docker compose build
docker compose run --rm --no-deps duplex-mount \
  init --config /state/config.yaml --state-dir /state --listen 0.0.0.0:8765
docker compose up -d duplex-mount
curl --fail http://127.0.0.1:8765/mountz
docker compose exec duplex-mount \
  cat /state/data/secrets/initial-admin-password
```

Open `http://localhost:8765/`. Sign in as `admin` with the generated password. The
password is generated once. Protect the file; it is not written to application
logs. Do not put passwords, Plex tokens, or signed media URLs in YAML.

The default paths inside the service are:

| Purpose | Path |
| --- | --- |
| Editable settings | `/state/config.yaml` |
| Durable catalogue and credentials | `/state/data` |
| Packaging indexes and representation descriptions | `/state/indexes` |
| Evictable source chunks | `/state/media` |
| Stable public mount parent | `/state/library` |
| Replaceable FUSE mount | `/state/library/media` |

The Plex container receives **only the stable public parent**, read-only, with
`rslave` propagation. Its library location is `/data/media`. Do not bind the FUSE
mount root directly into Plex: such a bind can retain a disconnected old mount
after duplex restarts. Only the mount service has FUSE access and `SYS_ADMIN`.

Start the optional isolated Plex service after `/mountz` succeeds:

```sh
docker compose --profile plex up -d plex
```

Claim the server in Plex Web and keep remote access disabled for a local household
installation. Restart Plex after claim if the NFO agents do not appear. The tested
server required claim and restart before it exposed the installed NFO providers.
The reported version alone does not establish this capability.

Create a dedicated **TV Shows** library with **Plex NFO Series**, location
`/data/media`. Use Plex Media Server 1.43.1 or newer, and verify that this actual
agent is available. Disable automatic trash emptying. Disable video preview thumbnails, intro detection, credits detection, and loudness
analysis in the dedicated library advanced settings. Also disable optional deep
media analysis and chapter thumbnails on the isolated server. The per-library
settings are checked before scans; server-wide defaults alone are insufficient. Set Plex transcode
storage outside the duplex media cache.

Read a server token through a trusted local administration path. Save it in
`/state/data/secrets/plex-token`, mode `0600`; do not paste it into the duplex UI.
In duplex Settings, configure the origin, dedicated section number, and file path:

```yaml
plex:
  url: http://plex:32400
  section: "1"
  token_file: /state/data/secrets/plex-token
```

Use the actual section number. The service checks the server version, NFO agent,
library agent, mount, and automatic trash setting before it requests a scan.
Follow channel URLs in Channels. Configure per-channel limits, paused channels,
and explicit unlisted video IDs through the YAML editor.

## Colima

Use the existing Colima profile when one is available. Do not change the default
Docker context for other projects. Prepare VM-local storage inside the VM:

```sh
colima ssh -- sudo sh -s -- /var/lib/duplex < scripts/prepare-host.sh
docker --context colima compose build
```

Use `docker --context colima compose` for the remaining Docker commands above.
The service state path is the Linux path `/var/lib/duplex`, not a path below
`/Users`. Do not export the Linux FUSE mount to macOS. Check both the VM root disk
and the Docker data disk. A full Docker disk can break Plex even if the duplex
media-cache disk still has space.

## Native Linux service

The native service was exercised on Ubuntu 24.04.4 LTS in Colima. The official
Linux tool bundles require a compatible glibc runtime.

Build `bin/duplex-linux-amd64` or `bin/duplex-linux-arm64` with `GOOS=linux GOARCH=arm64 make build`.
Install the matching binary as `/usr/local/bin/duplex`. Run
`sudo scripts/bootstrap-linux.sh` (requires `curl`, `unzip`, `sha256sum`, and
`install`) to install the pinned official yt-dlp directory
bundle and Deno runtime. The bundle includes matching EJS challenge code; remote
component downloads are disabled. See [yt-dlp dependencies](https://github.com/yt-dlp/yt-dlp#dependencies).

Create a dedicated `duplex` system user, `/etc/duplex` owned by it with mode `0700`,
and `/var/lib/duplex` owned by it with mode `0711`. Create
`/var/lib/duplex/library` with mode `0755` so Plex can traverse the public projection.
The data, index, and cache directories remain private. Initialize as that user:

```sh
sudo -u duplex /usr/local/bin/duplex init \
  --config /etc/duplex/config.yaml --state-dir /var/lib/duplex
sudo install -m 0644 deployment/duplex.service /etc/systemd/system/duplex.service
sudo systemctl daemon-reload
sudo systemctl enable --now duplex.service
```

The dedicated Plex library location is `/var/lib/duplex/library/media`. Install
`deployment/plex-duplex.conf` as an optional `plexmediaserver.service` drop-in to
require mount readiness before Plex starts. Keep the host mount namespace;
filesystem namespace isolation can hide the FUSE mount from Plex. The service
uses the FUSE device and `CAP_SYS_ADMIN` to mount, with other capabilities removed.

For a Docker host, run `sudo scripts/check-host.sh /var/lib/duplex` for setup
checks. For a native service, check `systemctl is-active duplex`,
`curl --fail http://127.0.0.1:8765/mountz`, and the mounted filesystem with
`findmnt /var/lib/duplex/library/media`.
The native unit is supplied separately from the Docker qualification environment.
