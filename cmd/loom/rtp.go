// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"

	"github.com/bgrewell/loom/core/app"
	"github.com/bgrewell/loom/core/app/voip"
	"github.com/bgrewell/loom/core/metrics"
	"github.com/bgrewell/loom/core/netpath"
	"github.com/bgrewell/loom/core/rtp/codec"
	"github.com/bgrewell/stencil"
)

// defaultCallDuration bounds a --call run when --duration is left at 0; an
// answerer with --duration 0 instead serves until interrupted, iperf-style.
const defaultCallDuration = 60 * time.Second

// rtpCommand builds the `loom rtp` subcommand: the iperf-esque two-host VoIP
// quick mode. It drives the same core/app/voip engine the agent runs as app
// "voip", with zero controller or agent involvement.
func rtpCommand() *stencil.Command {
	fs := stencil.NewFlagSet()
	fs.Bool("answer", "a", "answer mode: bind, print the address, latch onto the first caller", false)
	fs.String("call", "c", "call mode: the answerer's host:port", "")
	fs.Int("port", "p", "answer bind port (0 = ephemeral, preferring even)", 0)
	fs.Int("port-min", "", "lowest answer bind port (inclusive, with --port-max)", 0)
	fs.Int("port-max", "", "highest answer bind port (inclusive, with --port-min)", 0)
	fs.String("codec", "", "codec: g711|pcmu|pcma|g729|opus", "g711")
	fs.Duration("ptime", "", "packetization interval", 20*time.Millisecond)
	fs.Duration("jb", "", "jitter-buffer depth (fixed playout point)", 40*time.Millisecond)
	fs.Duration("duration", "d", "run duration (0 = 60s for --call, until Ctrl-C for --answer)", 0)
	fs.Duration("interval", "i", "report interval", time.Second)
	fs.Duration("handshake-timeout", "", "caller's wait for first return RTP/RTCP", voip.DefaultHandshakeTimeout)
	fs.Bool("json", "", "emit one JSON object per interval per end instead of human lines", false)

	return &stencil.Command{
		Name:    "rtp",
		Summary: "Run a two-host VoIP call and score it live (MOS/R, jitter, loss, RTT, OWD)",
		Long: "The two-host VoIP check, in the spirit of a throughput run: `loom rtp --answer` " +
			"on one host binds a media port and prints it; `loom rtp --call <host:port>` on the " +
			"other runs a bidirectional RTP/RTCP call against it. Both ends print per-interval " +
			"quality lines — MOS-CQ, R-factor, jitter, loss, jitter-buffer discards, RTT, and " +
			"one-way delay — and each folds the peer's MOS/R in from RTCP XR, so both ends are " +
			"visible from either terminal. Quick mode runs without TimeSync: one-way delay is " +
			"RTT/2 with a matching error bar, always labeled \"rtt/2\", never presented as " +
			"measured. The answerer latches onto the first source that passes RTP validity and " +
			"probation; everything else is counted as stray and dropped.",
		Flags: fs,
		Run:   runRTP,
	}
}

// rtpConfig is the validated flag set of one quick-mode run.
type rtpConfig struct {
	answer                 bool
	call                   string
	port, portMin, portMax int
	codec                  string
	ptime, jb              time.Duration
	duration, interval     time.Duration
	handshake              time.Duration
	jsonOut                bool
}

// runRTP is the command entrypoint: validate flags, then run under an
// interrupt-aware context so Ctrl-C ends the run through the summary path.
func runRTP(ctx *stencil.Context) error {
	cfg, err := rtpConfigFrom(ctx.Flags)
	if err != nil {
		return err
	}
	sigCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return rtpRun(sigCtx, cfg, ctx.App.IO.Out)
}

