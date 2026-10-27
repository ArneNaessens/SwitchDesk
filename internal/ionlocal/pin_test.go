package ionlocal

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

// fakeLogin answers like a switch that asks for the password in plaintext.
func fakeLogin(t *testing.T, redirect string, sent *int32) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/":
			http.Redirect(w, r, redirect, http.StatusFound)
		case strings.HasSuffix(r.URL.Path, "/device/wcd"):
			w.Write([]byte("<ResponseData><EncryptionSetting><passwEncryptEnable>0</passwEncryptEnable></EncryptionSetting></ResponseData>"))
		case strings.HasSuffix(r.URL.Path, "/config/system.xml"):
			if strings.Contains(r.URL.RawQuery, "password=") {
				atomic.AddInt32(sent, 1)
			}
			w.Write([]byte("<ResponseData><ActionStatus><statusCode>0</statusCode></ActionStatus></ResponseData>"))
		default:
			w.WriteHeader(404)
		}
	}))
}

// A switch that used encrypted login before can't be talked into plaintext.
func TestLoginRefusesDowngrade(t *testing.T) {
	var sent int32
	srv := fakeLogin(t, "/cs1a2b3c4d/hpe/config/login.htm", &sent)
	defer srv.Close()
	c := New(strings.TrimPrefix(srv.URL, "http://"), false, nil)
	c.RequireEncryption = true
	if err := c.Login(context.Background(), "admin", "secret"); !errors.Is(err, ErrDowngrade) || sent != 0 {
		t.Fatalf("err=%v password sent %d times", err, sent)
	}
}

// The session folder the switch redirects to must look like one; anything
// else is refused before any login request.
func TestDiscoverRejectsOddSessionPath(t *testing.T) {
	var sent int32
	for _, p := range []string{"/evil/path/hpe/config/login.htm", "/cs1/../x/hpe/", "/notasession/hpe/config/login.htm"} {
		srv := fakeLogin(t, p, &sent)
		err := New(strings.TrimPrefix(srv.URL, "http://"), false, nil).Login(context.Background(), "admin", "secret")
		srv.Close()
		if !errors.Is(err, ErrNotLocal) {
			t.Errorf("%s: %v", p, err)
		}
	}
	if sent != 0 {
		t.Fatalf("password sent %d times", sent)
	}
}

// Over HTTPS, a certificate that doesn't match the pin stops the sign-in.
func TestLoginRefusesChangedCertificate(t *testing.T) {
	var hits int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { atomic.AddInt32(&hits, 1) }))
	defer srv.Close()
	c := New(strings.TrimPrefix(srv.URL, "https://"), true, &tlspin.Pin{Want: "SHA256:someotherswitch"})
	if err := c.Login(context.Background(), "admin", "secret"); !errors.Is(err, tlspin.ErrChanged) || hits != 0 {
		t.Fatalf("err=%v hits=%d", err, hits)
	}
}
