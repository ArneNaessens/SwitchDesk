package cxrest

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jaba0x/switchdesk/internal/tlspin"
)

// The admin password is never sent to a switch whose certificate doesn't
// match the pin.
func TestPushRefusesChangedCertificate(t *testing.T) {
	var logins int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/login") {
			atomic.AddInt32(&logins, 1)
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()
	err := Push(context.Background(), srv.URL, &tlspin.Pin{Want: "SHA256:someotherswitch"}, "admin", "pw", "primary", "x.swi", strings.NewReader("x"), 1, nil)
	if !errors.Is(err, tlspin.ErrChanged) || atomic.LoadInt32(&logins) != 0 {
		t.Fatalf("err=%v logins=%d", err, logins)
	}
	// first use: accepted and the certificate is reported for pinning
	pin := &tlspin.Pin{}
	Push(context.Background(), srv.URL, pin, "admin", "pw", "primary", "x.swi", strings.NewReader("x"), 1, nil)
	if pin.Seen() == "" {
		t.Fatal("first-use fingerprint not recorded")
	}
}