// rtpConfigFrom validates the resolved flags into an rtpConfig, rejecting
// contradictory mode and port selections up front — one clear message instead
// of a bind-time surprise.
func rtpConfigFrom(f *stencil.ResolvedFlags) (rtpConfig, error) {
	cfg := rtpConfig{
		answer:    f.Bool("answer"),
		call:      f.String("call"),
		port:      f.Int("port"),
		portMin:   f.Int("port-min"),
		portMax:   f.Int("port-max"),
		codec:     codecNameFor(f.String("codec")),
		ptime:     f.Duration("ptime"),
		jb:        f.Duration("jb"),
		duration:  f.Duration("duration"),
		interval:  f.Duration("interval"),
		handshake: f.Duration("handshake-timeout"),
		jsonOut:   f.Bool("json"),
	}
	if cfg.answer == (cfg.call != "") {
		return cfg, errors.New("rtp: exactly one of --answer or --call <host:port> is required")
	}
	if _, err := codec.ByName(cfg.codec); err != nil {
		return cfg, fmt.Errorf("rtp: unknown codec %q (g711, pcmu, pcma, g729, opus)", f.String("codec"))
	}
	if cfg.ptime <= 0 {
		return cfg, errors.New("rtp: --ptime must be positive")
	}
	if cfg.jb < 0 {
		return cfg, errors.New("rtp: --jb cannot be negative")
	}
	if cfg.duration < 0 {
		return cfg, errors.New("rtp: --duration cannot be negative")
	}
	if cfg.interval <= 0 {
		return cfg, errors.New("rtp: --interval must be positive")
	}
	if !cfg.answer {
		if cfg.port != 0 || cfg.portMin != 0 || cfg.portMax != 0 {
			return cfg, errors.New("rtp: --port/--port-min/--port-max apply to --answer (the caller binds ephemerally)")
		}
		if cfg.handshake <= 0 {
			return cfg, errors.New("rtp: --handshake-timeout must be positive")
		}
		if _, _, err := net.SplitHostPort(cfg.call); err != nil {
			return cfg, fmt.Errorf("rtp: --call wants host:port: %w", err)
		}
	}
	if cfg.port != 0 && (cfg.portMin != 0 || cfg.portMax != 0) {
		return cfg, errors.New("rtp: --port conflicts with --port-min/--port-max")
	}
	if (cfg.portMin == 0) != (cfg.portMax == 0) {
		return cfg, errors.New("rtp: --port-min and --port-max go together")
	}
	for _, p := range []int{cfg.port, cfg.portMin, cfg.portMax} {
		if p < 0 || p > 65535 {
			return cfg, fmt.Errorf("rtp: port %d out of range", p)
		}
	}
	if cfg.portMin > cfg.portMax {
		return cfg, fmt.Errorf("rtp: empty port range %d..%d", cfg.portMin, cfg.portMax)
	}
	return cfg, nil
}

// codecNameFor maps CLI-friendly codec spellings onto codec-table names:
// "g711" (and the u-law/A-law variants) are the usual ways to ask for what
// the table registers as pcmu/pcma.
func codecNameFor(name string) string {
	switch strings.ToLower(name) {
	case "g711", "g711u":
		return "pcmu"
	case "g711a":
		return "pcma"
	default:
		return strings.ToLower(name)
	}
}

// rtpRun builds the voip app for the selected mode over the host network and
// drives it: start line, per-interval quality lines, and an end-of-run
// summary. It is the testable core — ctx, config, and writer are injected.
func rtpRun(ctx context.Context, cfg rtpConfig, out io.Writer) error {
	params := map[string]string{
		"codec": cfg.codec,
		"ptime": cfg.ptime.String(),
		"jb_ms": strconv.Itoa(int(cfg.jb / time.Millisecond)),
	}
	opts := app.Options{Params: params, Network: netpath.Host(netip.Addr{})}
	em := &rtpEmitter{w: out, jsonOut: cfg.jsonOut}

	var runner interface{ Run(context.Context) error }
	duration := cfg.duration
	if cfg.answer {
		if cfg.port > 0 {
			cfg.portMin, cfg.portMax = cfg.port, cfg.port
		}
		if cfg.portMin > 0 {
			params["port_min"] = strconv.Itoa(cfg.portMin)
			params["port_max"] = strconv.Itoa(cfg.portMax)
		}
		srv, err := voip.NewServer(opts)
		if err != nil {
			return fmt.Errorf("rtp: %w", err)
		}
		em.startAnswer(srv.Addr(), cfg)
		runner = srv
	} else {
		target, err := resolveTarget(ctx, cfg.call)
		if err != nil {
			return err
		}
		opts.Target = target
		params["handshake_timeout_ms"] = strconv.Itoa(int(cfg.handshake / time.Millisecond))
		cli, err := voip.NewClient(opts)
		if err != nil {
			return fmt.Errorf("rtp: %w", err)
		}
		if duration == 0 {
			duration = defaultCallDuration
		}
		em.startCall(cfg.call, cfg, duration)
		runner = cli
	}
	src, ok := runner.(metrics.Source)
	if !ok {
		return errors.New("rtp: voip app exposes no metrics (internal)")
	}

	runCtx := ctx
	if duration > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, duration)
		defer cancel()
	}

	start := time.Now()
	done := make(chan struct{})
	var runErr error
	go func() {
		runErr = runner.Run(runCtx)
		close(done)
	}()

	ticker := time.NewTicker(cfg.interval)
	defer ticker.Stop()
