// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

// Package httpx is loom's real web-traffic engine, registered as app "http":
// a genuine HTTP/1.1 + TLS 1.3 (+ optional HTTP/2) client and the HTTPOrigin
// server that is loom's own far end for web and video flows (design
// real-app-traffic §2.10 — the loom-owned origin, not nginx).
//
// Both sides ride the injected netpath.Network — the client's http.Transport
// dials through Options.Network.DialContext and the origin listens through
// Options.Network.Listen — so the entire stdlib HTTP stack (connection pool,
// TLS, h2 framing) runs unchanged over the kernel stack, a datapath-backed
// network, or the in-memory test fabric. Nothing in this package calls
// net.Dial or net.Listen.
//
// # Client (app "http", client side)
//
// The client fetches objects in a loop bounded by the flow's duration (its
// context) or by an object count. Parameters (Options.Params):
//
//	url_path     fixed request path (e.g. "/object/65536"). When set, objects
//	             are fetched from exactly this path and object_size is unused.
//	objects      number of requests to issue; 0 (default) = unbounded, the
//	             flow duration is the only bound.
//	object_size  size distribution for generated "/object/{bytes}" paths when
//	             url_path is empty: loom's size grammar, scalar or range —
//	             "100KB", "8KB..512KB" (uniform). Default "100KB".
//	think        inter-request think time distribution: duration grammar,
//	             scalar or range — "0", "200ms..2s". Default 0.
//	tls          "true" = https. Default false.
//	h2           "true" = negotiate HTTP/2 via ALPN (requires tls; h2c is not
//	             supported). Default false.
//	host         Host header and TLS SNI/verification name, when it should
//	             differ from the dialed target (Options.Target).
//	tls_ca       base64 (std encoding) of PEM certificate(s) to pin as the
//	             client's root pool — normally the origin's self-signed cert,
//	             read from its CertificatePEM accessor and carried through
//	             params. Base64 was chosen over raw PEM or a file path so the
//	             value travels safely inside the wire params map with no
//	             shared filesystem. Certificate verification stays ON.
//	tls_insecure "true" disables certificate verification. EXPLICIT OPT-IN
//	             for lab shortcuts only — never the default path; prefer
//	             tls_ca pinning.
//
// Per-request timings come from net/http/httptrace: connect and TLS-handshake
// (new connections only — reused keep-alive connections contribute none),
// time-to-first-byte, and full-transfer time. The client aggregates them into
// metrics.HTTP (p50/p95/p99, window semantics like core/app/voip: Metrics
// closes an observation interval, CumulativeMetrics reads the whole run) and
// implements metrics.Source. Counters counts one "packet" per completed
// request and the response body bytes.
//
// # Server (app "http", server side — HTTPOrigin)
//
// HTTPOrigin serves deterministic synthetic content:
//
//	GET /object/{bytes}                   pseudo-random body of exactly
//	                                      {bytes} bytes, deterministic in
//	                                      (seed, path), Content-Length set
//	GET /media/{name}/manifest.m3u8       HLS master playlist generated from
//	                                      the ladder
//	GET /media/{name}/manifest.mpd        DASH MPD for the same ladder
//	GET /media/{name}/{kbps}/playlist.m3u8  HLS media playlist for one rung
//	GET /media/{name}/{kbps}/seg{n}       segment n: kbps·seg_duration/8
//	                                      bytes, deterministic body
//
// Parameters:
//
//	tls               "true" = serve TLS with a self-signed ECDSA P-256
//	                  certificate generated at build time. Default false.
//	h2                "true" = enable HTTP/2 over TLS (requires tls; plain
//	                  listeners stay HTTP/1.1 — h2c is not supported).
//	host              extra DNS (or IP) SAN for the generated certificate.
//	                  When the origin binds the unspecified address, the
//	                  host's interface IPs are added as IP SANs automatically
//	                  so tls_ca-pinned clients on other machines can dial the
//	                  origin by IP; set host on both sides when clients use a
//	                  name or an address not present at build time.
//	ladder            bitrate ladder, "label:rate" pairs: default
//	                  "240p:400k,480p:1200k,720p:2500k" (rates in loom's rate
//	                  grammar; the per-rung path segment is the rate in kbps).
//	seg_duration      segment duration (default 4s).
//	segments          segments per rendition playlist (default 150).
//	port_min,port_max inclusive bind range for firewall determinism, as in
//	                  core/app/voip; omitted = ephemeral.
//
// The generated certificate is exposed through the CertificatePEM() []byte
// accessor (optional capability discovered by assertion, like io.Closer), so
// an embedder or controller can hand it to clients as tls_ca and keep
// verification on end to end. Addr reports the bound address at build time
// (the data_port readback pattern), and Close releases the listener when a
// flow is torn down between Configure and Start.
package httpx
