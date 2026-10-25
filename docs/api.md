# API version 1

The JSON API and HTMX forms use the same application services. All catalogue,
configuration, job, and diagnostic routes require the administrator session.
Responses do not include resolver output, media locators, passwords, or tokens.

First GET `/api/v1/session` with a cookie jar. It returns `authenticated` and
`csrf`. POST `/api/v1/login` with JSON `username`, `password`, and `csrf`, retaining
the login CSRF cookie. GET the session again to obtain its session CSRF token.
Send the token as `X-CSRF-Token` on each mutation. Retain the session cookie and
use the same HTTP origin. Cross-origin mutations are rejected. A restart ends
all sessions; sign in again. Failed logins are rate limited.

| Method and path | Contract |
| --- | --- |
| GET `/api/v1/session` | Authentication state and CSRF token |
| POST `/api/v1/login` | Administrator sign-in |
| GET `/api/v1/config` | Current settings, disk revision, load time, validation error |
| PUT `/api/v1/config` | JSON `config` and `revision`; validate and atomically replace YAML |
| GET `/api/v1/channels` | Settings and durable channel sync state |
| POST `/api/v1/channels` | JSON `url`, `limit`, `revision`; resolve stable ID and follow |
| DELETE `/api/v1/channels/{id}` | JSON `revision`; unfollow, retain durable identities |
| POST `/api/v1/channels/{id}/sync` | Queue one refresh; deduplicate queued/running jobs |
| GET `/api/v1/jobs` | Latest 100 sync job results; full history is available in administration |
| GET `/api/v1/status` | Mount, source/Plex health, catalogue, cache, settings, jobs |
| GET `/api/v1/diagnostics` | Same protected diagnostic snapshot |
| POST `/logout` | End session; CSRF required |
| GET `/healthz` | Public process health |
| GET `/mountz` | Public mount readiness; 503 if absent |
| GET `/readyz` | Public application readiness; 503 while required media is restoring |

JSON bodies must contain one document and known fields. Stale settings mutations
return 409. Reload current configuration before retrying. The disk revision also
changes for invalid external edits so the user can repair them safely. A valid
external edit is loaded automatically. An invalid edit keeps the last valid
in-memory settings and exposes an error; an invalid file at startup must be fixed
before the service can start.

Paths, listener changes, and worker-count changes require a restart. Channel,
quality, schedule, Plex, and cache settings reload. A cache reduction can fail
while chunks are pinned; the prior active limit remains and status reports the
failure. No unbounded source retry loop runs after a failed refresh. Use a manual
refresh or wait for the configured refresh interval.
