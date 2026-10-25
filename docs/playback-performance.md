# Plex, duplex, and YouTube performance

Measure the complete media path: Plex opens the file, duplex verifies or reuses
its source binding, YouTube supplies missing ranges, and Plex returns media to
the client. Catalogue query timings alone do not measure this path.

## Cache allocation failure found on 2026-10-25

An isolated Linux arm64 service followed one fixed public upload,
`bnBpJkC1AME`, at 2160p AV1 with AAC audio. Enumeration was fixed for the
fixture. Metadata, source leases, and media ranges came from live YouTube,
using the documented visionOS media-client profile. The cache limit stayed at
1 GiB throughout the comparison.

The first failing boundary was a cache write. A direct source read succeeded.
The same read through the cache failed with `cache allocation exceeds reservation`.
On the nearly full ext4 test filesystem, a 1,048,576-byte file used 1,052,672
allocated bytes. The extra 4 KiB belonged to extent metadata.

The old reservation allowed only rounded payload bytes. The write was rejected
after the source transfer. Preparation then tried another format and rebuilt
its index. At 272.64 seconds, five index builds had completed, 6,628 media
requests had run, and no verified file was available. The isolated run was
stopped and its failed evidence retained.

`317e509` reserves a bounded extent allowance before fetching, then charges the
observed physical allocation after writing. It releases unused allowance and
keeps the existing rejection for allocation above the reservation. Pinned data,
the configured limit, and the physical free-space guard remain enforced.

The fresh fixed service verified the first 4K candidate and published it to
Plex. The observed ready and Plex-discovery times were 56.82 and 56.88 seconds.
Quality checks overlapped part of that preparation run. These values are retained
observations, not a matched latency improvement estimate.

## Playback and restart observations

The client decoded video frames through Plex's HTTP media-part endpoint, a
credential-free localhost forwarding proxy, the FUSE mount, and live YouTube.
The source was 3,729.914 seconds long. These are individual probe observations.

| Workload | Observed elapsed time |
| --- | ---: |
| First client frame after preparation and Plex discovery | 0.39 s |
| First frame after a middle seek | 0.52 s |
| First frame after a seek near the end | 0.76 s |
| Repeated frames at the same three positions | 0.19–0.48 s |
| Four concurrent frame probes at different positions | 0.39–2.11 s |

The owned service was then restarted with its catalogue and indexes retained,
a new empty media cache directory, and the same 1 GiB limit. The mount became
ready in 0.40 seconds. The first decoded frame took 3.70 seconds. Warm repeated
frames took 0.18–0.48 seconds. Four concurrent probes all decoded successfully.
Frame hashes at matching positions stayed equal across the restart, and the
Plex playback offset stayed unchanged.

The restart used one saved-index load and no index build. Source resolution
used 2.409 seconds, track probing used 0.401 seconds, index loading used 0.028
seconds, and range verification used 0.129 seconds. These operation totals
help locate the delay; they are not independent phases to add to client time.

The reusable profiler also decoded two seconds of both video and audio at a
new seek position. It produced 119 video frames and 2.002 seconds of output
in 3.41 seconds. Its four-client run completed without proxy request errors.

The complete `make check` gate passed on isolated source matching the cache fix.
Regression tests cover extent allocation, reservation release, pinned data,
quota exhaustion, unexpected allocation, and integer bounds. The live probe
verified that the same filesystem now accepts and accounts for the extra block.
See the [sanitized result record](evidence/playback-cache-allocation.json).

## Repeat the client probes

Use a ready item in a dedicated Plex test library. The tool needs Python 3 and
the installed FFmpeg. Credentials are read from protected files and are never
included in decoder arguments or URLs.

```sh
./scripts/profile-playback.py \
  --plex-url http://127.0.0.1:32499 \
  --token-file /absolute/path/to/plex-token \
  --item ITEM_KEY \
  --clients 4 \
  --output "$PWD/.cache/playback-profile-new"
```

Replace `ITEM_KEY` with a numeric episode key. The output directory must be new
and absolute. Add `--duplex-url` and `--duplex-password-file` together to retain
source-operation counters, stage times, and cache allocation. The tool closes
the administrator session that it creates. Plex credentials and Duplex cookies
use separate clients; authenticated redirects cannot cross origins.

The default workload reads a frame at the beginning, middle, and near the end,
then repeats those reads. It also decodes two seconds of both audio and video;
`--av-seconds 0` skips that check. Concurrent frame probes run when `--clients`
is greater than one. Each proxy response has a 128 MiB limit, and each operation
has a bounded timeout. Limits and failures remain visible in `result.json`.

Keep raw reports and credentials outside Git. To measure an empty-cache restart,
stop only the owned test service and select a new cache directory while retaining
the catalogue and indexes. Do not remove files from an active cache. Record
the initial chunk count and the index-load/build counters with the result.

## Remaining measurements

- First-time index construction still accounts for most preparation time. Its
  fragment-header requests have explicit pacing. Measure alternatives against
  request count, transferred bytes, exact output, and provider failures.
- After restart, fresh source resolution accounts for most first-open time.
  Bounded preparation of likely next recordings is a candidate for measurement.
- These short decoder probes do not qualify sustained four-viewer playback,
  browser rendering, Plex conversion startup, or the completed-replay gate.
  The built-in Browser returned HTTP 400 for the local Plex page in this run;
  browser first-frame timing remains unverified.

No codec selection, retry count, mount setting, or cache limit was changed.