loop:
	for {
		select {
		case <-done:
			break loop
		case now := <-ticker.C:
			v, _ := src.Metrics().(metrics.VoIP)
			if cfg.answer && v.TxPackets == 0 && v.RxPackets == 0 {
				continue // still waiting for a caller to latch
			}
			em.interval(now.Sub(start), v)
		}
	}

	var hs *voip.HandshakeError
	if errors.As(runErr, &hs) {
		return fmt.Errorf("rtp: call to %s failed: %w — is `loom rtp --answer` running there, with UDP allowed through?", cfg.call, hs)
	}
	if runErr != nil {
		return fmt.Errorf("rtp: %w", runErr)
	}
	// The summary must reflect the whole run, not the sliver since the last
	// ticker fire: an interval-closing Metrics() read here would present the
	// trailing partial interval's loss/MOS as the run's final quality (a 60s
	// call with mid-call loss would summarize as its last clean fraction).
	// CumulativeMetrics scores loss/discard/R/MOS over the entire call.
	var v metrics.VoIP
	if cs, ok := runner.(interface{ CumulativeMetrics() metrics.Snapshot }); ok {
		v, _ = cs.CumulativeMetrics().(metrics.VoIP)
	} else {
		v, _ = src.Metrics().(metrics.VoIP)
	}
	em.summary(time.Since(start), v)
	return nil
}

// resolveTarget turns the --call argument into the ip:port the voip client
// wants: names are resolved here, at the CLI edge, because the netpath seam
// deliberately carries no resolver.
func resolveTarget(ctx context.Context, target string) (string, error) {
	if _, err := netip.ParseAddrPort(target); err == nil {
		return target, nil
	}
	host, port, err := net.SplitHostPort(target)
	if err != nil {
		return "", fmt.Errorf("rtp: --call wants host:port: %w", err)
	}
	addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return "", fmt.Errorf("rtp: cannot resolve %q: %w", host, err)
	}
	if len(addrs) == 0 {
		// A nil err must not reach %w (it renders as "%!w(<nil>)").
		return "", fmt.Errorf("rtp: cannot resolve %q: lookup returned no addresses", host)
	}
	return net.JoinHostPort(addrs[0].Unmap().String(), port), nil
}

// rtpEmitter renders the run to one writer in either the human live-line
// style (matching `loom run`) or json-lines: an envelope object per event,
// with interval and summary events carrying a metrics.VoIP snapshot per end.
type rtpEmitter struct {
	w       io.Writer
	jsonOut bool
}

// rtpEvent is the json-lines envelope: "start", then "interval" objects
// (end = "local" for this end's received-media quality, "remote" for the
// peer's XR-reported view of what we send), then one "summary".
type rtpEvent struct {
	Type      string        `json:"type"`
	Mode      string        `json:"mode,omitempty"`
	Addr      string        `json:"addr,omitempty"`
	Target    string        `json:"target,omitempty"`
	Codec     string        `json:"codec,omitempty"`
	PtimeMs   float64       `json:"ptime_ms,omitempty"`
	JbMs      float64       `json:"jb_ms,omitempty"`
	DurationS float64       `json:"duration_s,omitempty"`
	ElapsedS  float64       `json:"elapsed_s,omitempty"`
	End       string        `json:"end,omitempty"`
	VoIP      *metrics.VoIP `json:"voip,omitempty"`
}

// emit writes one json-lines event.
func (e *rtpEmitter) emit(ev rtpEvent) {
	b, err := json.Marshal(ev)
	if err != nil {
		return
	}
	fmt.Fprintf(e.w, "%s\n", b)
}

// startAnswer announces the bound media address — the line the far end's
// --call argument comes from.
func (e *rtpEmitter) startAnswer(addr netip.AddrPort, cfg rtpConfig) {
	if e.jsonOut {
		e.emit(rtpEvent{Type: "start", Mode: "answer", Addr: addr.String(), Codec: cfg.codec,
			PtimeMs: ms(cfg.ptime), JbMs: ms(cfg.jb)})
		return
	}
	fmt.Fprintf(e.w, "answering on %s  codec %s  ptime %s  jb %s\n", addr, cfg.codec, cfg.ptime, cfg.jb)
	fmt.Fprintf(e.w, "waiting for a caller (first valid source latches; Ctrl-C to stop)\n")
}

// startCall announces the outgoing call.
func (e *rtpEmitter) startCall(target string, cfg rtpConfig, duration time.Duration) {
	if e.jsonOut {
		e.emit(rtpEvent{Type: "start", Mode: "call", Target: target, Codec: cfg.codec,
			PtimeMs: ms(cfg.ptime), JbMs: ms(cfg.jb), DurationS: duration.Seconds()})
		return
	}
	fmt.Fprintf(e.w, "calling %s  codec %s  ptime %s  jb %s  duration %s\n",
		target, cfg.codec, cfg.ptime, cfg.jb, duration)
}

