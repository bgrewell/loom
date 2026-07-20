// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"

	loomv1 "github.com/bgrewell/loom/api/loomv1"
	"github.com/bgrewell/loom/core/scenario"

	// Register the voip app pair on the in-process agents, the way a stock
	// loomd does (cmd/loomd blank-imports the same package).
	_ "github.com/bgrewell/loom/core/app/voip"
)

// fakeAgent is an in-process loomv1.ControlClient for placement tests
// (WithDialer, ADR-0022): it records Configure/Start calls in order and
// answers Capabilities/Health from canned values, so app placement and the
// version-skew gate are testable without agents that implement the app roles.
type fakeAgent struct {
	name       string
	version    string
	apps       []string
	appsClient []string // per-side lists; empty = agent predates them (union fallback)
	appsServer []string
	networks   []string
	dataPort   uint32

	mu         sync.Mutex
	configured []*loomv1.FlowSpec
	started    []*loomv1.StartRequest
	calls      *[]string // shared across agents: global configure/start order
}

func (f *fakeAgent) record(s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.calls != nil {
		*f.calls = append(*f.calls, f.name+":"+s)
	}
}

func (f *fakeAgent) Health(context.Context, *loomv1.HealthRequest, ...grpc.CallOption) (*loomv1.HealthResponse, error) {
	return &loomv1.HealthResponse{Version: f.version, Ready: true}, nil
}

func (f *fakeAgent) Register(context.Context, *loomv1.RegisterRequest, ...grpc.CallOption) (*loomv1.RegisterResponse, error) {
	return &loomv1.RegisterResponse{}, nil
}

func (f *fakeAgent) Capabilities(context.Context, *loomv1.CapabilitiesRequest, ...grpc.CallOption) (*loomv1.CapabilitiesResponse, error) {
	return &loomv1.CapabilitiesResponse{
		Apps: f.apps, AppsClient: f.appsClient, AppsServer: f.appsServer, Networks: f.networks,
	}, nil
}

func (f *fakeAgent) Configure(_ context.Context, req *loomv1.ConfigureRequest, _ ...grpc.CallOption) (*loomv1.ConfigureResponse, error) {
	f.mu.Lock()
	f.configured = append(f.configured, req.GetFlow())
	id := fmt.Sprintf("flow-%d", len(f.configured))
	f.mu.Unlock()
	f.record("configure:" + req.GetFlow().GetRole().String())
	var port uint32
	if req.GetFlow().GetRole() == loomv1.FlowRole_FLOW_ROLE_APP_SERVER {
		port = f.dataPort
	}
	return &loomv1.ConfigureResponse{FlowId: id, DataPort: port}, nil
}

func (f *fakeAgent) Arm(context.Context, *loomv1.ArmRequest, ...grpc.CallOption) (*loomv1.ArmResponse, error) {
	return &loomv1.ArmResponse{}, nil
}

func (f *fakeAgent) Start(_ context.Context, req *loomv1.StartRequest, _ ...grpc.CallOption) (*loomv1.StartResponse, error) {
	f.mu.Lock()
	f.started = append(f.started, req)
	f.mu.Unlock()
	f.record("start:" + req.GetFlowId())
	return &loomv1.StartResponse{}, nil
}

func (f *fakeAgent) Stop(context.Context, *loomv1.StopRequest, ...grpc.CallOption) (*loomv1.StopResponse, error) {
	return &loomv1.StopResponse{}, nil
}

func (f *fakeAgent) Destroy(context.Context, *loomv1.DestroyRequest, ...grpc.CallOption) (*loomv1.DestroyResponse, error) {
	return &loomv1.DestroyResponse{}, nil
}

func (f *fakeAgent) TimeSync(_ context.Context, req *loomv1.TimeSyncRequest, _ ...grpc.CallOption) (*loomv1.TimeSyncResponse, error) {
	now := time.Now().UnixNano()
	return &loomv1.TimeSyncResponse{T1: req.GetT1(), T2: now, T3: now}, nil
}

func (f *fakeAgent) StreamTelemetry(context.Context, *loomv1.TelemetryRequest, ...grpc.CallOption) (grpc.ServerStreamingClient[loomv1.TelemetrySample], error) {
	return nil, fmt.Errorf("not implemented in fake")
}

