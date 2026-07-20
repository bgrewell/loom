// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package metrics_test

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/bgrewell/loom/core/metrics"
	"github.com/bgrewell/loom/core/quality/emodel"
	"github.com/bgrewell/loom/core/rtp"
)

// populatedVoIP fills every VoIP field with a distinct, JSON-stable value.
func populatedVoIP() metrics.VoIP {
	return metrics.VoIP{
		Codec:      "pcmu",
		TxPackets:  3000,
		RxPackets:  2985,
		Lost:       15,
		Duplicates: 2,
		Reordered:  3,
		LossPct:    0.5,
		DiscardPct: 0.25,
		JitterMs:   4.5,
		RTTMs:      22.5,
		OWDMs:      11.25,
		OWDErrMs:   0.5,
		OWDMethod:  "timesync",
		BurstR:     1.5,
		RFactor:    88.5,
		MOSCQ:      4.25,
		EModel: emodel.Components{
			Ro: 94.77, Is: 1.41, Idte: 0.5, Idle: 0.25,
			Idd: 0, Id: 0.75, Ie: 0, IeEff: 4.5, A: 0, R: 88.5,
		},
		RemoteRFactor: 87.5,
		RemoteMOSCQ:   4.2,
		RemoteBye:     true,
		MediaGaps: []rtp.Gap{{
			Start:       time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC),
			End:         time.Date(2026, 7, 17, 12, 0, 0, 240e6, time.UTC),
			PacketsLost: 12,
		}},
	}
}

func populatedHTTP() metrics.HTTP {
	return metrics.HTTP{
		Requests:       1200,
		Errors:         3,
		ConnectMs:      8.5,
		TLSHandshakeMs: 24.5,
		TTFBMsP50:      31.25,
		TTFBMsP95:      88.5,
		ObjectMsP95:    210.5,
		GoodputMbps:    42.5,
	}
}

func populatedVideo() metrics.Video {
	return metrics.Video{
		SegmentsFetched: 90,
		Stalls:          2,
		StartupMs:       480.5,
		StallTimeMs:     1800.25,
		RebufferRatio:   0.01,
		BufferMs:        12000,
		AvgBitrateKbps:  2350.5,
		RepSwitchesUp:   4,
		RepSwitchesDown: 2,
		StallEvents: []rtp.Gap{{
			Start:       time.Date(2026, 7, 17, 12, 1, 0, 0, time.UTC),
			End:         time.Date(2026, 7, 17, 12, 1, 1, 800e6, time.UTC),
			PacketsLost: 0,
		}},
	}
}

// TestKind pins the kind constants and each snapshot's Kind method, by value
// and through the Snapshot interface from a pointer.
func TestKind(t *testing.T) {
	cases := []struct {
		name     string
		byValue  metrics.Snapshot
		byPtr    metrics.Snapshot
		want     string
		constant string
	}{
		{"voip", metrics.VoIP{}, &metrics.VoIP{}, "voip", metrics.KindVoIP},
		{"http", metrics.HTTP{}, &metrics.HTTP{}, "http", metrics.KindHTTP},
		{"video", metrics.Video{}, &metrics.Video{}, "video", metrics.KindVideo},
	}
	seen := map[string]bool{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.constant != tc.want {
				t.Errorf("kind constant = %q, want %q", tc.constant, tc.want)
			}
			if got := tc.byValue.Kind(); got != tc.want {
				t.Errorf("value Kind() = %q, want %q", got, tc.want)
			}
			if got := tc.byPtr.Kind(); got != tc.want {
				t.Errorf("pointer Kind() = %q, want %q", got, tc.want)
			}
		})
		if seen[tc.want] {
			t.Errorf("kind %q not unique", tc.want)
		}
		seen[tc.want] = true
	}
}

// fakeEngine stands in for an app engine: it exposes a snapshot through the
// Source seam the way core/app/voip will.
type fakeEngine struct{ snap metrics.Snapshot }

func (f fakeEngine) Metrics() metrics.Snapshot { return f.snap }

// TestSourceDispatch exercises the consumer side of the seam: discover
// Source by type assertion (the flowTCPInfo pattern), then dispatch on Kind.
func TestSourceDispatch(t *testing.T) {
	var engine any = fakeEngine{snap: populatedVoIP()}

	src, ok := engine.(metrics.Source)
	if !ok {
		t.Fatal("fakeEngine does not satisfy metrics.Source")
	}
	snap := src.Metrics()
	if snap.Kind() != metrics.KindVoIP {
		t.Fatalf("Kind() = %q, want %q", snap.Kind(), metrics.KindVoIP)
	}
	v, ok := snap.(metrics.VoIP)
	if !ok {
		t.Fatalf("snapshot has kind %q but is %T, not metrics.VoIP", snap.Kind(), snap)
	}
	if v.MOSCQ != 4.25 || v.OWDMethod != "timesync" {
		t.Errorf("snapshot lost data through the seam: %+v", v)
	}
}

