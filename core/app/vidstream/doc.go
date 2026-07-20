// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

// Package vidstream is loom's ABR video player model, registered as app
// "video" (design real-app-traffic §2.10). It is client-only: the far end is
// the "http" app's HTTPOrigin (core/app/httpx), whose generated HLS ladder —
// GET /media/{name}/manifest.m3u8, per-rung media playlists, and
// deterministic segments — this player drives for real over the injected
// netpath.Network, on the same shared transport as the "http" client
// (httpx.NewTransport), so TLS/h2 behave identically across both apps.
//
// # Player model
//
// The player fetches the master manifest, adopts its bitrate ladder as the
// truth (an optional ladder param only checks expectations), and downloads
// segments sequentially into a virtual buffer. A virtual playhead drains the
// buffer in real time once playback starts:
//
//   - startup: playback begins when the buffer first reaches start_threshold;
//     the time from Run start to that instant is StartupMs.
//   - steady state: the player fetches ahead until the buffer reaches
//     buffer_target, then paces (a fetch starts only when the buffer has
//     drained below the target).
//   - stall: the buffer hitting zero while playing pauses the playhead and
//     opens a stall; playback resumes when the buffer refills to
//     rebuffer_target (or the stream ends). Every stall is recorded as a
//     timestamped event for correlation with external timelines (handovers).
//   - end: after the last segment, the player plays out the remaining buffer
//     and Run returns; running dry at end of stream is completion, not a
//     stall.
//
// Two ABR policies choose the rendition before each segment fetch:
//
//   - "throughput" (default): an exponentially weighted moving average of the
//     measured per-segment throughput — maintained in the harmonic domain, so
//     a bandwidth collapse pulls the estimate down within a segment or two
//     regardless of how fast the link was before — picks the highest rung
//     whose bitrate fits within a safety fraction of the estimate.
//   - "buffer": buffer-level thresholds (BBA-style): lowest rung at or below
//     rebuffer_target, highest at buffer_target, linear in between.
//
// Neither policy applies hysteresis (no switch-up margin, dwell time, or
// band overlap). This is a deliberate model simplification: an estimate (or
// buffer level) hovering at a rung boundary can flip between adjacent rungs
// on consecutive segments, so RepSwitchesUp/Down count raw policy decisions
// — estimator noise included — and should not be read as a path-change
// indicator on their own; correlate them with StallEvents and throughput.
//
// Ladder switches are counted up/down; fetched-bitrate history feeds
// AvgBitrateKbps (duration-weighted).
//
// # Parameters (Options.Params)
//
//	url_name         media name on the origin: the manifest is fetched from
//	                 /media/{url_name}/manifest.m3u8. Default "stream".
//	ladder           optional expectation, "label:rate" pairs in the origin's
//	                 grammar (httpx.ParseLadder). The manifest remains the
//	                 truth: Run fails with ErrLadderMismatch when the
//	                 manifest's rung set (kbps values) differs.
//	seg_duration     override for the playlist's per-segment durations in the
//	                 playhead accounting; default: the EXTINF values.
//	start_threshold  buffered media required before playback starts.
//	                 Default 2s.
//	buffer_target    buffer level the player fetches ahead to. Default 12s.
//	rebuffer_target  buffer level at which a stalled player resumes.
//	                 Default 4s.
//	abr              "throughput" | "buffer". Default "throughput".
//	tls, h2, host, tls_ca, tls_insecure
//	                 transport passthrough, exactly the "http" client's
//	                 grammar (see core/app/httpx).
//
// # Metrics
//
// The player implements metrics.Source with metrics.Video snapshots under
// the package-wide window discipline: Metrics closes one observation
// interval (segment/stall/switch counts, stall time, rebuffer ratio —
// stall time over stall+play time, so an interval that is all stall reads
// 1.0 — and average bitrate over the interval; BufferMs is the current
// level and StartupMs the run's startup delay), while CumulativeMetrics
// covers the whole run and closes nothing. StallEvents lists completed
// stalls as rtp.Gap records — a stall still in progress contributes to
// Stalls and StallTimeMs immediately but its event is emitted in the
// interval where it ends. When Run exits (completion, cancellation, or the
// flow duration expiring), the playhead is frozen at that instant: a stall
// still open is closed there as its event, and later Metrics or
// CumulativeMetrics reads stay stable instead of accruing wall time after
// the run. Counters counts one "packet" per completed HTTP request
// (manifests, playlists, segments) with response-body bytes.
package vidstream