type nopCloser struct{}

func (nopCloser) Close() error { return nil }

// fakeDialer maps agent addresses to fakes.
func fakeDialer(agents map[string]*fakeAgent) Dialer {
	return func(addr string) (loomv1.ControlClient, io.Closer, error) {
		a, ok := agents[addr]
		if !ok {
			return nil, nil, fmt.Errorf("no fake agent at %q", addr)
		}
		return a, nopCloser{}, nil
	}
}

func voipScenario(params map[string]any) *scenario.Scenario {
	p := map[string]any{"codec": "pcmu", "port_min": 40000, "port_max": 40100}
	for k, v := range params {
		p[k] = v
	}
	return &scenario.Scenario{
		Name: "call",
		Seed: 11,
		Endpoints: []scenario.Endpoint{
			{Name: "ran"},
			{Name: "n6", Address: "203.0.113.9"},
		},
		Timeline: []scenario.Event{{
			Name:  "voice",
			Flow:  scenario.Flow{Kind: "voip", Params: p},
			From:  scenario.Selector{Raw: "ran"},
			To:    scenario.Selector{Raw: "n6"},
			Start: scenario.Start{Offset: 0},
			Stop:  scenario.Stop{After: 60 * time.Second},
		}},
	}
}

// TestControllerPlacesAppFlows: a voip flow kind places an APP_SERVER on the
// 'to' agent first (bound at Configure, so it is reachable) and an APP_CLIENT
// on the 'from' agent whose Target carries the server's reported data_port —
// the responder→requester ordering, mirrored — with both ends started at one
// shared gate.
func TestControllerPlacesAppFlows(t *testing.T) {
	var calls []string
	ranAgent := &fakeAgent{name: "ran", version: "v0.10.0", apps: []string{"voip"}, networks: []string{"host"}, calls: &calls}
	n6Agent := &fakeAgent{name: "n6", version: "v0.10.0", apps: []string{"voip"}, networks: []string{"host"}, dataPort: 40007, calls: &calls}
	agents := map[string]*fakeAgent{"ran:9551": ranAgent, "n6:9551": n6Agent}

	c := New(voipScenario(nil), map[string]string{"ran": "ran:9551", "n6": "n6:9551"}, WithDialer(fakeDialer(agents)))
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Run(ctx, time.Second); err != nil {
		t.Fatalf("controller Run: %v", err)
	}

	// Server on the 'to' agent, configured and started before the client.
	if len(n6Agent.configured) != 1 || len(ranAgent.configured) != 1 {
		t.Fatalf("configured n6=%d ran=%d, want 1 each", len(n6Agent.configured), len(ranAgent.configured))
	}
	srv, cli := n6Agent.configured[0], ranAgent.configured[0]
	if srv.GetRole() != loomv1.FlowRole_FLOW_ROLE_APP_SERVER || srv.GetApp() != "voip" {
		t.Errorf("server role/app = %v/%q, want APP_SERVER/voip", srv.GetRole(), srv.GetApp())
	}
	if srv.GetParams()["port_min"] != "40000" || srv.GetParams()["port_max"] != "40100" {
		t.Errorf("server params missing port range: %v", srv.GetParams())
	}
	if got := srv.GetDuration().AsDuration(); got != 60*time.Second+appServerGrace {
		t.Errorf("server duration = %v, want 60s + grace %v (orphan protection)", got, appServerGrace)
	}
	if cli.GetRole() != loomv1.FlowRole_FLOW_ROLE_APP_CLIENT || cli.GetApp() != "voip" {
		t.Errorf("client role/app = %v/%q, want APP_CLIENT/voip", cli.GetRole(), cli.GetApp())
	}
	// The client targets the endpoint's data address at the server's data_port.
	if cli.GetTarget() != "203.0.113.9:40007" {
		t.Errorf("client target = %q, want 203.0.113.9:40007", cli.GetTarget())
	}
	if got := cli.GetDuration().AsDuration(); got != 60*time.Second {
		t.Errorf("client duration = %v, want 60s", got)
	}
	if cli.GetSeed() != 11 || srv.GetSeed() != 11 {
		t.Errorf("seeds = %d/%d, want scenario seed 11", cli.GetSeed(), srv.GetSeed())
	}

	// Ordering: server configure+start strictly before the client's configure.
	want := []string{"n6:configure:FLOW_ROLE_APP_SERVER", "n6:start:flow-1", "ran:configure:FLOW_ROLE_APP_CLIENT", "ran:start:flow-1"}
	if strings.Join(calls, ",") != strings.Join(want, ",") {
		t.Errorf("call order = %v, want %v", calls, want)
	}
	// Both ends start at ONE shared gate (fire()'s lockstep invariant): their
	// telemetry interval clocks align for per-interval consolidation, and the
	// server's duration deadline is pinned to gate+d+grace so it always
	// outlives the client's gate+d call. The server is nonetheless reachable
	// from Configure (it binds its port at Build).
	srvAt := n6Agent.started[0].GetStartAtUnixNanos()
	cliAt := ranAgent.started[0].GetStartAtUnixNanos()
	if srvAt == 0 || cliAt == 0 {
		t.Errorf("start_at server=%d client=%d, want both gated (nonzero)", srvAt, cliAt)
	}
	if srvAt != cliAt {
		t.Errorf("start_at server=%d client=%d, want one shared gate", srvAt, cliAt)
	}
	if cliAt <= time.Now().Add(-time.Second).UnixNano() {
		t.Errorf("client start_at = %d, want a (near-)future gate", cliAt)
	}

	// Both ends tracked with the app roles, labeled by the app name.
	placed := c.Placed()
	if len(placed) != 2 {
		t.Fatalf("placed %d flows, want 2", len(placed))
	}
	roles := map[Role]Placed{}
	for _, p := range placed {
		roles[p.Role] = p
	}
	if _, ok := roles[AppServer]; !ok {
		t.Fatalf("no AppServer placed: %+v", placed)
	}
	if _, ok := roles[AppClient]; !ok {
		t.Fatalf("no AppClient placed: %+v", placed)
	}
	if roles[AppServer].Datapath != "voip" || roles[AppClient].From != "ran" || roles[AppClient].To != "n6" {
		t.Errorf("placed metadata = %+v", roles)
	}
}

