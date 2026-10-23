package sshx

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// scpOnlyServer has no SFTP subsystem, like older ProCurve firmware, and
// accepts files with the classic "scp -t" protocol.
type scpOnlyServer struct {
	mu   sync.Mutex
	path string
	data []byte
}

func (s *scpOnlyServer) start(t *testing.T) (host string, port int) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(priv)
	cfg := &ssh.ServerConfig{PasswordCallback: func(c ssh.ConnMetadata, p []byte) (*ssh.Permissions, error) { return nil, nil }}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				_, chans, reqs, err := ssh.NewServerConn(nc, cfg)
				if err != nil {
					return
				}
				go ssh.DiscardRequests(reqs)
				for nch := range chans {
					ch, creqs, _ := nch.Accept()
					go func() {
						for r := range creqs {
							if r.Type == "exec" && len(r.Payload) > 4 {
								cmd := string(r.Payload[4:])
								r.Reply(strings.HasPrefix(cmd, "scp -t "), nil)
								if strings.HasPrefix(cmd, "scp -t ") {
									go s.sink(ch, strings.TrimPrefix(cmd, "scp -t "))
								}
								continue
							}
							r.Reply(false, nil) // no sftp subsystem
						}
					}()
				}
			}()
		}
	}()
	h, p, _ := net.SplitHostPort(ln.Addr().String())
	port, _ = strconv.Atoi(p)
	return h, port
}

func (s *scpOnlyServer) sink(ch ssh.Channel, path string) {
	defer ch.Close()
	r := bufio.NewReader(ch)
	ch.Write([]byte{0})
	hdr, err := r.ReadString('\n')
	if err != nil {
		return
	}
	var mode, name string
	var size int
	fmt.Sscanf(hdr, "%s %d %s", &mode, &size, &name)
	ch.Write([]byte{0})
	buf := make([]byte, size)
	if _, err := io.ReadFull(r, buf); err != nil {
		return
	}
	r.ReadByte() // trailing zero
	s.mu.Lock()
	s.path, s.data = path, buf
	s.mu.Unlock()
	ch.Write([]byte{0})
	ch.SendRequest("exit-status", false, ssh.Marshal(struct{ C uint32 }{0}))
}

func TestUploadFallsBackToSCP(t *testing.T) {
	srv := &scpOnlyServer{}
	host, port := srv.start(t)
	payload := bytes.Repeat([]byte("image"), 100000)
	var last int64
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	method, err := Upload(ctx, Options{Host: host, Port: port, Username: "manager", Password: "x"}, "/os/secondary",
		bytes.NewReader(payload), int64(len(payload)), func(sent, total int64) { last = sent })
	if err != nil {
		t.Fatal(err)
	}
	if method != "scp" {
		t.Fatalf("method %q", method)
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if srv.path != "/os/secondary" || !bytes.Equal(srv.data, payload) {
		t.Fatalf("server got %q, %d bytes", srv.path, len(srv.data))
	}
	if last != int64(len(payload)) {
		t.Fatalf("last progress %d", last)
	}
}

func TestUploadHostKeyPinned(t *testing.T) {
	srv := &scpOnlyServer{}
	host, port := srv.start(t)
	_, err := Upload(context.Background(), Options{Host: host, Port: port, Username: "m", Password: "x", KnownKey: "SHA256:wrong"},
		"/os/primary", bytes.NewReader([]byte("x")), 1, nil)
	if err != ErrHostKeyMismatch {
		t.Fatalf("want host key mismatch, got %v", err)
	}
}
