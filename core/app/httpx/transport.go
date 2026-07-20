// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package httpx

import (
	"crypto/tls"
	"errors"
	"net/http"
	"time"

	"github.com/bgrewell/loom/core/app"
	"github.com/bgrewell/loom/core/netpath"
)

// NewTransport builds the HTTP transport loom's web-driven apps share (the
// "http" client here and the "video" player in core/app/vidstream): an
// http.Transport whose DialContext rides the injected netpath.Network (with
// the httptrace connect hooks preserved, see traceDial), configured from the
// shared client-side parameter grammar read from p — tls, h2, host, tls_ca,
// tls_insecure, documented in this package's doc. It returns the transport,
// the URL scheme to use ("http" or "https"), and the Host/SNI override
// (empty when the host param is unset).
//
// Malformed parameter values accumulate on p per the app.Params discipline
// (the caller checks p.Err()); cross-parameter consistency failures (h2
// without tls, tls_ca outside tls, an undecodable tls_ca) are joined into the
// returned error. The transport is returned even on error so callers can
// keep their error-accumulation flow; they must not use it when either error
// source is non-nil.
func NewTransport(n netpath.Network, p *app.Params) (tr *http.Transport, scheme, host string, err error) {
	var (
		useTLS   = p.GetBool("tls", false)
		useH2    = p.GetBool("h2", false)
		caB64    = p.GetString("tls_ca", "")
		insecure = p.GetBool("tls_insecure", false)
	)
	host = p.GetString("host", "")
	var errs []error
	if useH2 && !useTLS {
		errs = append(errs, errors.New(`param "h2": requires tls=true (h2c is not supported)`))
	}
	if !useTLS && (caB64 != "" || insecure) {
		errs = append(errs, errors.New(`params "tls_ca"/"tls_insecure": only meaningful with tls=true`))
	}
	var tcfg *tls.Config
	if useTLS {
		tcfg = &tls.Config{MinVersion: tls.VersionTLS12, ServerName: host}
		if caB64 != "" {
			pool, cerr := rootsFromParam(caB64)
			if cerr != nil {
				errs = append(errs, cerr)
			}
			tcfg.RootCAs = pool
		}
		if insecure {
			// Lab shortcut ONLY, explicitly opted into via tls_insecure:
			// verification is otherwise always on (pin via tls_ca).
			tcfg.InsecureSkipVerify = true
		}
	}
	scheme = "http"
	if useTLS {
		scheme = "https"
	}
	// The Transport rides the injected Network. traceDial forwards the
	// standard httptrace ConnectStart/ConnectDone hooks around the injected
	// dial: net.Dialer fires them itself, but a netpath.Network (memory
	// fabric, datapath-backed stack) has no obligation to, and the connect
	// timing must not silently vanish on non-kernel networks.
	tr = &http.Transport{
		DialContext:           traceDial(n.DialContext),
		TLSClientConfig:       tcfg,
		ForceAttemptHTTP2:     useH2, // explicit: custom DialContext+TLSClientConfig otherwise disable h2
		DisableCompression:    true,  // synthetic bodies are incompressible; keep sizes exact
		MaxIdleConns:          8,
		IdleConnTimeout:       60 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
	return tr, scheme, host, errors.Join(errs...)
}