// TestControllerPlacesVideoFlows: the video kind's far end is the "http"
// origin (the ABR player is client-only, design §2.10): the server spec
// carries app "http" — gated against the destination agent's server side —
// while the client spec carries app "video", and params travel verbatim to
// both ends (the ladder configures the origin and doubles as the player's
// expectation).
func TestControllerPlacesVideoFlows(t *testing.T) {
	var calls []string
	ranAgent := &fakeAgent{name: "ran", version: "v0.12.0",
		apps: []string{"http", "video"}, appsClient: []string{"http", "video"}, appsServer: []string{"http"},
		networks: []string{"host"}, calls: &calls}
	n6Agent := &fakeAgent{name: "n6", version: "v0.12.0",
		apps: []string{"http", "video"}, appsClient: []string{"http", "video"}, appsServer: []string{"http"},
		networks: []string{"host"}, dataPort: 8443, calls: &calls}
	agents := map[string]*fakeAgent{"ran:9551": ranAgent, "n6:9551": n6Agent}

	s := &scenario.Scenario{
		Name: "stream",
		Seed: 7,
		Endpoints: []scenario.Endpoint{
			{Name: "ran"},
			{Name: "n6", Address: "203.0.113.9"},
		},
		Timeline: []scenario.Event{{
			Name:  "binge",
			Flow:  scenario.Flow{Kind: "video", Params: map[string]any{"ladder": "l:400k,h:2500k", "seg_duration": "4s"}},
			From:  scenario.Selector{Raw: "ran"},
			To:    scenario.Selector{Raw: "n6"},
			Start: scenario.Start{Offset: 0},
			Stop:  scenario.Stop{After: 60 * time.Second},
		}},
	}
	c := New(s, map[string]string{"ran": "ran:9551", "n6": "n6:9551"}, WithDialer(fakeDialer(agents)))
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Run(ctx, time.Second); err != nil {
		t.Fatalf("controller Run: %v", err)
	}

	if len(n6Agent.configured) != 1 || len(ranAgent.configured) != 1 {
		t.Fatalf("configured n6=%d ran=%d, want 1 each", len(n6Agent.configured), len(ranAgent.configured))
	}
	srv, cli := n6Agent.configured[0], ranAgent.configured[0]
	if srv.GetRole() != loomv1.FlowRole_FLOW_ROLE_APP_SERVER || srv.GetApp() != "http" {
		t.Errorf("server role/app = %v/%q, want APP_SERVER/http (video's far end is the http origin)", srv.GetRole(), srv.GetApp())
	}
	if cli.GetRole() != loomv1.FlowRole_FLOW_ROLE_APP_CLIENT || cli.GetApp() != "video" {
		t.Errorf("client role/app = %v/%q, want APP_CLIENT/video", cli.GetRole(), cli.GetApp())
	}
	if cli.GetTarget() != "203.0.113.9:8443" {
		t.Errorf("client target = %q, want 203.0.113.9:8443", cli.GetTarget())
	}
	// The shared param grammar rides both specs verbatim.
	for _, spec := range []*loomv1.FlowSpec{srv, cli} {
		if spec.GetParams()["ladder"] != "l:400k,h:2500k" || spec.GetParams()["seg_duration"] != "4s" {
			t.Errorf("%v params = %v, want the ladder/seg_duration passthrough", spec.GetRole(), spec.GetParams())
		}
	}
	// Tracked under the engine actually placed on each side.
	roles := map[Role]Placed{}
	for _, p := range c.Placed() {
		roles[p.Role] = p
	}
	if roles[AppServer].Datapath != "http" || roles[AppClient].Datapath != "video" {
		t.Errorf("placed labels = server %q / client %q, want http / video", roles[AppServer].Datapath, roles[AppClient].Datapath)
	}
}