// interval renders one report boundary: the local rx line (this end's
// received-media quality), plus a tx line with the remote end's MOS/R once
// RTCP XR has carried it back — both ends visible from one terminal.
func (e *rtpEmitter) interval(elapsed time.Duration, v metrics.VoIP) {
	if e.jsonOut {
		e.emit(rtpEvent{Type: "interval", ElapsedS: elapsed.Seconds(), End: "local", VoIP: &v})
		if r := remoteView(v); r != nil {
			e.emit(rtpEvent{Type: "interval", ElapsedS: elapsed.Seconds(), End: "remote", VoIP: r})
		}
		return
	}
	fmt.Fprintf(e.w, "[%6.1fs] rx  MOS %4.2f  R %5.1f  jit %5.1fms  loss %5.2f%%  disc %5.2f%%  rtt %5.1fms  owd %s\n",
		elapsed.Seconds(), v.MOSCQ, v.RFactor, v.JitterMs, v.LossPct, v.DiscardPct, v.RTTMs, owdText(v))
	if v.RemoteMOSCQ > 0 || v.RemoteRFactor > 0 {
		fmt.Fprintf(e.w, "[%6.1fs] tx  MOS %4.2f  R %5.1f  (remote XR)\n",
			elapsed.Seconds(), v.RemoteMOSCQ, v.RemoteRFactor)
	}
}

// summary renders the end-of-run cumulative picture: both ends' final quality,
// packet totals, delay figures, and the media-gap count/longest — the raw
// material for spotting outages after the fact.
func (e *rtpEmitter) summary(elapsed time.Duration, v metrics.VoIP) {
	if e.jsonOut {
		e.emit(rtpEvent{Type: "summary", ElapsedS: elapsed.Seconds(), End: "local", VoIP: &v})
		if r := remoteView(v); r != nil {
			e.emit(rtpEvent{Type: "summary", ElapsedS: elapsed.Seconds(), End: "remote", VoIP: r})
		}
		return
	}
	gaps, longest := gapStats(v)
	lossDen := v.RxPackets + v.Lost
	lossPct := 0.0
	if lossDen > 0 {
		lossPct = float64(v.Lost) / float64(lossDen) * 100
	}
	fmt.Fprintf(e.w, "--- summary ---\n")
	fmt.Fprintf(e.w, "  duration    : %s\n", elapsed.Round(time.Millisecond))
	fmt.Fprintf(e.w, "  local  (rx) : MOS %4.2f  R %5.1f  jitter %.1fms\n", v.MOSCQ, v.RFactor, v.JitterMs)
	if v.RemoteMOSCQ > 0 || v.RemoteRFactor > 0 {
		fmt.Fprintf(e.w, "  remote (tx) : MOS %4.2f  R %5.1f  (via RTCP XR)\n", v.RemoteMOSCQ, v.RemoteRFactor)
	} else {
		fmt.Fprintf(e.w, "  remote (tx) : no RTCP XR received\n")
	}
	fmt.Fprintf(e.w, "  packets     : tx %d  rx %d  lost %d (%.2f%%)  dup %d  reordered %d\n",
		v.TxPackets, v.RxPackets, v.Lost, lossPct, v.Duplicates, v.Reordered)
	fmt.Fprintf(e.w, "  rtt / owd   : rtt %.1fms  owd %s\n", v.RTTMs, owdText(v))
	if gaps > 0 {
		fmt.Fprintf(e.w, "  media gaps  : %d (longest %s)\n", gaps, longest.Round(time.Millisecond))
	} else {
		fmt.Fprintf(e.w, "  media gaps  : 0\n")
	}
}

// remoteView extracts the peer's XR-reported quality as its own snapshot, or
// nil before any XR has arrived. Only MOS/R travel in the XR VoIP-metrics
// block — the rest of the remote picture lives on the remote terminal.
func remoteView(v metrics.VoIP) *metrics.VoIP {
	if v.RemoteMOSCQ == 0 && v.RemoteRFactor == 0 {
		return nil
	}
	return &metrics.VoIP{Codec: v.Codec, MOSCQ: v.RemoteMOSCQ, RFactor: v.RemoteRFactor}
}

// owdText renders the one-way delay with its error bar and provenance, or
// "n/a" while no estimate exists — the label travels with the number.
func owdText(v metrics.VoIP) string {
	if v.OWDMethod == "none" || v.OWDMethod == "" {
		return "n/a"
	}
	return fmt.Sprintf("%.1f±%.1fms (%s)", v.OWDMs, v.OWDErrMs, v.OWDMethod)
}

// gapStats summarizes the media-gap list: count and longest.
func gapStats(v metrics.VoIP) (int, time.Duration) {
	var longest time.Duration
	for _, g := range v.MediaGaps {
		if d := g.End.Sub(g.Start); d > longest {
			longest = d
		}
	}
	return len(v.MediaGaps), longest
}

// ms converts a duration to float milliseconds for the JSON envelope.
func ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }
