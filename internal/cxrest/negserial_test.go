// same setting as the app (see main.go)
//
//go:debug x509negativeserial=1
package cxrest

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"math/big"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// negSerialCert makes a self-signed certificate whose serial number is
// negative, like the one on the 6100 in the field. Go refuses to create such
// a certificate, so a normal one is made and the serial's sign bit flipped.
func negSerialCert(t *testing.T) tls.Certificate {
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(0x55), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	old := []byte{0xa0, 0x03, 0x02, 0x01, 0x02, 0x02, 0x01, 0x55} // version 3, serial 0x55
	if bytes.Count(der, old) != 1 {
		t.Fatal("serial not found in certificate")
	}
	der = bytes.Replace(der, old, []byte{0xa0, 0x03, 0x02, 0x01, 0x02, 0x02, 0x01, 0xd5}, 1)
	if c, err := x509.ParseCertificate(der); err != nil || c.SerialNumber.Sign() >= 0 {
		t.Fatalf("want a negative serial, got %v %v", c, err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: k}
}

func TestNegativeSerialCertificate(t *testing.T) {
	f := &fakeCX{}
	srv := httptest.NewUnstartedServer(f.handler())
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{negSerialCert(t)}}
	srv.StartTLS()
	defer srv.Close()
	err := Push(context.Background(), srv.URL, "admin", "pw", "secondary", "x.swi", strings.NewReader("img"), 3, nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(f.got) != "img" {
		t.Fatalf("got %q", f.got)
	}
}
