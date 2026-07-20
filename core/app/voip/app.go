// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package voip

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strconv"
	"time"

	"github.com/bgrewell/loom/core/accounting"
	"github.com/bgrewell/loom/core/app"
	"github.com/bgrewell/loom/core/metrics"
	"github.com/bgrewell/loom/core/rtp/codec"
)

// Name is the registry name under which the VoIP client and server register
// (metrics.KindVoIP, and FlowSpec.app in Phase 3).
const Name = "voip"

func init() {
	app.ClientRegistry.Register(Name, NewClient)
	app.ServerRegistry.Register(Name, NewServer)
}

// Compile-time checks: both sides expose a quality snapshot through the
// metrics.Source seam the telemetry streamer asserts for, and a Close for
// the built-but-never-run teardown path (NewMediaSession binds eagerly so
// Addr is valid at configure time; without Close a flow torn down between
// Configure and Start would leak its advertised port). Consumers discover
// Close by io.Closer assertion, the same optional-capability pattern as
// metrics.Source.
var (
	_ metrics.Source = (*client)(nil)
	_ metrics.Source = (*server)(nil)
	_ io.Closer      = (*client)(nil)
	_ io.Closer      = (*server)(nil)
)

// client adapts a MediaSession to app.Client (caller side).
type client struct{ sess *MediaSession }

// Name implements app.Client.
func (c *client) Name() string { return Name }

// Run implements app.Client.
func (c *client) Run(ctx context.Context) error { return c.sess.Run(ctx) }

// Counters implements app.Client.
func (c *client) Counters() *accounting.Counters { return c.sess.Counters() }

// Metrics implements metrics.Source. Each call closes one observation
// interval (see MediaSession.Metrics).
func (c *client) Metrics() metrics.Snapshot { return c.sess.Metrics() }

// CumulativeMetrics returns the whole-call snapshot without closing an
// observation interval — the final-sample/summary capability consumers
// discover by assertion (interface{ CumulativeMetrics() metrics.Snapshot }),
// the same optional pattern as io.Closer and metrics.Source.
func (c *client) CumulativeMetrics() metrics.Snapshot { return c.sess.CumulativeMetrics() }

// Close implements io.Closer: it releases the session's socket when the
// instance was built but never run (idempotent; see MediaSession.Close).
func (c *client) Close() error { return c.sess.Close() }

// server adapts a MediaSession to app.Server (answerer side).
type server struct{ client }

// Addr implements app.Server: the bound media port, valid from Build time
// (NewMediaSession binds in the constructor) so the agent can advertise it
// as the flow's data_port before Run.
func (s *server) Addr() netip.AddrPort { return s.sess.LocalAddr() }

// NewClient builds the "voip" caller: a MediaSession dialed at
// Options.Target (an IP:port; on port-routed networks like the in-memory
// fabric a non-IP host is accepted and ignored — the netpath seam carries no
// resolver). Parameters, all optional:
//
//	codec                 codec table name (default "pcmu")
//	ptime                 packetization interval (Go duration, e.g. "20ms")
//	jb_ms                 jitter-buffer depth in ms (default 40)
//	handshake_timeout_ms  caller handshake bound (default 5000)
//	direction             "sendrecv" (default), "sendonly", "recvonly"
//
// Options.OWD (nil ⇒ labeled RTT/2 fallback) and Options.Seed (RTCP interval
// randomization) are honored; Options.Network is required.
func NewClient(o app.Options) (app.Client, error) {
	cfg, err := configFromOptions(o)
	if err != nil {
		return nil, err
	}
	if o.Target == "" {
		return nil, errors.New("voip: client requires Options.Target (server host:port)")
	}
	cfg.RemoteRTP, err = parseTarget(o.Target)
	if err != nil {
		return nil, err
	}
	sess, err := NewMediaSession(o.Network, cfg, o.OWD)
	if err != nil {
		return nil, err
	}
	if o.Seed != 0 {
		sess.seed = o.Seed
	}
	return &client{sess: sess}, nil
}

