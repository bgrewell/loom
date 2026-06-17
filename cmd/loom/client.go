// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"

	"github.com/bgrewell/loom/control"
	"github.com/bgrewell/loom/controller"
	"github.com/bgrewell/loom/core/units"
	"github.com/bgrewell/stencil"
)

// clientCommand builds `loom client <host>`: the iperf3-style test driver. It
// connects to a loom agent on <host> (a `loom server`, or an already-running
// loomd) and runs a throughput test, printing live interval lines and an
// authoritative summary.
func clientCommand() *stencil.Command {
	fs := stencil.NewFlagSet()
	fs.Int("port", "p", "server control port", 9551)
	fs.String("time", "t", "test duration, e.g. 10 (seconds) or 500ms", "10")
	fs.Bool("udp", "u", "use UDP (default TCP)", false)
	fs.String("bitrate", "b", "target send rate, e.g. 1G or 100Mbps (empty = unlimited)", "")
	fs.Int("length", "l", "packet/datagram size in bytes", 1400)
	fs.Int("parallel", "P", "number of parallel streams", 1)
	fs.Bool("reverse", "R", "reverse direction: server sends to client", false)
	fs.Bool("bidir", "", "bidirectional: test both directions at once", false)
	fs.String("bytes", "n", "stop after N bytes, e.g. 100M (overrides --time)", "")
	fs.String("interval", "i", "report interval, e.g. 1 (seconds) or 500ms", "1")
	fs.Bool("json", "J", "emit a single JSON summary instead of live text", false)
	fs.String("token", "", "control-plane auth token (default $LOOM_TOKEN)", "")
	fs.Bool("per-flow", "", "show per-flow lines (auto-on for -P>1 or --bidir)", false)
	return &stencil.Command{
		Name:    "client",
		Summary: "Connect to a loom server and run a throughput test",
		Long:    "Run an iperf3-style throughput test against a loom agent on <host>. Forward by default; -R reverses, --bidir does both.",
		Args:    stencil.ArgSpec{Min: 1, Max: 1, Names: []string{"host"}},
		Flags:   fs,
		Run:     runClient,
	}
}

