// Copyright 2026 Benjamin Grewell
// SPDX-License-Identifier: Apache-2.0

package httpx

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/netip"
	"time"
)

// certValidity is the generated certificate's lifetime. Origins are ephemeral
// lab fixtures rebuilt per flow, so a long-lived cert only papers over clock
// skew between test hosts; a year is ample.
const certValidity = 365 * 24 * time.Hour

// selfSigned generates the origin's self-signed serving certificate: ECDSA
// P-256, valid for localhost/loopback, the wildcard "*" and the memory
// fabric's "mem" literal, plus extraHost (the origin's host param) and the
// bind address when it is a specific IP. It is its own CA (IsCA — required
// for clients to pin it as a root), which is the standard shape for a pinned
// self-signed cert (httptest does the same). Returns the tls.Certificate and
// the PEM the origin publishes via CertificatePEM.
func selfSigned(extraHost string, bind netip.Addr) (tls.Certificate, []byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("httpx: generate key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("httpx: serial: %w", err)
	}
	tmpl := x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "loom httpx origin", Organization: []string{"loom"}},
		NotBefore:             time.Now().Add(-time.Hour), // tolerate modest clock skew
		NotAfter:              time.Now().Add(certValidity),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              []string{"localhost", "*", "mem"},
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
	}
	if extraHost != "" {
		if ip, perr := netip.ParseAddr(extraHost); perr == nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip.AsSlice())
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, extraHost)
		}
	}
	if bind.IsValid() && !bind.IsUnspecified() && !bind.IsLoopback() {
		tmpl.IPAddresses = append(tmpl.IPAddresses, bind.AsSlice())
	} else {
		// The origin normally binds the unspecified address (listenRange uses
		// an empty host), so the machine's routable IPs would never appear as
		// SANs — and IP verification uses IPAddresses only (no wildcard), so a
		// tls_ca-pinned client dialing this host's IP from another box would
		// fail x509 verification out of the box (design §13's normal two-host
		// deployment). Enumerate the interfaces' unicast addresses instead;
		// hosts whose addresses change after build still need host= on both
		// sides.
		if ifAddrs, aerr := net.InterfaceAddrs(); aerr == nil {
			for _, ia := range ifAddrs {
				ipn, ok := ia.(*net.IPNet)
				if !ok || ipn.IP.IsLoopback() {
					continue
				}
				tmpl.IPAddresses = append(tmpl.IPAddresses, ipn.IP)
			}
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("httpx: create certificate: %w", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("httpx: marshal key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("httpx: key pair: %w", err)
	}
	return cert, certPEM, nil
}

// rootsFromParam decodes the tls_ca param — base64 (std encoding) of one or
// more PEM certificates — into a pinned root pool. See the package doc for
// why base64 over raw PEM or a file path: the value must travel inside the
// wire params map with no shared filesystem or newline mangling.
func rootsFromParam(b64 string) (*x509.CertPool, error) {
	pemBytes, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, fmt.Errorf("httpx: param %q: not valid base64: %w", "tls_ca", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemBytes) {
		return nil, errors.New(`httpx: param "tls_ca": no PEM certificates found after base64 decode`)
	}
	return pool, nil
}
