package tlspin

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func get(t *testing.T, url string, p *Pin) error {
	t.Helper()
	c := &http.Client{Transport: &http.Transport{TLSClientConfig: p.Config()}}
	resp, err := c.Get(url)
	if err == nil {
		resp.Body.Close()
	}
	return err
}

func TestPinTrustOnFirstUseThenEnforced(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	first := &Pin{}
	if err := get(t, srv.URL, first); err != nil || first.Seen() == "" {
		t.Fatalf("first use: %v %q", err, first.Seen())
	}
	if err := get(t, srv.URL, &Pin{Want: first.Seen()}); err != nil {
		t.Fatalf("same certificate refused: %v", err)
	}
	other := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("request reached a server with a different certificate")
	}))
	other.TLS = &tls.Config{Certificates: []tls.Certificate{newCert(t)}}
	other.StartTLS()
	defer other.Close()
	if err := get(t, other.URL, &Pin{Want: first.Seen()}); !errors.Is(err, ErrChanged) {
		t.Fatalf("changed certificate: %v", err)
	}
}

// newCert makes a throwaway self-signed certificate: a "different switch".
func newCert(t *testing.T) tls.Certificate {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := x509.Certificate{SerialNumber: big.NewInt(7), Subject: pkix.Name{CommonName: "other-switch"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, &tpl, &tpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: k}
}