// TestVideoSkewGateWantsHTTPServer: an agent whose server side lacks "http"
// cannot be a video far end, and the refusal names the engine actually
// missing — the http server — not the video kind (which no agent will ever
// advertise as a server, the player being client-only).
func TestVideoSkewGateWantsHTTPServer(t *testing.T) {
	clientOnly := &fakeAgent{name: "n6", version: "v0.12.0",
		apps: []string{"video"}, appsClient: []string{"video"}, networks: []string{"host"}}
	full := &fakeAgent{name: "ran", version: "v0.12.0",
		apps: []string{"http", "video"}, appsClient: []string{"http", "video"}, appsServer: []string{"http"},
		networks: []string{"host"}}
	agents := map[string]*fakeAgent{"ran:9551": full, "n6:9551": clientOnly}

	s := &scenario.Scenario{
		Name:      "stream",
		Endpoints: []scenario.Endpoint{{Name: "ran"}, {Name: "n6"}},
		Timeline: []scenario.Event{{
			Name:  "binge",
			Flow:  scenario.Flow{Kind: "video"},
			From:  scenario.Selector{Raw: "ran"},
			To:    scenario.Selector{Raw: "n6"},
			Start: scenario.Start{Offset: 0},
			Stop:  scenario.Stop{After: 30 * time.Second},
		}},
	}
	c := New(s, map[string]string{"ran": "ran:9551", "n6": "n6:9551"}, WithDialer(fakeDialer(agents)))
	defer c.Close()

	err := c.Run(context.Background(), time.Second)
	if err == nil || !strings.Contains(err.Error(), `lacks app server "http"`) {
		t.Fatalf("error = %v, want a refusal naming the missing http server", err)
	}
	if len(clientOnly.configured)+len(full.configured) != 0 {
		t.Errorf("gate must fail fast: %d flows were configured", len(clientOnly.configured)+len(full.configured))
	}
}

