# Security

This project is experimental. There is no supported stable release or guaranteed
response time. The administration panel is intended for a trusted local network;
keep it behind a loopback binding or an access-controlled private connection.

The Docker examples run Duplex as UID/GID 10001 with all capabilities dropped,
the default Docker AppArmor and seccomp profiles, and `no-new-privileges`.
The service has no FUSE device. A separate helper receives only `SYS_ADMIN` and
`/dev/fuse`, uses an enforced [AppArmor profile](deployment/duplex-mount.apparmor),
and has no network or access to service configuration, credentials, or catalogue
data. Its protocol accepts one fixed mount request from UID 10001. The helper
mounts only `/library/media` as read-only, `nosuid`, `nodev`, and `noexec`, then
passes the FUSE descriptor to Duplex. Filesystem reads stay in Duplex.

The helper retains a broad kernel capability, so it is still a trusted component.
The profile, separate mounts, fixed protocol, and network isolation restrict its
use. AppArmor support is required by these Compose examples; there is no automatic
unconfined fallback. The native systemd installation uses a separate direct-mount
model and retains `CAP_SYS_ADMIN` in the service.

Container CI checks enforced profiles, zero service capabilities, rejected
helper operations, read-only consumer propagation, service and helper crashes,
and packaged runtime startup. These checks do not establish protection against
kernel defects or replace the deployment requirements in [Validation](docs/validation.md).

Report suspected vulnerabilities privately through the repository hosting
service's private reporting feature when it is available. If no private channel
is configured, ask the maintainer to provide one without posting exploit details.
Do not put credentials, signed source URLs, private media, or installation state
in public issues.

Include the affected revision, platform, a short description of the impact, and
a minimal reproduction with synthetic data. Remove secrets and personal data
from logs. Keep the report private until a disclosure plan is agreed.

For installation and recovery procedures, see [Installation](docs/installation.md),
[Operations](docs/operations.md), and [Troubleshooting](docs/troubleshooting.md).
