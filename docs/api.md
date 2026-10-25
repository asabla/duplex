# JSON API version 1

The JSON API and HTML forms use the same application services. Configuration,
catalogue, jobs, policies, artwork, and diagnostics require an administrator session.
Responses exclude tokens, passwords, source locators, and raw resolver output.

## Session and CSRF

1. GET `/api/v1/session` with a cookie jar. It returns `authenticated` and `csrf`.
2. POST `/api/v1/login` with JSON `username`, `password`, and `csrf`, retaining the
   sign-in CSRF cookie.
3. GET the session again for its session CSRF token.
4. Retain the session cookie and send `X-CSRF-Token` on mutations from the same origin.

A restart ends sessions. Cross-origin writes and invalid CSRF tokens are rejected.
Failed logins are rate limited. Do not send credentials in URLs.

## Endpoints

| Method and path | Response or mutation |
| --- | --- |
| GET `/api/v1/session` | Authentication state and CSRF token. |
| POST `/api/v1/login` | Sign in with the administrator credential. |
| GET `/api/v1/config` | Settings, revision, load time, and configuration error. |
| PUT `/api/v1/config` | Complete JSON `config` plus expected `revision`; validate and atomically replace YAML. |
| GET `/api/v1/channels` | Existing compatibility shape: `settings` and durable `channels`. |
| POST `/api/v1/channels` | Existing JSON `url`, numeric `limit`, and `revision`; new channels enable playlists. A zero limit uses 50. |
| DELETE `/api/v1/channels/{id}` | JSON `revision`; unfollow and retain identities. |
| POST `/api/v1/channels/{id}/sync` | Queue one enabled channel; deduplicate queued or running work. |
| PUT `/api/v1/policy` | Change one upload policy or playlist override. See below. |
| GET `/api/v1/videos` | Bounded, filtered video page. Defaults to the selected library. |
| POST `/api/v1/videos/{id}/prepare` | Prioritize a selected recording through the same preparation service as the HTML form; CSRF required. |
| GET `/api/v1/playlists` | Bounded playlist page with effective policy and inheritance. |
| GET `/api/v1/jobs/page` | Bounded discovery history page. |
| GET `/api/v1/jobs` | Compatibility array of the latest 100 jobs. |
| GET `/api/v1/summary` | Small `status` and `counts`; use this for polling. |
| GET `/api/v1/status` | Compatibility status including the complete video array; avoid frequent polling. |
| GET `/api/v1/diagnostics` | Protected summary and redacted stage measurements. |
| GET `/api/v1/artwork/seasons?channel={id}` | Bounded season titles and stable artwork scopes; accepts `q`, `page`, and `per_page`. |
| GET `/api/v1/artwork?channel={id}&scope={scope}` | Active reference and whether it is an override. |
| POST `/api/v1/artwork?channel={id}&scope={scope}&revision={revision}` | Raw JPEG or PNG body; validate, normalize, save, and update YAML. |
| DELETE `/api/v1/artwork?channel={id}&scope={scope}&revision={revision}` | Reset the selected show or season to automatic artwork. |
| POST `/logout` | End the session; CSRF required. |
| GET `/healthz` | Public process health. |
| GET `/mountz` | Public mount readiness; 503 when unavailable. |
| GET `/readyz` | Public application integration readiness. Per-video preparation is separate. |

## Read pages and filters

Pages use `page` and `per_page`. Supported sizes are 25, 50, and 100. Invalid sizes
use 25; out-of-range page numbers clamp to the result range. An empty result has
page 1 and no items. Filters and ordering are applied before the database page is
loaded. The page envelope contains `items`, `page`, `per_page`, and `total`.

| Resource | Filters | Sort values |
| --- | --- | --- |
| Videos | `q`, `channel`, `playlist`, `lifecycle`, `preparation`, `inclusion` | `newest`, `oldest`, `title`, `title_desc`, `updated`, `updated_oldest` |
| Playlists | `q`, `channel`, `policy`, `failure`, `availability` | `title`, `title_desc`, `season`, `season_desc`, `updated`, `updated_oldest` |
| Jobs | `channel`, `state` | `newest`, `oldest` |

Video `inclusion` is `selected` by default, `all`, or `outside`. Lifecycle is
`discovered`, `recorded`, `replay-ready`, `upcoming`, `active`, `processing`, or
`unavailable`. Preparation is `discovered`, `queued`, `resolving`, `preparing`,
`ready`, `waiting`, `failed`, or `skipped`. Playlist `policy` filters the effective
`latest`, `all`, or `skip` policy. `failure=failed` selects failed refreshes.
Playlist `availability` is `available`, `missing`, or empty for both. Missing means
absent from the last successful complete root discovery; its identity and season
number remain durable. Artwork season pages include those retained seasons.

## Policy writes

Upload policy example:

```json
{
  "channel_id": "UCw7FkXsC00lH2v2yB5LQoYA",
  "playlist_id": "",
  "revision": "<revision from GET /api/v1/config>",
  "selection": {"mode": "all"},
  "include_playlists": true
}
```

A nonempty `playlist_id` writes an override. Its selection is `inherit`, `latest`
with a positive `limit`, `all`, or `skip`. Inherit removes the override. The
channel-wide playlist control still takes precedence.

## Artwork scopes

Use `show`, `year:2026`, or `playlist:<stable-playlist-id>`. The channel must be
followed and a requested season must exist. Uploads are limited to 10 MiB and
24 megapixels. A returned reference is a content hash, not an uploaded filename.
Assets are durable and independent of the media cache. Reset removes the YAML
reference; it does not delete a shared content-addressed asset.

## Errors and settings revisions

JSON bodies must contain one document and known fields. Configuration and artwork
writes require the expected disk revision. Stale JSON saves return 409; reload
before retrying. Invalid input returns 400. Session and authorization failures
return 401 or 403 as appropriate.

Valid external settings edits reload. Invalid edits retain the last valid
in-memory configuration and change the observed disk revision, so a stale save
cannot overwrite them. A corrupt or invalid file must be repaired before startup.
Storage, listener, mountpoint, and worker-count changes require a restart.
