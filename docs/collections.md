# Configure a collection for Plex

## Choose the installation

Use one of the complete Compose examples from the repository root:

| File | Services | Use |
| --- | --- | --- |
| [compose.yaml](../compose.yaml) | duplex only | Connect an existing Plex server. |
| [compose.plex.yaml](../compose.plex.yaml) | duplex and Plex | Create a separate Plex installation for duplex. |

Select one file; do not combine them. Use that same `-f` option for build, init,
start, stop, logs, and upgrades. Both examples use Linux Docker Engine and its
NFS client support. See [Installation](installation.md).
The duplex-only example still requires Plex for browsing and playback.

An existing Plex server reads the library through a read-only NFS volume mounted
by Docker. Select `/data/media` in Plex. Duplex and Plex need no mount capability
or FUSE device. The examples keep NFS on the Docker host; they do not secure or
qualify access from another machine.

## Create the Plex library

A duplex collection is a channel's upload selection or one of its discovered
playlists. It is not a Plex **Collection** object. duplex publishes TV shows:

| Source | Plex representation |
| --- | --- |
| Followed channel | Show |
| Upload year | Year season |
| Discovered playlist | Named season under the playlist owner's show |
| Selected, verified recording | Episode |

Create a dedicated **TV Shows** library. Use **Plex NFO Series**, not an online
TV matching agent. Use Plex Media Server 1.43.1 or later, claim the server, and
confirm that the agent is available. The version alone does not prove support.
For the bundled Plex example, choose `/data/media` as the library folder.

Enable **Use local assets** and **Use season titles**. Disable automatic trash
emptying, video preview thumbnails, intro detection, credits detection, loudness
analysis, and optional deep analysis or chapter thumbnails. Media is fetched by
range when read. Extra analysis can cause large source reads and cache pressure;
a scan is not a full local download. Automatic trash emptying can remove history
while a source or mount is temporarily unavailable. duplex checks the required
library settings before managed scans.

Save the Plex token in a protected file accessible to duplex, with mode `0600`.
In duplex Settings, set the Plex origin, the dedicated library's section ID, and
the token file path. For the bundled example the origin is `http://plex:32400`.
For an existing server, use an origin reachable from the duplex container;
`127.0.0.1` inside that container refers to duplex itself. Never put the token
value in YAML or a Compose file. See the exact [Plex setup steps](installation.md#plex-library).

## Start with a small channel selection

1. In Channels, select **Follow channel** and enter a public YouTube channel URL.
2. Select **Latest videos**, set a small count such as `20`, and disable playlists
   for the first check. New-channel defaults otherwise include playlists.
3. Wait for discovery, then inspect Videos. Discovery completion does not mean
   every media file is prepared. Use **Retry preparation** when a failed item is
   eligible for a retry, and inspect Diagnostics before repeated attempts.
4. Wait for a recording to become prepared and for Plex to discover it. Test
   playback, a distant seek, and resume before expanding the selection.

These fields inside an existing channel entry select the latest 20 uploads:

```yaml
indexing:
  mode: latest
  limit: 20
playlists:
  enabled: false
```

Use the UI where possible. For external YAML edits, change the existing entry;
do not replace the complete settings file with this fragment. Invalid edits keep
the previous settings active until corrected, but can prevent the next startup.

## Separate content and keep individual recordings

In Channel Settings, open **Content rules and individual videos** to give
uploads, Shorts, and completed streams separate Latest, All, or Skip rules.
The shared rule remains the default for existing settings and inherited
playlists. These separate rules affect only the channel's recording collection.

Use Keep for an older recording or Exclude for one that should leave that
collection. The same controls are available on a video's details page. Preview
the known selected, added, and removed counts before saving. Preview does not
fetch source metadata or media; discovery can find more recordings. Unknown
content types are omitted by separate automatic rules until identified.

Keep does not create an archive, select active live content, or bypass source
availability and media verification. See the [configuration fields](configuration.md#separate-content-and-individual-recordings)
for complete examples and override behavior.

## Captions and metadata refresh

duplex selects available subtitles for the original audio language. Explicit
subtitles take priority over automatic captions. A regional audio tag can use
original captions for its base language, such as `en-US` with `en-orig`.

Optional captions can arrive after Plex's first scan. Changed assets queue a
targeted metadata refresh for each observed occurrence of that recording.
Unchanged assets do not queue another refresh. Pending work survives restarts;
failed attempts remain visible in Diagnostics and retry after fifteen minutes.
Plex must first discover the recording before its metadata can be refreshed.

To retry manually in Plex, open the episode's **More** menu and select
**Refresh Metadata**, then check its Subtitles list. **Analyse** alone does not
refresh a late subtitle.

## Add playlist seasons

Enable playlists for the channel, then refresh it. duplex discovers playlists
from that channel. It does not import an arbitrary playlist URL. In the channel's
Playlists view, set each discovered playlist to Inherit, Latest videos, All videos,
or Skip playlist. A playlist owner can differ from a video's uploader.

For example, these fields keep the latest 50 uploads, keep the latest 10 eligible
videos in one discovered playlist, and skip another. Replace both placeholder
keys with IDs from the discovered playlist list:

```yaml
indexing:
  mode: latest
  limit: 50
playlists:
  enabled: true
  overrides:
    REPLACE_WITH_PLAYLIST_ID:
      mode: latest
      limit: 10
    REPLACE_WITH_OTHER_PLAYLIST_ID:
      mode: skip
```

Unspecified playlists inherit the upload policy. Enabling playlists therefore
can add several seasons, each with its own selection. There is no separate
"skip all except these" default. Set Skip on unwanted playlists. Disabling
playlists skips all of them, including individual overrides.

Latest N chooses by publication time, then displays selected playlist entries in
YouTube playlist order. It does not mean the first N entries in that playlist.
An arbitrarily ordered playlist must be enumerated completely to choose the
latest N; a small selection does not bound discovery work. All has no numeric
selection limit and can create a large preparation queue. It does not pre-download
complete media files. Repeated references within one playlist appear once.

## Keep these limits in mind

- A video can appear in uploads and several playlist seasons. Its media data is
  shared, but each playlist occurrence has separate Plex identity and progress.
  Do not expect watching one occurrence to mark every occurrence watched.
- Playlist season numbers and year episode slots remain stable. Playlist reorder
  changes display positions after a complete refresh, while identity remains.
  Failed enumeration keeps the previous complete membership. Names are metadata;
  do not rename generated paths to change titles or ordering.
- Latest N is a selection rule, not a storage quota or an archive. Source removal,
  region restrictions, unsupported formats, and expired URLs can prevent playback.
  Authenticated sources and active live playback are outside this release scope.
- `cache_gib` limits media chunks, including unfinished reservations. It does not
  limit SQLite, indexes, artwork, subtitles, Docker images, Plex data, or transcodes.
  All of these need free storage. Do not delete files from an active cache.
- `max_height` is a ceiling, not a guarantee. A supported lower quality can be
  selected. Plex performs client conversion; conversion capacity depends on its
  host. Four simultaneous 4K conversions have not been qualified.
- Removing or pausing a channel changes the exposed library; durable identities
  remain. Keep Plex trash emptying disabled and back up YAML, durable duplex data,
  indexes, and Plex configuration together before large policy changes.

This setup is experimental. The examples do not establish sustained four-viewer
playback or TV/mobile compatibility. See [Validation](validation.md) for the open
release gates and [Operations](operations.md) for backup and recovery.