// TestControllerDrivesVoipScenario is the end-to-end path over real agents:
// a voip flow kind passes the skew gate (the agents advertise the app), places
// the answerer + caller pair, and the server's boundary telemetry carries both
// traffic accounting and VoipMetrics.
func TestControllerDrivesVoipScenario(t *testing.T) {
	ranAddr, stopRan := startAgent(t)
	defer stopRan()
	n6Addr, stopN6 := startAgent(t)
	defer stopN6()

	s := &scenario.Scenario{
		Name: "call",
		Seed: 5,
		Endpoints: []scenario.Endpoint{
			{Name: "ran"},
			{Name: "n6"},
		},
		Timeline: []scenario.Event{{
			Name:  "voice",
			Flow:  scenario.Flow{Kind: "voip", Params: map[string]any{"codec": "pcmu"}},
			From:  scenario.Selector{Raw: "ran"},
			To:    scenario.Selector{Raw: "n6"},
			Start: scenario.Start{Offset: 0},
			Stop:  scenario.Stop{After: 3 * time.Second},
		}},
	}

	c := New(s, map[string]string{"ran": ranAddr, "n6": n6Addr}, WithInterval(200*time.Millisecond))
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := c.Run(ctx, time.Second); err != nil {
		t.Fatalf("controller Run: %v", err)
	}
	defer c.Teardown(context.Background())

	var srv Placed
	roles := map[Role]bool{}
	for _, p := range c.Placed() {
		roles[p.Role] = true
		if p.Role == AppServer {
			srv = p
		}
	}
	if !roles[AppServer] || !roles[AppClient] {
		t.Fatalf("expected app server + client, got %+v", roles)
	}

	// Read the server's boundary samples until media has flowed and the app
	// snapshot is attached (the client starts at the gate, so the first
	// boundary may predate media).
	stream, err := srv.Agent.StreamTelemetry(ctx, &loomv1.TelemetryRequest{FlowId: srv.FlowID})
	if err != nil {
		t.Fatalf("server StreamTelemetry: %v", err)
	}
	for {
		sample, err := stream.Recv()
		if err != nil {
			t.Fatalf("server telemetry ended before media + app metrics were seen: %v", err)
		}
		v := sample.GetApp().GetVoip()
		if sample.GetBytes() > 0 && v != nil && v.GetRxPackets() > 0 {
			if v.GetMosCq() < 1 || v.GetMosCq() > 5 {
				t.Fatalf("voip MOS-CQ = %v, want within [1,5]", v.GetMosCq())
			}
			t.Logf("voip e2e: server accounted %d bytes, MOS-CQ %.2f (R %.1f, jitter %.2fms)",
				sample.GetBytes(), v.GetMosCq(), v.GetRFactor(), v.GetJitterMs())
			return
		}
	}
}

// TestAppFlowRequiresDuration: an unbounded app event is refused at placement
// time with the scenario-level fix, before any RPC is issued.
func TestAppFlowRequiresDuration(t *testing.T) {
	a := &fakeAgent{name: "n6", version: "v0.10.0", apps: []string{"voip"}, networks: []string{"host"}}
	agents := map[string]*fakeAgent{"ran:9551": a, "n6:9551": a}
	sc := voipScenario(nil)
	sc.Timeline[0].Stop = scenario.Stop{} // unbounded

	c := New(sc, map[string]string{"ran": "ran:9551", "n6": "n6:9551"}, WithDialer(fakeDialer(agents)))
	defer c.Close()

	err := c.Run(context.Background(), time.Second)
	if err == nil || !strings.Contains(err.Error(), "requires a duration bound") {
		t.Fatalf("error = %v, want duration-bound refusal", err)
	}
	if len(a.configured) != 0 {
		t.Errorf("no flow should be configured for an unbounded app event, got %d", len(a.configured))
	}
}

// TestAppFlowRefusesCountVolumeStops: the agents' app path enforces only a
// duration bound, so stop.count/stop.volume on an app flow are refused with
// an actionable error instead of being silently dropped ("whichever is
// reached first" must never mean "never").
func TestAppFlowRefusesCountVolumeStops(t *testing.T) {
	for name, stop := range map[string]scenario.Stop{
		"count":  {After: 60 * time.Second, Count: 1000},
		"volume": {After: 60 * time.Second, Volume: 1 << 20},
	} {
		a := &fakeAgent{name: "n6", version: "v0.10.0", apps: []string{"voip"}, networks: []string{"host"}}
		agents := map[string]*fakeAgent{"ran:9551": a, "n6:9551": a}
		sc := voipScenario(nil)
		sc.Timeline[0].Stop = stop

		c := New(sc, map[string]string{"ran": "ran:9551", "n6": "n6:9551"}, WithDialer(fakeDialer(agents)))
		err := c.Run(context.Background(), time.Second)
		c.Close()
		if err == nil || !strings.Contains(err.Error(), "stop.count/stop.volume are not supported") {
			t.Errorf("%s: error = %v, want stop-condition refusal", name, err)
		}
		if len(a.configured) != 0 {
			t.Errorf("%s: no flow should be configured, got %d", name, len(a.configured))
		}
	}
}