// TestGoldenJSON pins the exact serialized form of every snapshot kind —
// field names, order, embedded emodel.Components and rtp.Gap tags, and
// time encoding — so any wire-visible change is a deliberate golden update,
// never an accident.
func TestGoldenJSON(t *testing.T) {
	cases := []struct {
		name string
		in   metrics.Snapshot
		want string
	}{
		{
			name: "voip",
			in:   populatedVoIP(),
			want: `{"codec":"pcmu","tx_packets":3000,"rx_packets":2985,"lost":15,` +
				`"duplicates":2,"reordered":3,"loss_pct":0.5,"discard_pct":0.25,` +
				`"jitter_ms":4.5,"rtt_ms":22.5,"owd_ms":11.25,"owd_err_ms":0.5,` +
				`"owd_method":"timesync","burst_r":1.5,"r_factor":88.5,"mos_cq":4.25,` +
				`"emodel":{"ro":94.77,"is":1.41,"idte":0.5,"idle":0.25,"idd":0,` +
				`"id":0.75,"ie":0,"ie_eff":4.5,"a":0,"r":88.5},` +
				`"remote_r_factor":87.5,"remote_mos_cq":4.2,"remote_bye":true,` +
				`"media_gaps":[{"start":"2026-07-17T12:00:00Z",` +
				`"end":"2026-07-17T12:00:00.24Z","packets_lost":12}]}`,
		},
		{
			name: "http",
			in:   populatedHTTP(),
			want: `{"requests":1200,"errors":3,"connect_ms":8.5,` +
				`"tls_handshake_ms":24.5,"ttfb_ms_p50":31.25,"ttfb_ms_p95":88.5,` +
				`"object_ms_p95":210.5,"goodput_mbps":42.5}`,
		},
		{
			name: "video",
			in:   populatedVideo(),
			want: `{"segments_fetched":90,"stalls":2,"startup_ms":480.5,` +
				`"stall_time_ms":1800.25,"rebuffer_ratio":0.01,"buffer_ms":12000,` +
				`"avg_bitrate_kbps":2350.5,"rep_switches_up":4,"rep_switches_down":2,` +
				`"stall_events":[{"start":"2026-07-17T12:01:00Z",` +
				`"end":"2026-07-17T12:01:01.8Z","packets_lost":0}]}`,
		},
		// Zero values pin two more contracts: numeric/string fields are
		// always present (no omitempty), and the gap/event slices vanish
		// when empty instead of serializing as null.
		{
			name: "voip-zero",
			in:   metrics.VoIP{},
			want: `{"codec":"","tx_packets":0,"rx_packets":0,"lost":0,` +
				`"duplicates":0,"reordered":0,"loss_pct":0,"discard_pct":0,` +
				`"jitter_ms":0,"rtt_ms":0,"owd_ms":0,"owd_err_ms":0,` +
				`"owd_method":"","burst_r":0,"r_factor":0,"mos_cq":0,` +
				`"emodel":{"ro":0,"is":0,"idte":0,"idle":0,"idd":0,"id":0,` +
				`"ie":0,"ie_eff":0,"a":0,"r":0},` +
				`"remote_r_factor":0,"remote_mos_cq":0,"remote_bye":false}`,
		},
		{
			name: "http-zero",
			in:   metrics.HTTP{},
			want: `{"requests":0,"errors":0,"connect_ms":0,"tls_handshake_ms":0,` +
				`"ttfb_ms_p50":0,"ttfb_ms_p95":0,"object_ms_p95":0,"goodput_mbps":0}`,
		},
		{
			name: "video-zero",
			in:   metrics.Video{},
			want: `{"segments_fetched":0,"stalls":0,"startup_ms":0,` +
				`"stall_time_ms":0,"rebuffer_ratio":0,"buffer_ms":0,` +
				`"avg_bitrate_kbps":0,"rep_switches_up":0,"rep_switches_down":0}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := json.Marshal(tc.in)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if string(got) != tc.want {
				t.Errorf("golden mismatch\n got: %s\nwant: %s", got, tc.want)
			}
		})
	}
}

// TestJSONRoundTrip verifies every snapshot survives marshal → unmarshal
// unchanged, so the JSON form is lossless, not display-only.
func TestJSONRoundTrip(t *testing.T) {
	t.Run("voip", func(t *testing.T) {
		in := populatedVoIP()
		b, err := json.Marshal(in)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var out metrics.VoIP
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if !reflect.DeepEqual(in, out) {
			t.Errorf("round trip changed snapshot\n in: %+v\nout: %+v", in, out)
		}
	})
	t.Run("http", func(t *testing.T) {
		in := populatedHTTP()
		b, err := json.Marshal(in)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var out metrics.HTTP
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if !reflect.DeepEqual(in, out) {
			t.Errorf("round trip changed snapshot\n in: %+v\nout: %+v", in, out)
		}
	})
	t.Run("video", func(t *testing.T) {
		in := populatedVideo()
		b, err := json.Marshal(in)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var out metrics.Video
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if !reflect.DeepEqual(in, out) {
			t.Errorf("round trip changed snapshot\n in: %+v\nout: %+v", in, out)
		}
	})
}
