package core

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"strconv"
	"sync"
	"testing"

	"github.com/jaba0x/switchdesk/internal/sshx"
	"github.com/jaba0x/switchdesk/internal/store"
	"golang.org/x/crypto/ssh"
)

// impostor accepts SSH connections with its own host key and records any
// password it is sent.
func impostor(t *testing.T) (addr string, got func() []string) {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(priv)
	var mu sync.Mutex
	var pw []string
	cfg := &ssh.ServerConfig{PasswordCallback: func(c ssh.ConnMetadata, p []byte) (*ssh.Permissions, error) {
		mu.Lock()
		pw = append(pw, string(p))
		mu.Unlock()
		return nil, errors.New("no")
	}}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go ssh.NewServerConn(c, cfg)
		}
	}()
	return ln.Addr().String(), func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), pw...) }
}

// After a host key change, Test connection on the existing switch must not
// send the password to whoever answers with a different key.
func TestTestConnectionKeepsHostKeyPin(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cred, _ := st.SaveCredential(store.Credential{Name: "c", Username: "admin", Password: "dummy-secret"})
	addr, got := impostor(t)
	host, p, _ := net.SplitHostPort(addr)
	port, _ := strconv.Atoi(p)
	sw, _ := st.SaveSwitch(store.Switch{Host: host, Port: port, CredentialID: cred.ID})
	st.SetHostKey(sw.ID, "SHA256:the-real-switch")
	sw, _ = st.Switch(sw.ID)
	m := New(st, nil)

	// the dialog may even send the switch back without its key
	sw.HostKey = ""
	if _, err := m.TestConnection(sw); !errors.Is(err, sshx.ErrHostKeyMismatch) {
		t.Fatalf("want host key mismatch, got %v", err)
	}
	if p := got(); len(p) != 0 {
		t.Fatalf("password sent to impostor: %v", p)
	}
}
