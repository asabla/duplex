# Retained layout fuzz seeds

The two JSON files contain generated MP4 headers, source lengths, and range maps.
They contain no source URLs or media payload. They are test inputs, not local
installation state.

The sources were generated with FFmpeg 9.0.2: one second of a 16 by 16
`testsrc2` picture at ten frames per second, encoded with H.264, and one second
of a 440 Hz sine wave at 48 kHz, encoded with AAC. Both sources use `+faststart`.
The source identities are the fixed strings `fuzz-video` and `fuzz-audio`.

`legacy-layout.json` uses the original source chunks and packaging version 1.
`current-layout.json` uses the sample-aligned chunks produced by the current
mapper from the same sources. A test requires offline repacking of the legacy
seed to match the current header, identity, and size, and to split the coarse
chunks. This ensures the fuzz seeds reach sample-table conversion before
mutations start. Fuzzing recomputes the envelope checksum so mutations reach
structural validation.

`FuzzRepackTables` also decodes this legacy header and directly mutates sample
counts, timing, chunk offsets, sample sizes, and timescales. It requires accepted
results to have valid range maps and requires the input to remain unchanged.
