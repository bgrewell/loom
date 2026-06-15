// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package emul

import (
	"fmt"
	"math"

	"github.com/bgrewell/loom/core/units"
)

// The launch emulations. Each compiles scenario params to a BehaviorScript; the
// Runner repeats that script until the flow's stop condition. They model the
// client-side traffic *shape*; a plain receiver absorbs and accounts it.
func init() {
	Registry.Register("voip-call", voipCall)
	Registry.Register("https-browse", httpsBrowse)
	Registry.Register("prometheus-sender", prometheusSender)
	Registry.Register("ssh-session", sshSession)
}

// voipCall: constant-bit-rate UDP, one media frame per packetization interval.
// The codec sets the payload bitrate and ptime sets the interval; the frame size
// is bitrate × ptime (so g711 at 20 ms = 160 B). Knobs:
//
//	codec       g711 (64 kbps, default) | g729 (8 kbps) | opus (~32 kbps)
//	ptime       packetization interval (default 20ms; alias: interval)
//	frame_size  override the computed frame size (e.g. "160", "200..200")
//	duration    call length (handled by the controller; or use stop.after)
func voipCall(p Params) (BehaviorScript, error) {
	var bytesPerSec float64
	switch str(p, "codec", "g711") {
	case "g711":
		bytesPerSec = 8000 // 64 kbps
	case "g729":
		bytesPerSec = 1000 // 8 kbps
	case "opus":
		bytesPerSec = 4000 // ~32 kbps (opus is variable; a sane default)
	default:
		return nil, fmt.Errorf("voip-call: unknown codec %q (want g711|g729|opus)", p["codec"])
	}
	ptime, err := units.ParseDuration(str(p, "ptime", str(p, "interval", "20ms")))
	if err != nil {
		return nil, fmt.Errorf("voip-call ptime: %w", err)
	}
	if ptime <= 0 {
		return nil, fmt.Errorf("voip-call: ptime must be > 0")
	}
	size := Constant(math.Round(bytesPerSec * ptime.Seconds()))
	if v := str(p, "frame_size", ""); v != "" {
		if size, err = SizeDist(v); err != nil {
			return nil, fmt.Errorf("voip-call frame_size: %w", err)
		}
	}
	return BehaviorScript{{Size: size, Think: Constant(float64(ptime))}}, nil
}

// httpsBrowse: a keep-alive session of N object fetches with think-time gaps —
// the bursty shape of a user reading pages. Params: objects, object_size, think.
func httpsBrowse(p Params) (BehaviorScript, error) {
	n, err := intParam(p, "objects", 10)
	if err != nil {
		return nil, err
	}
	if n < 1 {
		n = 1
	}
	size, err := sizeParam(p, "object_size", "8KB..512KB")
	if err != nil {
		return nil, err
	}
	think, err := durParam(p, "think", "200ms..2s")
	if err != nil {
		return nil, err
	}
	script := make(BehaviorScript, n)
	for i := range script {
		script[i] = Step{Size: size, Think: think}
	}
	return script, nil
}

// prometheusSender: periodic remote-write POSTs at the scrape interval. Params:
// scrape (interval), batch_size.
func prometheusSender(p Params) (BehaviorScript, error) {
	size, err := sizeParam(p, "batch_size", "64KB")
	if err != nil {
		return nil, err
	}
	think, err := durParam(p, "scrape", "15s")
	if err != nil {
		return nil, err
	}
	return BehaviorScript{{Size: size, Think: think}}, nil
}

// sshSession: interactive keystrokes (tiny sends with inter-key timing), then an
// optional bulk transfer (scp). Params: keys, key_size, interkey, bulk.
func sshSession(p Params) (BehaviorScript, error) {
	keys, err := intParam(p, "keys", 100)
	if err != nil {
		return nil, err
	}
	if keys < 1 {
		keys = 1
	}
	keySize, err := sizeParam(p, "key_size", "1..64")
	if err != nil {
		return nil, err
	}
	interkey, err := durParam(p, "interkey", "80ms..300ms")
	if err != nil {
		return nil, err
	}
	script := make(BehaviorScript, 0, keys+1)
	for i := 0; i < keys; i++ {
		script = append(script, Step{Size: keySize, Think: interkey})
	}
	// Optional bulk transfer (e.g. scp); default 0 = none.
	if bulk := str(p, "bulk", "0"); bulk != "0" && bulk != "" {
		bulkSize, err := sizeParam(p, "bulk", bulk)
		if err != nil {
			return nil, err
		}
		script = append(script, Step{Size: bulkSize, Think: Constant(0)})
	}
	return script, nil
}