// TestAppSkewGateRefusesOldAgent: an agent that advertises no apps (an old
// loomd predating CapabilitiesResponse.apps) is refused before any Configure,
// with the design's actionable error shape.
func TestAppSkewGateRefusesOldAgent(t *testing.T) {
	var calls []string
	old := &fakeAgent{name: "n6", version: "v0.9.1", calls: &calls} // no apps, no networks
	newer := &fakeAgent{name: "ran", version: "v0.10.0", apps: []string{"voip"}, networks: []string{"host"}, calls: &calls}
	agents := map[string]*fakeAgent{"ran:9551": newer, "n6:9551": old}

	c := New(voipScenario(nil), map[string]string{"ran": "ran:9551", "n6": "n6:9551"}, WithDialer(fakeDialer(agents)))
	defer c.Close()

	err := c.Run(context.Background(), time.Second)
	if err == nil {
		t.Fatal("Run succeeded against an agent lacking apps")
	}
	want := `loomd at n6:9551 (v0.9.1) lacks app "voip"; run loom >= v0.10`
	if !strings.Contains(err.Error(), want) {
		t.Errorf("error = %q, want it to contain %q", err, want)
	}
	if len(old.configured)+len(newer.configured) != 0 {
		t.Errorf("gate must fail fast: %d flows were configured", len(old.configured)+len(newer.configured))
	}
	if len(c.Placed()) != 0 {
		t.Errorf("no flows should be placed after a refusal, got %d", len(c.Placed()))
	}
}

// TestAppSkewGateRefusesWrongSide: an agent that advertises the per-side
// capability lists is gated on the side it must run — a server-only slimmed
// build placed as the client agent is refused at provision time (union-only
// gating would pass it and fail confusingly at Configure with Unimplemented).
func TestAppSkewGateRefusesWrongSide(t *testing.T) {
	serverOnly := &fakeAgent{name: "ran", version: "v0.11.0",
		apps: []string{"voip"}, appsServer: []string{"voip"}, networks: []string{"host"}}
	full := &fakeAgent{name: "n6", version: "v0.11.0",
		apps: []string{"voip"}, appsClient: []string{"voip"}, appsServer: []string{"voip"},
		networks: []string{"host"}, dataPort: 40007}
	// The scenario runs ran→n6: ran must be the app CLIENT, which the
	// server-only build cannot run.
	agents := map[string]*fakeAgent{"ran:9551": serverOnly, "n6:9551": full}

	c := New(voipScenario(nil), map[string]string{"ran": "ran:9551", "n6": "n6:9551"}, WithDialer(fakeDialer(agents)))
	defer c.Close()

	err := c.Run(context.Background(), time.Second)
	if err == nil || !strings.Contains(err.Error(), `lacks app client "voip"`) {
		t.Fatalf("error = %v, want per-side refusal (lacks app client)", err)
	}
	if len(serverOnly.configured)+len(full.configured) != 0 {
		t.Errorf("gate must fail fast: %d flows were configured", len(serverOnly.configured)+len(full.configured))
	}
}

