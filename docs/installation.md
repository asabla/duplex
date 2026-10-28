# Install duplex

Use Linux Docker Engine 28 or later on amd64 or arm64, NFS client support in its kernel,
and Docker Compose v2. Docker performs the NFS client mount. Duplex runs as
UID/GID 10001 with all capabilities dropped, the default AppArmor/seccomp
policies, no FUSE device, and no Docker socket.

Docker Desktop and rootless Docker are not qualified deployment targets. Colima
can supply the Linux daemon on macOS; select it with `docker --context colima`.
No shared bind mount or custom AppArmor profile is needed.

## Initialize and start

Choose [compose.yaml](../compose.yaml) for Duplex alone or
[compose.plex.yaml](../compose.plex.yaml) to include Plex. These are complete
alternatives; do not combine them. Set the same `COMPOSE_FILE` and project name
for installation, upgrades, logs, and shutdown.

```sh
export COMPOSE_FILE=compose.yaml  # Or compose.plex.yaml.
docker compose pull
docker compose run --rm --no-deps duplex init \
  --config /state/config.yaml --state-dir /state --listen 0.0.0.0:8765
docker compose up -d --wait
docker compose exec duplex cat /state/data/secrets/initial-admin-password
```

Open [duplex](http://localhost:8765) and sign in as `admin`. Initialization refuses
to replace an existing configuration. The `duplex-state` named volume contains
settings, catalogue data, credentials, indexes, artwork, and the media cache.
Never remove it with `docker compose down --volumes` on an existing installation.

| Purpose | Container path |
| --- | --- |
| Settings | `/state/config.yaml` |
| Catalogue, credentials, artwork, and NFS identities | `/state/data` |
| Packaging indexes | `/state/indexes` |
| Evictable media chunks | `/state/media` |
| Plex library in the bundled example | `/data/media` |

The web and NFS host ports bind to localhost. Use `DUPLEX_PORT` or
`DUPLEX_NFS_PORT` to change them, for example if the host already has an NFS
server. See [.env.example](../.env.example). NFS exports only `/media`, through the
library provider. It does not export `/state` or accept filesystem writes.

NFS AUTH_SYS is not user authentication. Keep this endpoint on the trusted Docker
host; do not publish it to the LAN or internet. Local users on that host can read
the exported library. This is a file-access boundary, separate from the protected
administrator interface and Plex API token.
Keep Docker's normal NAT bridge isolation and direct routing disabled. Older
Docker releases have a [localhost port publishing limitation](https://docs.docker.com/engine/network/port-publishing/#publishing-ports).

## Connect from another machine

Forward only the web ports over SSH:

```sh
ssh -N -L 8765:127.0.0.1:8765 -L 32499:127.0.0.1:32499 user@server
```

Use your own login and configured web ports. Keep NFS on the Docker host.

## Plex library

Use your existing Plex server's web interface. For a container, follow the
[NFS volume setup](#use-an-existing-plex-server) below.

If you selected `compose.plex.yaml`, it starts Plex after duplex is healthy and
already mounts the media folder. Open [Plex](http://localhost:32499/web/) and claim
that server. Keep remote access disabled. If **Plex NFO Series** is absent after
claim, restart Plex and check again. duplex checks the actual agent; it does not
infer support from the server version.

Create a dedicated **TV Shows** library:

1. Select **Plex NFO Series** as the agent. Use Plex Media Server 1.43.1 or later.
2. Set the folder to `/data/media` for the supplied Compose service.
3. Enable **Use local assets** and **Use season titles**.
4. Disable automatic trash emptying. This preserves items and watch history
   through temporary source or mount failures.
5. Disable video preview thumbnails, intro detection, credits detection, and
   loudness analysis in this library. Disable optional deep analysis and chapter
   thumbnails if enabled on the server.
6. Put Plex transcode storage outside the duplex media cache.

### Connect duplex to Plex

This optional connection enables automatic scans and metadata updates. Plex can
read and play the mounted library without it. To scan manually, use **Scan Library
Files** in Plex after recordings are prepared.

Create a protected token file in the Duplex container:

```sh
docker compose exec -T duplex sh -c 'umask 077; cat > /state/data/secrets/plex-token'
```

Paste the Plex token, then press Ctrl-D. Keep only the token in this file.
See [Plex authentication](https://forums.plex.tv/t/authenticating-with-plex/609370).
This token permits Plex automation; NFS file delivery does not require it.

Find the dedicated library section ID with this read-only request:

```sh
docker compose exec -T duplex awk '{print "X-Plex-Token: " $0}' /state/data/secrets/plex-token |
  curl --fail --silent --show-error --header @- http://127.0.0.1:32499/library/sections
```

The matching XML Directory element's `key` is the section ID. For an existing
Plex server, use its reachable address instead of `127.0.0.1:32499`.

In duplex, open **Settings → Advanced configuration**. Update the existing `plex`
block, keep the other settings, and save:

```yaml
plex:
  url: http://plex:32400
  section: "1"
  token_file: /state/data/secrets/plex-token
```

Replace `"1"` with your section ID. `http://plex:32400` is the bundled Plex address
as seen by duplex; it differs from the host's web address. For another server,
use an origin reachable from inside the duplex container. Open **Diagnostics**
and check the Plex connection before following a channel.

### Use an existing Plex server

Start Duplex with `compose.yaml`. Add this volume to the existing Plex Compose
file, keeping the rest of that service's configuration:

```yaml
services:
  plex:
    volumes:
      - type: volume
        source: duplex-library
        target: /data/media
        read_only: true
        volume:
          nocopy: true

volumes:
  duplex-library:
    driver: local
    driver_opts:
      type: nfs
      device: :/media
      o: addr=127.0.0.1,port=2049,mountport=2049,nfsvers=3,tcp,mountproto=tcp,nolock,noacl,ro,hard,timeo=600,retrans=2,actimeo=1,lookupcache=positive
```

The NFS client runs on the Docker daemon host, so `127.0.0.1` refers to that Linux
host, not the Plex container. Use the published NFS port in both port options.
Start Duplex and wait for its health check before starting Plex. `nocopy` prevents
Docker from trying to populate the NFS volume during container creation.
The mount caches attributes for one second and does not cache missing names. This
keeps newly prepared recordings visible during later Plex scans.

Select `/data/media` as the Plex library folder. For a native Plex installation,
the host administrator can mount the same read-only NFS export through the normal
OS mount configuration. Plex itself needs no mount capability. A Plex server on
another machine requires a separately secured transport; these examples do not
expose NFS across machines.

Follow [Collection setup](collections.md) for selection rules and first playback.
See [Operations](operations.md#upgrade) before migrating an existing FUSE setup.

## Native Linux with systemd

The supplied [unit](../deployment/duplex.service) serves NFS on localhost as the
`duplex` account, without capabilities or device access. Install the built binary
and pinned runtimes, create that account, and give it private writable
`/var/lib/duplex` and `/etc/duplex` directories. Then initialize and start:

```sh
sudo -u duplex /usr/local/bin/duplex init \
  --config /etc/duplex/config.yaml --state-dir /var/lib/duplex
sudo install -m 0644 deployment/duplex.service /etc/systemd/system/duplex.service
sudo systemctl daemon-reload
sudo systemctl enable --now duplex.service
```

The host administrator mounts the library for Plex, independently of Duplex:

```sh
sudo mkdir -p /srv/duplex/library/media
sudo mount -t nfs -o ro,nfsvers=3,tcp,port=2049,mountport=2049,nolock,noacl,actimeo=1,lookupcache=positive \
  127.0.0.1:/media /srv/duplex/library/media
```

Use `/srv/duplex/library/media` in Plex. Configure normal OS mount ordering and
recovery for this mount. Preserve that path when migrating an existing library.
The older native FUSE command remains available for existing installations; it
is not used by the Docker examples or the supplied systemd unit.