func runClient(ctx *stencil.Context) error {
	host := ctx.Args[0]
	port := ctx.Flags.Int("port")
	serverAddr := net.JoinHostPort(host, strconv.Itoa(port))

	token := ctx.Flags.String("token")
	if token == "" {
		token = os.Getenv("LOOM_TOKEN")
	}

	rate := ctx.Flags.String("bitrate")
	if rate != "" {
		if _, err := units.ParseRate(rate); err != nil {
			return fmt.Errorf("bad --bitrate %q: %w", rate, err)
		}
	}
	var volume uint64
	if b := ctx.Flags.String("bytes"); b != "" {
		v, err := units.ParseSize(b)
		if err != nil {
			return fmt.Errorf("bad --bytes %q: %w", b, err)
		}
		volume = v
	}

	duration, err := parseDur(ctx.Flags.String("time"))
	if err != nil {
		return fmt.Errorf("bad --time: %w", err)
	}
	interval, err := parseDur(ctx.Flags.String("interval"))
	if err != nil {
		return fmt.Errorf("bad --interval: %w", err)
	}
	reverse := ctx.Flags.Bool("reverse")
	bidir := ctx.Flags.Bool("bidir")
	parallel := ctx.Flags.Int("parallel")
	jsonOut := ctx.Flags.Bool("json")
	perFlow := ctx.Flags.Bool("per-flow") || parallel > 1 || bidir

	// Start an embedded agent for this (client) endpoint — the same agent loomd
	// runs — so the controller can place the client-side sender/receiver here.
	var opts []control.Option
	if token != "" {
		opts = append(opts, control.WithAuthToken(token))
	}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("embedded agent listen: %w", err)
	}
	gs := control.NewGRPCServer(control.NewServer(version, opts...))
	go func() { _ = gs.Serve(lis) }()
	defer gs.GracefulStop()
	clientCtrlAddr := lis.Addr().String()

	// The server targets the client's routable IP for reverse/bidir traffic.
	clientIP := localSourceIP(serverAddr)
	if (reverse || bidir) && clientIP == "" {
		fmt.Fprintf(os.Stderr, "warning: could not determine this host's source IP toward %s; reverse/bidir may not receive\n", host)
	}
	if (reverse || bidir) && clientIP != "" {
		fmt.Fprintf(os.Stderr, "note: reverse/bidir needs %s reachable from the server (won't traverse NAT)\n", clientIP)
	}

	sc := buildScenario(clientOpts{
		host: host, clientIP: clientIP, udp: ctx.Flags.Bool("udp"),
		packetSize: ctx.Flags.Int("length"), rate: rate, duration: duration,
		volume: volume, parallel: parallel, reverse: reverse, bidir: bidir,
	})

	addrs := map[string]string{epClient: clientCtrlAddr, epServer: serverAddr}
	c := controller.New(sc, addrs, controller.WithToken(token), controller.WithInterval(interval))
	defer c.Close()

	tel := controller.NewTelemetry(interval, controller.WithTelemetryToken(token))
	defer tel.Close()
	if !jsonOut {
		tel.AddObserver(controller.NewTextObserver(os.Stdout).WithPerFlow(perFlow))
	}

	// Horizon is a safety ceiling; WaitSources ends the run as soon as the sources
	// finish. Volume-bounded runs have no time bound, so allow generous headroom.
	horizon := duration + 10*time.Second
	if volume > 0 {
		horizon = time.Hour
	}

	fmt.Fprintf(os.Stderr, "connecting to %s\n", serverAddr)
	sigCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	agg, wall, err := controller.RunSession(sigCtx, c, tel, horizon)
	if err != nil {
		return err
	}

	// Average over the intended duration for time-bounded runs (consistent with the
	// interval lines); over wall-clock for volume-bounded ones.
	disp := duration
	if volume > 0 {
		disp = wall
	}
	if jsonOut {
		printJSONSummary(os.Stdout, "loom", agg, disp, tel.LiveIncomplete())
	} else {
		fmt.Fprint(os.Stdout, agg.Summary("loom", disp, perFlow, tel.LiveIncomplete()))
	}
	return nil
}

// parseDur accepts an iperf3-style bare number of seconds ("10") or a Go duration
// string ("10s", "500ms").
func parseDur(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if d, err := time.ParseDuration(s); err == nil {
		return d, nil
	}
	if n, err := strconv.Atoi(s); err == nil {
		return time.Duration(n) * time.Second, nil
	}
	return 0, fmt.Errorf("invalid duration %q (use e.g. 10, 10s, or 500ms)", s)
}

// localSourceIP returns the local IP the kernel would use to reach addr (host:port),
// without sending anything — a connected UDP socket just selects the route. Empty
// on failure.
func localSourceIP(addr string) string {
	c, err := net.Dial("udp", addr)
	if err != nil {
		return ""
	}
	defer c.Close()
	if ua, ok := c.LocalAddr().(*net.UDPAddr); ok {
		return ua.IP.String()
	}
	return ""
}

// printJSONSummary writes a final machine-readable summary object. Copied from
// loomctl (a main package that can't be imported) to keep loomctl untouched.
func printJSONSummary(w *os.File, scenario string, a controller.Aggregate, dur time.Duration, liveIncomplete bool) {
	secs := dur.Seconds()
	avg := func(bytes uint64) float64 {
		if secs <= 0 {
			return 0
		}
		return float64(bytes) * 8 / secs
	}
	streams, _ := controller.StreamSummary(a.Flows)
	enc := json.NewEncoder(w)
	_ = enc.Encode(map[string]any{
		"summary":          true,
		"authoritative":    true,
		"scenario":         scenario,
		"duration_seconds": secs,
		"streams":          streams,
		"tx_bytes":         a.TxBytes,
		"rx_bytes":         a.RxBytes,
		"tx_avg_bps":       avg(a.TxBytes),
		"rx_avg_bps":       avg(a.RxBytes),
		"flows":            len(a.Flows),
		"live_incomplete":  liveIncomplete,
	})
}