// TestAppSkewGateRefusesUnknownNetwork: pinning a netpath network the agent
// does not register is refused with the same actionable shape.
func TestAppSkewGateRefusesUnknownNetwork(t *testing.T) {
	a := &fakeAgent{name: "n6", version: "v0.10.0", apps: []string{"voip"}, networks: []string{"host"}}
	agents := map[string]*fakeAgent{"ran:9551": a, "n6:9551": a}

	c := New(voipScenario(map[string]any{"network": "dgram"}),
		map[string]string{"ran": "ran:9551", "n6": "n6:9551"}, WithDialer(fakeDialer(agents)))
	defer c.Close()

	err := c.Run(context.Background(), time.Second)
	if err == nil || !strings.Contains(err.Error(), `lacks network "dgram"; run loom >= v0.10`) {
		t.Fatalf("error = %v, want unknown-network refusal", err)
	}
}

// TestAppSpecsCarryNetwork: the flow param `network` travels as FlowSpec.network
// on both ends (and is gated); "" defaults to the agent's host stack.
func TestAppSpecsCarryNetwork(t *testing.T) {
	ev := scenario.Event{
		Name: "voice",
		Flow: scenario.Flow{Kind: "voip", Params: map[string]any{"network": "mem", "duration": "45s"}},
	}
	srv := appServerSpec(ev, appNetwork(ev), 3, 45*time.Second+appServerGrace)
	cli := appClientSpec(ev, appNetwork(ev), "10.0.0.2:40000", 3)
	if srv.GetNetwork() != "mem" || cli.GetNetwork() != "mem" {
		t.Errorf("networks = %q/%q, want mem/mem", srv.GetNetwork(), cli.GetNetwork())
	}
	// The `duration` flow-param convenience mirrors requesterSpec.
	if got := cli.GetDuration().AsDuration(); got != 45*time.Second {
		t.Errorf("client duration = %v, want 45s from the duration knob", got)
	}
	if got := srv.GetDuration().AsDuration(); got != 45*time.Second+appServerGrace {
		t.Errorf("server duration = %v, want 45s + grace", got)
	}
}

// appSample builds a telemetry sample carrying interval accounting plus voip
// AppMetrics, as an app-role agent emits.
func appSample(idx int64, bytes uint64, v *loomv1.VoipMetrics) *loomv1.TelemetrySample {
	s := sample(idx, bytes)
	s.App = &loomv1.AppMetrics{Kind: &loomv1.AppMetrics_Voip{Voip: v}}
	return s
}

// TestAggregatorFoldsAppMetrics: AppMetrics fold into the per-flow samples and
// the aggregate carries the client's snapshot (the initiating end's view) even
// when the server reports the interval first.
func TestAggregatorFoldsAppMetrics(t *testing.T) {
	cli := Placed{AgentAddr: "a", FlowID: "1", Role: AppClient, Event: "voice", From: "ran", To: "n6"}
	srv := Placed{AgentAddr: "b", FlowID: "1", Role: AppServer, Event: "voice", From: "ran", To: "n6"}
	tel := NewTelemetry(100 * time.Millisecond)
	tel.src = fakePlaced{flows: []Placed{cli, srv}}
	cap := &capture{}
	tel.AddObserver(cap)

	srvVoip := &loomv1.VoipMetrics{MosCq: 4.4, RFactor: 92.0, JitterMs: 1.2}
	cliVoip := &loomv1.VoipMetrics{MosCq: 4.1, RFactor: 86.5, JitterMs: 3.4, LossPct: 0.5, DiscardPct: 0.2, OwdMs: 12.3, OwdErrMs: 0.4, OwdMethod: "timesync"}
	tel.fold(srv, appSample(0, 96_000, srvVoip)) // server reports first
	tel.fold(cli, appSample(0, 95_000, cliVoip))
	tel.tryEmit(time.Now())

	cap.mu.Lock()
	defer cap.mu.Unlock()
	if len(cap.aggs) != 1 {
		t.Fatalf("emitted %d lines, want 1", len(cap.aggs))
	}
	a := cap.aggs[0]
	if a.App.GetVoip().GetMosCq() != 4.1 {
		t.Errorf("aggregate app = %v, want the client's snapshot (mos 4.1)", a.App)
	}
	// App ends fold as per-end totals: the client end lands in the rx bucket,
	// the server end in the tx bucket. These are both-direction media-plane
	// bytes (see foldLocked) — display buckets, not a loss basis.
	if a.RxBytes != 95_000 || a.TxBytes != 96_000 {
		t.Errorf("tx/rx = %d/%d, want 96000/95000", a.TxBytes, a.RxBytes)
	}
	perFlow := map[Role]float64{}
	for _, f := range a.Flows {
		perFlow[f.Role] = f.App.GetVoip().GetMosCq()
	}
	if perFlow[AppClient] != 4.1 || perFlow[AppServer] != 4.4 {
		t.Errorf("per-flow app snapshots = %v, want both ends' MOS", perFlow)
	}
}

