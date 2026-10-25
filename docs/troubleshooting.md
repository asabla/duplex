# Troubleshooting

Start with **Diagnostics**, then the affected video's preparation details or its
channel's discovery job. Source URLs, tokens, and resolver output are intentionally
absent from these views. Do not enable Plex trash emptying to resolve a failure.

## A channel takes a long time to appear

Check **Jobs** for a running discovery stage. The first request must resolve the
channel's stable ID. New entries can appear before media preparation finishes.
Latest N stops after its eligible window; All can continue processing a large
collection. Playlist Latest N must inspect the complete playlist because source
order is not publication order.

If discovery failed, the previous selection remains. Fix the reported source or
runtime issue, then use **Refresh** once. Immediate repeated requests do not make
a blocked upstream source faster.

## A video is visible in duplex but absent from Plex

Check these in order:

1. Set Videos to **Selected**. Outside-selection records are retained identities.
2. Check lifecycle. Upcoming, active, processing, and unavailable content has no
   verified playable recording.
3. Check preparation. Queued, resolving, and preparing entries are not ready yet.
   Use **Prepare now** to raise one selected recording's priority.
4. Check mount readiness and the Plex prerequisites in Diagnostics.
5. Check that Plex uses the FUSE `media` child through the stable read-only parent.

A failed preparation leaves its error visible. A previous verified revision can
remain usable during refresh. An unsupported representation is not replaced by a
zero-filled file or a complete-file download.

## NFO Series is missing

Use Plex Media Server 1.43.1 or later. Claim the isolated server, restart Plex,
and check the available agents again. The server must actually advertise
**Plex NFO Series**. A version number by itself is insufficient.

## Playlist seasons have numeric names

Enable **Use season titles** in the dedicated library's advanced settings. Keep
**Use local assets** enabled. Refresh the show's metadata after the correct
`tvshow.nfo` is visible. Do not rename the FUSE directories manually; season
numbers are durable identities and playlist titles are metadata.

## The poster is old

Open the show's or season's artwork page. A manual override takes precedence over
automatic composition. Use **Reset to automatic** only if that is the intended
change. A valid upload is normalized and saved before Plex metadata refresh is
requested.

Check **Use local assets** in Plex. Then refresh that show's metadata. Plex can
cache poster responses, so reopen the show after refresh. If upstream artwork
retrieval failed, duplex retains the last valid automatic poster. Missing source
imagery uses a labelled channel fallback.

## Playback or a distant seek fails

Check source availability, preparation state, and cache pressure. An expired or
changed source fails the bound handle; it cannot silently switch to different
bytes. A new verified revision needs a new open. Request preparation or a channel
refresh, then retry playback after the recording is ready.

Keep optional Plex background analysis disabled. It can read ahead extensively,
consume source traffic, or hold cache chunks while no viewer is watching. Test
Plex Web original playback and forced conversion separately when diagnosing a
client codec issue.

## The cache reports no reclaimable space

The configured limit includes unfinished reservations. Active reads pin chunks.
A full pinned cache returns an error instead of exceeding the limit. Stop idle
playback sessions or allow reads to finish, then retry.

Also check physical storage. The cache limit is a maximum, not reserved capacity.
Plex transcodes, durable packaging indexes, and Docker images use separate space.
On Colima, inspect both the VM root and Docker data disks. Do not remove unrelated
Docker volumes or run a global prune as a duplex recovery step.

## Settings cannot be saved

A stale form or API revision is rejected. Reload current settings before retrying.
For an invalid external YAML edit, correct the file while the last valid settings
remain active. Fix it before restarting. For a local file error, check ownership
and write access to the configuration directory, not only the file itself.

## The mount is disconnected after a crash

Restart duplex and check `/mountz`. The service detaches only a disconnected mount
at its exact configured path with the expected duplex filesystem identity. It
refuses a live or unrelated mount.

Plex must bind the stable parent with `rslave` propagation. A bind of the old FUSE
root can remain stale even after duplex has mounted a replacement. Correct that
bind and restart the affected Plex container while preserving its configuration
volume. Keep automatic trash emptying disabled throughout recovery.
