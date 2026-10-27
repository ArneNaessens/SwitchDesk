// Package tlspin checks a switch's HTTPS certificate against a pinned
// fingerprint. Switches use self-signed certificates, so the usual CA check
// can't work; instead the first certificate seen is remembered (like an SSH
// host key) and any later change is refused.
package tlspin

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"sync"
)

// ErrChanged is returned when the switch presents a different certificate
// than the one pinned on first use.
var ErrChanged = errors.New("the switch's HTTPS certificate changed since last connection")

// Fingerprint is the SHA-256 of the certificate's DER bytes, in the same
// "SHA256:base64" form as SSH host key fingerprints.
func Fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])
}

// Pin holds the expected fingerprint and records the one actually seen.
type Pin struct {
	Want string // empty: trust on first use

	mu   sync.Mutex
	seen string
}

// Seen returns the fingerprint of the last certificate the peer presented.
func (p *Pin) Seen() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.seen
}

// Config returns a TLS config for a self-signed switch: chain and name checks
// are replaced by a fingerprint check against p.Want. With Want empty any
// certificate is accepted and remembered, so the caller can store it.
func (p *Pin) Config() *tls.Config {
	return &tls.Config{
		// The certificate is self-signed, so chain verification is replaced
		// by the fingerprint check below, which runs on every handshake.
		InsecureSkipVerify: true, // codeql[go/disabled-certificate-check]
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			if len(raw) == 0 {
				return errors.New("the switch sent no certificate")
			}
			fp := Fingerprint(raw[0])
			p.mu.Lock()
			p.seen = fp
			p.mu.Unlock()
			if p.Want != "" && p.Want != fp {
				return ErrChanged
			}
			return nil
		},
	}
}
