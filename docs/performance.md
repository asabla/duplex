# Performance evidence

The cleanup measurements below use an isolated Colima container. They do not
measure a native Linux installation. Raw profiles and source responses remain
outside Git because they can contain local data or signed source URLs.

## Baseline

The original sequential pipeline indexed two jackfrags recordings. A cold sync
took 111.987 seconds. Enumeration took 28.500 seconds, six metadata operations
used 12.872 seconds, and two preparations used 70.561 seconds. A 180-second CPU
profile recorded 2.20 seconds of Go CPU time. Most elapsed time was spent waiting.

A repeated instrumented run took 115.469 seconds. Building two container indexes
used 70.024 seconds; the longest used 51.227 seconds. Probing, verification, and
optional assets used 0.384, 0.382, and 0.126 seconds respectively. The old source
identity changed at every resolution, so saved indexes could not be rebound.

The household catalogue contained 5,806 durable video records. The status JSON
response was about 1.54 MB. Status took about 18–19 ms; a 539-byte progress fragment
still took about 12 ms because it loaded the complete catalogue. These are local
observations, not latency guarantees.

## Verified index reuse

A completed NASA replay (`k0ILMhbWHNQ`, formats 401 and 140) was resolved twice.
The test independently opened both leases, checked the provider version and
exact size, and fetched fresh beginning, middle, and end range fingerprints.
It then loaded the saved progressive MP4 index and checked representation identity,
exact size, and output ranges.

| Operation | Elapsed | Upstream requests |
| --- | ---: | ---: |
| Cold progressive index | 246.445 s | 5,215 |
| Fresh verification and index rebind | 1.839 s | 11, including output verification |

The rebind test transferred 2,349,394 bytes from 7,132,272,976 bytes of source
tracks. The same test passed under the Go race detector. This proves the tested
index can be reused across renewed leases. It does not establish Plex startup
latency or complete the media qualification gate. Missing version evidence still
uses a fresh namespace. A changed size or fingerprint prevents index reuse.

## Rejected experiment

A lazy fragmented MP4 prototype passed controlled packet and decoded-audio
comparisons. With real 4K sources, clients read too many fragment headers while
seeking; one replay timed out. Adding a front index did not resolve the failure.
The prototype was removed. The product retains the progressive MP4 range mapper.

## Remaining comparison

The incremental pipeline, SQL read pages, separate preparation workers, Plex
publication, and complete playback workloads still require matched before/after
measurements. No release acceptance is claimed by the results above.