// TestObserversRenderVoip: the text observer emits the compact MOS/QoE line
// and the JSON observer carries the AppMetrics verbatim (proto field names).
func TestObserversRenderVoip(t *testing.T) {
	a := Aggregate{
		At:           time.Now(),
		Event:        "voice",
		From:         "ran",
		To:           "n6",
		TxBitsPerSec: 87e3,
		RxBitsPerSec: 86e3,
		Flows:        []FlowSample{{Role: AppClient}, {Role: AppServer}},
		App: &loomv1.AppMetrics{Kind: &loomv1.AppMetrics_Voip{Voip: &loomv1.VoipMetrics{
			MosCq: 4.12, RFactor: 86.5, JitterMs: 3.41, LossPct: 0.5, DiscardPct: 0.21,
			OwdMs: 12.3, OwdErrMs: 0.4, OwdMethod: "timesync",
		}}},
	}
	var human, jsonOut bytes.Buffer
	NewTextObserver(&human).Observe(a)
	NewJSONObserver(&jsonOut).Observe(a)

	for _, want := range []string{"mos 4.12 (R 86.5)", "jitter 3.41ms", "loss 0.50%", "discard 0.21%", "owd 12.3±0.4ms (timesync)"} {
		if !strings.Contains(human.String(), want) {
			t.Errorf("text line missing %q: %q", want, human.String())
		}
	}
	// The JSON observer carries AppMetrics verbatim under "app" (proto field
	// names). protojson output spacing is deliberately unstable, so decode
	// rather than substring-match.
	var m map[string]any
	if err := json.Unmarshal(jsonOut.Bytes(), &m); err != nil {
		t.Fatalf("json observer output not JSON: %v: %q", err, jsonOut.String())
	}
	app, _ := m["app"].(map[string]any)
	voip, _ := app["voip"].(map[string]any)
	if voip == nil {
		t.Fatalf("json line missing app.voip: %q", jsonOut.String())
	}
	if voip["mos_cq"] != 4.12 || voip["owd_method"] != "timesync" {
		t.Errorf("app.voip = %v, want mos_cq 4.12 and owd_method timesync", voip)
	}

	// A non-app aggregate renders no MOS line and no app key.
	var plain, plainJSON bytes.Buffer
	NewTextObserver(&plain).Observe(Aggregate{At: time.Now(), TxBitsPerSec: 1e6, Flows: []FlowSample{{Role: Sender}}})
	NewJSONObserver(&plainJSON).Observe(Aggregate{At: time.Now(), Flows: []FlowSample{{Role: Sender}}})
	if strings.Contains(plain.String(), "mos") || strings.Contains(plainJSON.String(), `"app"`) {
		t.Errorf("non-app lines must carry no app fields: %q / %q", plain.String(), plainJSON.String())
	}

	// The summary renders one voip quality line per app end.
	sum := Aggregate{Flows: []FlowSample{
		{Event: "voice", From: "ran", To: "n6", Role: AppClient, App: a.App},
		{Event: "voice", From: "ran", To: "n6", Role: AppServer, App: &loomv1.AppMetrics{Kind: &loomv1.AppMetrics_Voip{Voip: &loomv1.VoipMetrics{MosCq: 4.4, RFactor: 92}}}},
	}}
	out := sum.Summary("call", time.Minute, false, false)
	if !strings.Contains(out, "voip voice") || !strings.Contains(out, "app-client") || !strings.Contains(out, "app-server") {
		t.Errorf("summary missing voip lines: %q", out)
	}
	if !strings.Contains(out, "mos 4.12 (R 86.5)") || !strings.Contains(out, "mos 4.40 (R 92.0)") {
		t.Errorf("summary missing per-end MOS: %q", out)
	}
}
