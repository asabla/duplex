# Use the duplex administration panel

## Follow a channel

Open Channels and select **Follow channel**. Enter the YouTube channel URL, choose
Latest videos or All videos, and choose whether to include its playlists. Latest
videos defaults to 50. All videos has no numeric selection field.

After the channel resolves, duplex opens its channel page. The overview shows
selected recordings, prepared files, and recordings that need attention. Discovery
progress and counts update while live updates are enabled. Videos can appear
before their media files are prepared.

Use the channel tabs to inspect Videos, Playlists, Activity, Artwork, and Settings.
**Back to results** returns to the originating filtered list and page. The global
navigation opens views across all channels.

## Inspect a blocked recording

Open Videos or select a channel's Needs attention count. Apply preparation,
YouTube-state, or playlist filters under **More filters**. Open a video title to
inspect its availability, failure reason, selection, and last verified quality.

The primary state describes preparation availability. YouTube lifecycle and
library selection remain separate facts. A previous quality verification does
not make a failed preparation ready. **Prepared** means that duplex verified a
media file; it does not certify Plex discovery or playback. The service-health
notice identifies integration failures that can still block playback.

When offered, **Retry preparation** moves the selected recording ahead of backfill.
Correct a reported source or service problem before repeated retries. **Back to
video results** retains the search, filters, sort, and page.

## Change selection or artwork

Channel Settings controls uploads and playlist inclusion. In Playlists, open a
row's Actions and select **Edit selection** to change Inherit, Latest videos,
All videos, or Skip playlist. The displayed policy distinguishes inheritance from
an override. Changes still pass through revision-checked YAML settings.

Successful saves replace only the submitted editor with its saved values and a
current revision. Other drafts remain intact. If settings changed elsewhere,
duplex rejects the stale save, keeps the draft, and offers the current page in a
separate tab for comparison. Field validation links to the affected input.

### Select uploads, Shorts, and completed streams

From Channel Settings, open **Content rules and individual videos**. Enable
separate limits to choose Latest, All, or Skip for each content type. When this
option is disabled, the existing shared channel selection applies. Playlist
policies remain separate and inherit the shared selection when configured.

Use **Find and change individual recordings** to search the discovered catalogue
by title, video ID, or recording URL. Each result shows its title, confirmed
content type, and draft rule. Choose **Keep**, **Exclude**, or **Automatic**. These
buttons update the draft; preview and save to apply it. Search returns ten
channel-owned recordings per page and makes no source requests.

Use **Keep** for a recording that must remain beyond the automatic window, or
**Exclude** to remove one from this channel's recording collection. The advanced
editor accepts one video ID or HTTPS YouTube recording URL per line. You can also
open a video and change its **Channel recording selection**.
Choose **Automatic** on that video to remove its override. These controls do not
archive media or bypass source availability and preparation requirements.

Select **Preview selection** before saving. The preview shows selected,
added, and removed counts from the last discovered catalogue. Select a review
list to inspect its titles, with at most 25 recordings per page. It does not
fetch source metadata or media. Unknown content
types are identified in the preview; discovery must classify them before separate
automatic rules can select them. Explicit Keep can select an eligible recording
whose type is unknown.

**Save selection** applies the rules to known recordings and queues discovery
when the channel is enabled. Discovery can find more recordings. A stale save
retains the draft and requires comparison with current settings. Recording paths,
slots, and media revisions are retained when membership changes.

An edit after preview requires another preview before saving. The server checks
this even without JavaScript. Counts for All, Skip, or disabled separate rules do
not affect validation. Field errors retain the draft and identify the input to
correct. A successful save confirms whether discovery was queued.

In Videos, **Not selected** means outside all active library selections. The
**Channel recording rule** filter distinguishes Keep and explicit Exclude rules.
Each row shows the confirmed content type and selection reason. An explicit
Exclude can still appear as selected through a playlist, whose rules are separate.

### Artwork and unfollow

Artwork opens in the current channel and poster scope. **Change show or season**
contains bounded season search and paging. Upload a JPEG or PNG, or select
**Reset to automatic**. The saved preview keeps the target and return context.
Manual overrides and image validation follow the existing artwork settings.

Unfollow is under Channel Settings. It describes the library effect and requires
a deliberate confirmation in the enhanced interface. Durable identity records
are retained. Keep Plex automatic trash emptying disabled to preserve history.

## Lists and keyboard controls

Search, filters, sorting, and pagination use URLs that can be bookmarked. The
upper pager gives compact navigation; the lower pager also controls page size.
With JavaScript enabled, changing page size submits the selection. Native forms
retain an Apply button when JavaScript is unavailable.

The enhanced playlist filter is one searchable combobox. Use the arrow keys to
move through matching options, Enter to select, and Escape to close. The server
loads at most 25 matching playlists and retains a selected item outside that
search page. A native select remains available without JavaScript.

Use **Pause live updates** to stop automatic progress changes. The choice persists
across navigation in the current browser tab. Background swaps also pause while
an affected region contains a draft, a focused control, or an open inspection.

## Design references and verification scope

The panel applies a small set of documented administrative patterns using Go
templates, embedded HTMX, semantic HTML, CSS, and plain JavaScript. It adds no
component library or frontend build.

- [PatternFly tables](https://www.patternfly.org/components/table/design-guidelines/),
  [toolbars](https://www.patternfly.org/components/toolbar/design-guidelines/),
  and [filter patterns](https://www.patternfly.org/patterns/filters/design-guidelines/)
  inform table controls and progressive disclosure.
- [Carbon data tables](https://www.carbondesignsystem.com/building-blocks/core/components/data-table/guidelines)
  inform consistent control and row density.
- [GOV.UK validation](https://design-system.service.gov.uk/patterns/validation/)
  informs error summaries, field messages, and preservation of entered values.
- [W3C guidance for updating content](https://www.w3.org/WAI/WCAG22/Understanding/pause-stop-hide.html)
  informs the live-update pause control.

Use handler tests, controlled rendered markup, and Browser/keyboard checks for
interface changes, as described in [Development](development.md). Controlled
preview data does not establish household Plex playback, device compatibility,
or complete accessibility conformance. Media acceptance uses the separate
[validation requirements](validation.md).
