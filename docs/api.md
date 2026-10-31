# JSON API version 1

The JSON API and HTML forms use the same application services. No sign-in is
required. Any client that can reach the web listener can read and change the
installation. Keep access within the configured trusted network boundary.
Responses exclude protected service tokens, source locators, and raw resolver output.
Status includes optional `measurements` and `counters` arrays. Counter items have
`name` and cumulative integer `value`; media counters reset with the process.
See [measurement fields](performance.md#current-measurement-fields) for scope and units.

## CSRF

1. GET `/api/v1/session` with a cookie jar. It returns `csrf` and sets a
   `duplex_csrf` cookie when needed. This endpoint retains its existing name;
   it does not create an authenticated session.
2. Retain that cookie and send the returned token in `X-CSRF-Token` on mutations
   from the same origin. HTML forms send the token in their `csrf` field.

GET requests do not require a cookie. Cross-origin writes, missing cookies, and
invalid CSRF tokens return HTTP 403. The cookie is HttpOnly and SameSite=Strict;
HTTPS also sets Secure. A restart does not invalidate the cookie. Login and
logout endpoints have been removed.

## Endpoints

| Method and path | Response or mutation |
| --- | --- |
| GET `/api/v1/session` | CSRF token for the requesting client's cookie. |
| GET `/api/v1/config` | Settings, revision, load time, and configuration error. |
| PUT `/api/v1/config` | Complete JSON `config` plus expected `revision`; validate and atomically replace YAML. |
| GET `/api/v1/channels` | Existing compatibility shape: `settings` and durable `channels`. |
| POST `/api/v1/channels` | Existing JSON `url`, numeric `limit`, and `revision`; new channels enable playlists. A zero limit uses 50. |
| DELETE `/api/v1/channels/{id}` | JSON `revision`; unfollow and retain identities. |
| POST `/api/v1/channels/{id}/sync` | Queue one enabled channel; deduplicate queued or running work. |
| PUT `/api/v1/policy` | Change one upload policy or playlist override. See below. |
| POST `/api/v1/selection/preview` | Preview proposed channel recording rules against known records, without source or media work. |
| POST `/api/v1/selection` | Save the same proposed rules using the expected configuration revision. |
| GET `/api/v1/videos` | Bounded, filtered video page. Defaults to the selected library. |
| POST `/api/v1/videos/{id}/prepare` | Prioritize a selected recording through the same preparation service as the HTML form; CSRF required. |
| GET `/api/v1/playlists` | Bounded playlist page with effective policy and inheritance. |
| GET `/api/v1/jobs/page` | Bounded discovery history page. |
| GET `/api/v1/jobs` | Compatibility array of the latest 100 jobs. |
| GET `/api/v1/summary` | Small `status` and `counts`; use this for polling. |
| GET `/api/v1/status` | Compatibility status including the complete video array; avoid frequent polling. |
| GET `/api/v1/diagnostics` | Summary and redacted stage measurements. |
| GET `/api/v1/artwork/seasons?channel={id}` | Bounded season titles and stable artwork scopes; accepts `q`, `page`, and `per_page`. |
| GET `/api/v1/artwork?channel={id}&scope={scope}` | Active reference and whether it is an override. |
| POST `/api/v1/artwork?channel={id}&scope={scope}&revision={revision}` | Raw JPEG or PNG body; validate, normalize, save, and update YAML. |
| DELETE `/api/v1/artwork?channel={id}&scope={scope}&revision={revision}` | Reset the selected show or season to automatic artwork. |
| GET `/healthz` | Public process health. |
| GET `/mountz` | Public filesystem readiness: NFS export listener or legacy FUSE mount; 503 when unavailable. Does not check Plex's client mount. |
| GET `/readyz` | Public application integration readiness. Per-video preparation is separate. |

## Read pages and filters

Pages use `page` and `per_page`. Supported sizes are 25, 50, and 100. Invalid sizes
use 25; out-of-range page numbers clamp to the result range. An empty result has
page 1 and no items. Filters and ordering are applied before the database page is
loaded. The page envelope contains `items`, `page`, `per_page`, and `total`.

| Resource | Filters | Sort values |
| --- | --- | --- |
| Videos | `q`, `channel`, `playlist`, `lifecycle`, `preparation`, `inclusion`, `rule` | `newest`, `oldest`, `title`, `title_desc`, `updated`, `updated_oldest` |
| Playlists | `q`, `channel`, `policy`, `failure`, `availability` | `title`, `title_desc`, `season`, `season_desc`, `updated`, `updated_oldest` |
| Jobs | `channel`, `state` | `newest`, `oldest` |

Video `inclusion` is `selected` by default, `all`, or `outside`. `outside` means
not selected by any active channel or playlist policy. Optional `rule=keep` or
`rule=exclude` filters explicit channel recording overrides from current YAML.
It combines with the other filters. An explicit Exclude can remain selected
through a playlist. Lifecycle is
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
  "channel_id": "UCaaaaaaaaaaaaaaaaaaaaaa",
  "playlist_id": "",
  "revision": "<revision from GET /api/v1/config>",
  "selection": {"mode": "all"},
  "include_playlists": true
}
```

A nonempty `playlist_id` writes an override. Its selection is `inherit`, `latest`
with a positive `limit`, `all`, or `skip`. Inherit removes the override. The
channel-wide playlist control still takes precedence.

### Recording rule preview and save

Both selection endpoints accept the same complete recording-rule draft:

```json
{
  "channel_id": "UCaaaaaaaaaaaaaaaaaaaaaa",
  "revision": "<revision from GET /api/v1/config>",
  "selection": {"mode": "latest", "limit": 20},
  "content": {
    "uploads": {"mode": "latest", "limit": 20},
    "shorts": {"mode": "skip"},
    "replays": {"mode": "latest", "limit": 2}
  },
  "video_overrides": {"video000001": "keep", "video000002": "exclude"}
}
```

The IDs above are controlled examples. `selection` retains the shared default,
including playlist inheritance. A null or omitted `content` restores the shared
recording rule. Each separate rule is Latest, All, or Skip. `video_overrides`
replaces the channel's recording overrides; null or an empty object clears them.
These endpoints preserve playlist policies and artwork settings.

The preview returns `known`, `unknown_kind`, `selected`, `added`, and `removed`
counts plus `items`, capped at 25 recordings. Query parameters `view=selected`
(the default), `view=added`, or `view=removed` select the review list. `page`
selects a page; the response includes `view`, `page`, and `pages`. For example,
POST the same draft to `/api/v1/selection/preview?view=removed&page=2` to review
the second page of removals. Each item contains `id`,
`title`, `kind`, and an optional `override`. Content kinds are `upload`, `short`,
`replay`, or an empty string when unknown. Counts cover the last-discovered
completed recordings, not future source results. Preview does not save settings,
change membership, queue jobs, or fetch source metadata or media.

Save validates the same rules, applies changes to known recordings, and queues
enabled channel discovery. Source and preparation requirements still apply.
Stale previews and saves return HTTP 409. CSRF requirements are the
same as other mutations, including for the read-only POST preview.

## Artwork scopes

Use `show`, `year:2026`, or `playlist:<stable-playlist-id>`. The channel must be
followed and a requested season must exist. Uploads are limited to 10 MiB and
24 megapixels. A returned reference is a content hash, not an uploaded filename.
Assets are durable and independent of the media cache. Reset removes the YAML
reference; it does not delete a shared content-addressed asset.

## Errors and settings revisions

JSON bodies must contain one document and known fields. Configuration and artwork
writes require the expected disk revision. Stale JSON saves return 409; reload
before retrying. Invalid input returns 400. CSRF and origin failures return 403.

Valid external settings edits reload. Invalid edits retain the last valid
in-memory configuration and change the observed disk revision, so a stale save
cannot overwrite them. A corrupt or invalid file must be repaired before startup.
Storage, listener, mountpoint, and worker-count changes require a restart.
