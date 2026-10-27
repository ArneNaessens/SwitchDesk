package cxrest

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type fakeCX struct {
	mu       sync.Mutex
	got      []byte
	bank     string
	name     string
	loggedIn bool
	logouts  int
	noLatest bool
}

func (f *fakeCX) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/rest", func(w http.ResponseWriter, r *http.Request) {
		if f.noLatest {
			http.NotFound(w, r)
			return
		}
		io.WriteString(w, `{"latest":{"version":"10.13","prefix":"/rest/v10.13"},"v10.09":{"version":"10.09","prefix":"/rest/v10.09"}}`)
	})
	mux.HandleFunc("/rest/v10.13/login", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.Form.Get("username") != "admin" || r.Form.Get("password") != "pw" {
			w.WriteHeader(401)
			return
		}
		if r.Header.Get("x-use-csrf-token") == "true" {
			w.Header().Set("X-Csrf-Token", "tok123")
		}
		http.SetCookie(w, &http.Cookie{Name: "id", Value: "sess", Path: "/"})
		f.mu.Lock()
		f.loggedIn = true
		f.mu.Unlock()
	})
	mux.HandleFunc("/rest/v10.13/logout", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.logouts++
		f.mu.Unlock()
	})
	mux.HandleFunc("/rest/v10.13/firmware", func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie("id"); err != nil || c.Value != "sess" || r.Header.Get("x-csrf-token") != "tok123" {
			w.WriteHeader(401)
			return
		}
		file, hdr, err := r.FormFile("fileupload")
		if err != nil {
			w.WriteHeader(400)
			io.WriteString(w, "Error processing the form.")
			return
		}
		data, _ := io.ReadAll(file)
		f.mu.Lock()
		f.got, f.bank, f.name = data, r.URL.Query().Get("image"), hdr.Filename
		f.mu.Unlock()
	})
	return mux
}

func TestPush(t *testing.T) {
	f := &fakeCX{}
	srv := httptest.NewTLSServer(f.handler())
	defer srv.Close()
	img := bytes.Repeat([]byte("firmware"), 100000)
	var last int64
	err := Push(context.Background(), srv.URL, nil, "admin", "pw", "secondary", "/lib/PL_10_16_1060.swi", bytes.NewReader(img), int64(len(img)), func(s, _ int64) { last = s })
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !bytes.Equal(f.got, img) || f.bank != "secondary" || f.name != "PL_10_16_1060.swi" {
		t.Fatalf("switch got %d bytes into %q as %q", len(f.got), f.bank, f.name)
	}
	if last != int64(len(img)) {
		t.Fatalf("progress ended at %d", last)
	}
	if f.logouts != 1 {
		t.Fatalf("logouts: %d", f.logouts)
	}
}

func TestPushBadPassword(t *testing.T) {
	srv := httptest.NewTLSServer((&fakeCX{}).handler())
	defer srv.Close()
	err := Push(context.Background(), srv.URL, nil, "admin", "nope", "secondary", "x.swi", strings.NewReader("x"), 1, nil)
	if err == nil || !strings.Contains(err.Error(), "refused the login") {
		t.Fatalf("got %v", err)
	}
}

func TestPushNoVersionList(t *testing.T) {
	// older releases have no /rest index; login walks the known versions
	f := &fakeCX{noLatest: true}
	srv := httptest.NewTLSServer(f.handler())
	defer srv.Close()
	if err := Push(context.Background(), srv.URL, nil, "admin", "pw", "primary", "x.swi", strings.NewReader("abc"), 3, nil); err != nil {
		t.Fatal(err)
	}
	if string(f.got) != "abc" || f.bank != "primary" {
		t.Fatalf("got %q into %q", f.got, f.bank)
	}
}

func TestNoREST(t *testing.T) {
	err := Push(context.Background(), "https://127.0.0.1:1", nil, "a", "b", "primary", "x", strings.NewReader(""), 0, nil)
	if err == nil || !strings.Contains(err.Error(), "REST API didn't answer") {
		t.Fatalf("got %v", err)
	}
}
