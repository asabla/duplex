# Plex, duplex, and source playback measurement

Measure the complete media path: Plex opens the file, duplex verifies or reuses
its source binding, the provider supplies missing ranges, and Plex returns media
to the client. Catalogue query timings alone do not measure this path.

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

## Interpretation and remaining checks

The default frame probes and short audio/video decode show whether those reads
completed and whether repeated frame hashes match. Record the source quality,
duration, tool versions, initial cache state, and exact source revision with each
result. Keep preparation time separate from client decode time.

First-time index construction uses paced fragment-header requests. After a
restart, fresh source resolution and verification can dominate first-open work.
Measure any change against request count, transferred bytes, exact output,
provider errors, and total resources. Do not infer an improvement from latency
alone.

The cache reserves payload capacity and a bounded allowance for filesystem extent
metadata before a fetch. It then charges observed physical allocation and releases
unused capacity. Allocations above the reservation remain errors. Include cache
allocation and physical free space when diagnosing a failed read.

Short probes do not qualify sustained four-viewer playback, browser rendering,
Plex conversion startup, or completed-replay playback. Use the built-in Browser
for rendered playback checks and follow [validation](validation.md) for release
acceptance. Do not change codecs, retries, mount settings, or limits until the
first failing boundary is known.