// NewServer builds the "voip" answerer: a MediaSession in latch mode. Beyond
// NewClient's parameters (Target is ignored) it honors:
//
//	port_min, port_max    inclusive bind range for firewall determinism; the
//	                      first free port wins and Addr reports it. Omitted ⇒
//	                      ephemeral even port.
func NewServer(o app.Options) (app.Server, error) {
	cfg, err := configFromOptions(o)
	if err != nil {
		return nil, err
	}
	p := app.NewParams(o.Params)
	portMin := p.GetInt("port_min", 0)
	portMax := p.GetInt("port_max", portMin)
	if err := p.Err(); err != nil {
		return nil, fmt.Errorf("voip: %w", err)
	}
	if portMin < 0 || portMax > 65535 || portMax < portMin {
		return nil, fmt.Errorf("voip: invalid port range %d..%d", portMin, portMax)
	}
	if portMax > 0 && portMin == 0 {
		// Half a range is a silent ephemeral bind outside the firewall's
		// pinhole — reject it at Build time instead.
		return nil, fmt.Errorf("voip: port_max %d given without port_min", portMax)
	}
	var sess *MediaSession
	if portMin > 0 {
		var lastErr error
		for port := portMin; port <= portMax; port++ {
			cfg.LocalRTP = netip.AddrPortFrom(cfg.LocalRTP.Addr(), uint16(port))
			if sess, lastErr = NewMediaSession(o.Network, cfg, o.OWD); lastErr == nil {
				break
			}
		}
		if sess == nil {
			return nil, fmt.Errorf("voip: no free port in %d..%d: %w", portMin, portMax, lastErr)
		}
	} else if sess, err = NewMediaSession(o.Network, cfg, o.OWD); err != nil {
		return nil, err
	}
	if o.Seed != 0 {
		sess.seed = o.Seed
	}
	return &server{client{sess: sess}}, nil
}

// configFromOptions reads the shared MediaConfig knobs out of Options.Params
// with app.Params error accumulation.
func configFromOptions(o app.Options) (MediaConfig, error) {
	if o.Network == nil {
		return MediaConfig{}, errors.New("voip: Options.Network is required")
	}
	p := app.NewParams(o.Params)
	name := p.GetString("codec", "pcmu")
	c, cerr := codec.ByName(name)
	if ptime := p.GetDuration("ptime", 0); ptime > 0 {
		c.Ptime = ptime
	}
	cfg := MediaConfig{
		Codec:            c,
		JitterBufferMs:   p.GetInt("jb_ms", 0),
		HandshakeTimeout: time.Duration(p.GetInt("handshake_timeout_ms", 0)) * time.Millisecond,
	}
	switch d := p.GetString("direction", "sendrecv"); d {
	case "sendrecv":
		cfg.Direction = SendRecv
	case "sendonly":
		cfg.Direction = SendOnly
	case "recvonly":
		cfg.Direction = RecvOnly
	default:
		cerr = errors.Join(cerr, fmt.Errorf("param %q: unknown direction %q", "direction", d))
	}
	if err := errors.Join(cerr, p.Err()); err != nil {
		return MediaConfig{}, fmt.Errorf("voip: %w", err)
	}
	return cfg, nil
}

// parseTarget turns a host:port target into the MediaConfig's RemoteRTP.
// Non-IP hosts (DNS names, the memory fabric's "mem") map to the unspecified
// IPv4 address: routing then works only on networks that route by port.
func parseTarget(t string) (netip.AddrPort, error) {
	if ap, err := netip.ParseAddrPort(t); err == nil {
		if ap.Port() == 0 {
			return netip.AddrPort{}, fmt.Errorf("voip: target %q has port 0", t)
		}
		return ap, nil
	}
	_, ps, err := net.SplitHostPort(t)
	if err != nil {
		return netip.AddrPort{}, fmt.Errorf("voip: target %q: %w", t, err)
	}
	port, err := strconv.Atoi(ps)
	if err != nil || port <= 0 || port > 65535 {
		return netip.AddrPort{}, fmt.Errorf("voip: target %q: invalid port %q", t, ps)
	}
	return netip.AddrPortFrom(netip.IPv4Unspecified(), uint16(port)), nil
}
