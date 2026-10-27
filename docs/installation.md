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

Choose one complete example at the repository root. [compose.yaml](../compose.yaml)
runs only duplex for an existing Plex installation. [compose.plex.yaml](../compose.plex.yaml)
runs duplex and a separate Plex server. Do not combine the two files.
Qualification fixtures are separate.

Set `COMPOSE_FILE` for your chosen example before using the commands below.
Compose reads this variable for each command. Keep it set for logs, upgrades,
and shutdown, or save it in your local `.env`. [.env.example](../.env.example)
contains non-secret settings for both examples.

```sh
export COMPOSE_FILE=compose.yaml  # Or compose.plex.yaml to include Plex.
docker compose build
docker compose run --rm --no-deps duplex-mount init \
  --config /state/config.yaml --state-dir /state --listen 0.0.0.0:8765
docker compose up -d --wait
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

## Connect from another machine

The web ports listen on localhost. From your own computer, forward them over SSH:

```sh
ssh -N -L 8765:127.0.0.1:8765 -L 32499:127.0.0.1:32499 user@server
```

Replace `user@server` with your SSH login. Keep the connection open, then use
[duplex](http://localhost:8765) and [Plex](http://localhost:32499/web/) in your
browser. The Plex forwarding rule applies to the bundled example. Use your
existing Plex address for a separate installation. Use your configured ports if
you changed them.

## Plex library

Use your existing Plex server's web interface. For a container, follow the
[shared mount setup](#use-an-existing-plex-server) below.

If you selected `compose.plex.yaml`, it starts Plex after duplex is healthy and
already mounts the media folder. Open [Plex](http://localhost:32499/web/) and claim
that server. Keep remote
access disabled. If **Plex NFO Series** is absent after claim, restart Plex and
check again. duplex checks the actual agent; it does not infer support from the
server version.

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

For a new installation, create a protected token file on the Docker host:

```sh
sudo install -m 0600 /dev/null /var/lib/duplex/data/secrets/plex-token
sudoedit /var/lib/duplex/data/secrets/plex-token
```

Paste your claimed Plex server's token into the file and save it. Keep only the
token value in this file. It appears inside duplex at
`/state/data/secrets/plex-token`. Keep the value out of YAML and shell arguments.
See [Plex authentication](https://forums.plex.tv/t/authenticating-with-plex/609370)
for token authorization.

Find the library section ID with this read-only request on the Docker host. The
token passes through standard input, not a command argument:

```sh
sudo awk '{print "X-Plex-Token: " $0}' /var/lib/duplex/data/secrets/plex-token |
  curl --fail --silent --show-error --header @- \
    http://127.0.0.1:32499/library/sections
```

Find your library's title in the XML response. Its `key` is the section ID; for
example, `<Directory key="1" title="Duplex" />` means section `1`. For an existing
Plex server, replace the URL with its address reachable from the Docker host.

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

Set `COMPOSE_FILE=compose.yaml` before the build, initialization, and start
commands. Keep that selection for later operations.

Plex must read the same Linux projection. For a Plex container, add this bind to
its Compose service:

```yaml
volumes:
  - type: bind
    source: /var/lib/duplex/library
    target: /data
    read_only: true
    bind:
      propagation: rslave
      create_host_path: false
```

Use `/data/media` as the library folder, then apply the library settings and
connection steps above. Bind the stable `library` parent so a replacement FUSE
mount remains visible. These examples do not configure network file sharing for
a Plex server on another machine.

Follow the [collection setup guide](collections.md) for channel and playlist
selection, Plex settings, and the current limitations. Then use the
[first playback steps](collections.md#start-with-a-small-channel-selection).

## Colima on macOS

Keep FUSE, Plex, SQLite, indexes, and media chunks inside the Linux VM. Docker
bind paths belong to the daemon host, as described in the
[Docker bind-mount documentation](https://docs.docker.com/engine/storage/bind-mounts/).
Do not export the Linux FUSE mount back to macOS.

Use the existing Colima profile. If it is stopped, start it. Then run from the
repository on macOS:

```sh
export COMPOSE_FILE=compose.yaml  # Or compose.plex.yaml to include Plex.
colima start
colima ssh -- sudo sh -s -- /var/lib/duplex < scripts/prepare-host.sh
colima ssh -- sudo sh -s -- /var/lib/duplex < scripts/check-host.sh
docker --context colima compose build
docker --context colima compose run --rm --no-deps duplex-mount init \
  --config /state/config.yaml --state-dir /state --listen 0.0.0.0:8765
docker --context colima compose up -d --wait
curl --fail http://127.0.0.1:8765/mountz
```

Use `docker --context colima compose` for the remaining Compose commands. This
leaves other projects' Docker contexts unchanged. Check both the VM root disk and
the Docker data disk when storage is low.

Run `colima ssh` before the password and token-file steps above. Those files are
inside the Linux VM; use the same `/var/lib/duplex` paths there.

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

For this native installation, set `plex.token_file` to
`/var/lib/duplex/data/secrets/plex-token`. After creating the token file, give the
service account ownership:

```sh
sudo chown duplex:duplex /var/lib/duplex/data/secrets/plex-token
```
