// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package emul

// Mode is how an emulation's traffic is carried on the wire.
type Mode int

const (
	// ModePush is one-directional: the Runner transmits the script's bytes over a
	// Tx datapath and a plain receiver absorbs them. This is the default for any
	// emulation that does not register a different mode.
	ModePush Mode = iota
	// ModeRequestResponse is bidirectional request/response: a Requester drives
	// the script, asking a Responder for each object, and the download bytes flow
	// back over a real connection (TCP or UDP). See reqresp.go.
	ModeRequestResponse
)

// behavior records how an emulation is carried beyond its BehaviorScript. The
// zero value (ModePush, empty transport) applies to push emulations, which need
// no extra metadata.
type behavior struct {
	mode      Mode
	transport string // request/response transport: "tcp" | "udp"
}

// emulationModes maps an emulation name to its carriage behavior. Push
// emulations are absent (they take the zero value). Keep in sync with the
// registrations in emulations.go.
var emulationModes = map[string]behavior{
	"https-browse": {mode: ModeRequestResponse, transport: "tcp"},
}

// ModeOf reports how the named emulation is carried. Unknown or push emulations
// return ModePush.
func ModeOf(name string) Mode { return emulationModes[name].mode }

// DefaultTransport returns the request/response transport ("tcp"|"udp") for a
// ModeRequestResponse emulation, or "" for push emulations. A scenario may
// override it via a flow `transport` param.
func DefaultTransport(name string) string { return emulationModes[name].transport }
