// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package emul

import "fmt"

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
// G.711 (the default codec) is 160 bytes every 20 ms. Params: codec (g711|g729),
// frame_size, interval.
func voipCall(p Params) (BehaviorScript, error) {
	frame, interval := "160", "20ms"
	switch str(p, "codec", "g711") {
	case "g711":
		// defaults above
	case "g729":
		frame, interval = "20", "20ms"
	default:
		return nil, fmt.Errorf("voip-call: unknown codec %q", p["codec"])
	}
	size, err := sizeParam(p, "frame_size", frame)
	if err != nil {
		return nil, err
	}
	think, err := durParam(p, "interval", interval)
	if err != nil {
		return nil, err
	}
	return BehaviorScript{{Size: size, Think: think}}, nil
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
