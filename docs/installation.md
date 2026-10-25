# Install duplex

This guide installs one household instance on a Linux Docker host. It then
connects a dedicated Plex TV library. Use Linux amd64 or arm64. The mount requires
`/dev/fuse`; Plex itself does not need access to that device.

## Prepare the host

Check free storage for the media cache, durable indexes, artwork, Docker images,
Plex configuration, and transcodes. These are separate storage budgets. A 50 GiB
cache limit does not provide 50 GiB of physical space.

From the repository on the Linux Docker daemon host:

```sh
sudo ./scripts/prepare-host.sh /var/lib/duplex
sudo ./scripts/check-host.sh /var/lib/duplex
```

The checks must report shared mount propagation and a usable FUSE device. The
preparation script refuses a nonempty, unmarked directory. Repeat the preparation
and checks after a host reboot unless host startup already restores the shared
bind mount. Do not substitute a macOS directory for this Linux path.

## Initialize and start

The production definition is [compose.yaml](../compose.yaml) at the repository root.
Qualification fixtures are separate.

```sh
docker compose build
docker compose run --rm --no-deps duplex-mount init \
  --config /state/config.yaml --state-dir /state --listen 0.0.0.0:8765
docker compose up -d --wait duplex-mount
curl --fail http://127.0.0.1:8765/mountz
```

The last command prints `mount ready`. Initialization creates settings and one
administrator credential. It refuses to replace an existing installation.

Read the initial password locally:

```sh
sudo cat /var/lib/duplex/data/secrets/initial-admin-password
```

Open `http://localhost:8765`, then sign in as `admin`. The host port is bound to
loopback. Keep the password file private; do not put its contents in YAML or logs.

| Purpose | Container path |
| --- | --- |
| Settings | `/state/config.yaml` |
| Catalogue, credentials, and durable artwork | `/state/data` |
| Packaging indexes | `/state/indexes` |
| Media chunks | `/state/media` |
| Stable public parent | `/state/library` |
| FUSE projection | `/state/library/media` |

To use another dedicated host directory or port, set `DUPLEX_HOST_DIR` and
`DUPLEX_PORT` before each Compose command, or put them in a protected local `.env`.
These settings choose locations and ports; credentials remain in protected files.

## Plex library

The supplied optional Plex service mounts the stable parent read-only with
`rslave` propagation:

```sh
docker compose --profile plex up -d plex
```

Open `http://localhost:32499/web/` and claim that isolated server. Keep remote
access disabled. If **Plex NFO Series** is absent after claim, restart Plex and
check again. duplex checks the actual agent; it does not infer support from the
server version.

For an existing Plex container, give it a read-only bind of the stable
`/var/lib/duplex/library` parent with `rslave` propagation. Select the mounted
`media` child as the library folder. Do not bind the FUSE root directly: an old
root bind can remain disconnected after remount recovery.

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

Save your server token in `/state/data/secrets/plex-token` with mode `0600` through
a trusted local administration path. Keep it out of the UI, YAML, shell arguments,
and chat. Configure the connection in duplex Settings; use the advanced editor
for the token file path:

```yaml
plex:
  url: http://plex:32400
  section: "1"
  token_file: /state/data/secrets/plex-token
```

Replace `"1"` with the dedicated library's actual section ID. For another Plex
server, use its reachable origin. The token is sent in a header. Diagnostics
reports a missing agent, inaccessible token file, or incorrect library setting.
Scans require mount readiness and the library prerequisites.

Follow the [first channel and playback steps](../README.md#follow-a-channel-and-play-a-video).

## Colima on macOS

Keep FUSE, Plex, SQLite, indexes, and media chunks inside the Linux VM. Docker
bind paths belong to the daemon host, as described in the
[Docker bind-mount documentation](https://docs.docker.com/engine/storage/bind-mounts/).
Do not export the Linux FUSE mount back to macOS.

Use the existing Colima profile. If it is stopped, start it. Then run from the
repository on macOS:

```sh
colima start
colima ssh -- sudo sh -s -- /var/lib/duplex < scripts/prepare-host.sh
colima ssh -- sudo sh -s -- /var/lib/duplex < scripts/check-host.sh
docker --context colima compose build
docker --context colima compose run --rm --no-deps duplex-mount init \
  --config /state/config.yaml --state-dir /state --listen 0.0.0.0:8765
docker --context colima compose up -d --wait duplex-mount
curl --fail http://127.0.0.1:8765/mountz
```

Use `docker --context colima compose` for the remaining Compose commands. This
leaves other projects' Docker contexts unchanged. Check both the VM root disk and
the Docker data disk when storage is low.

## Native Linux with systemd

The native unit is separate from Docker. It uses a dedicated `duplex` account,
the FUSE device, and `CAP_SYS_ADMIN`. It must share the host mount namespace with
Plex. Do not add filesystem namespace isolation that hides the mount.

Build on Linux, then install the binary and pinned runtimes:

```sh
make build
sudo install -m 0755 bin/duplex /usr/local/bin/duplex
sudo ./scripts/bootstrap-linux.sh
```

The bootstrap script needs `curl`, `unzip`, `sha256sum`, and `install`. It downloads
checksum-pinned official yt-dlp and Deno bundles. The yt-dlp bundle includes its
matching EJS code; remote component downloads remain disabled. See the
[yt-dlp dependency contract](https://github.com/yt-dlp/yt-dlp#dependencies).

Create a non-login `duplex` system account using your distribution's account tool.
Keep private state separate from the public library path:

```sh
sudo install -d -m 0700 -o duplex -g duplex /etc/duplex /var/lib/duplex
sudo install -d -m 0755 -o duplex -g duplex /srv/duplex/library/media
sudo -u duplex /usr/local/bin/duplex init \
  --config /etc/duplex/config.yaml --state-dir /var/lib/duplex \
  --mountpoint /srv/duplex/library/media
sudo install -m 0644 deployment/duplex.service /etc/systemd/system/duplex.service
sudo systemctl daemon-reload
sudo systemctl enable --now duplex.service
systemctl is-active duplex.service
curl --fail http://127.0.0.1:8765/mountz
```

The expected results are `active` and `mount ready`. Plex's folder is
`/srv/duplex/library/media`. The public parents must be traversable by the Plex
account; the catalogue, cache, indexes, and secrets remain private.

The optional [Plex drop-in](../deployment/plex-duplex.conf) makes
`plexmediaserver.service` wait for mount readiness. Configure the Plex library as
above, using the native path and a reachable Plex origin.
