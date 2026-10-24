package sshx

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// fakeSwitch emulates the awkward parts of an AOS-S CLI: VT100 cursor moves,
// a "Press any key" banner, echo, and a pager.
func fakeSwitch(t *testing.T) (addr string, stop func()) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(priv)
	cfg := &ssh.ServerConfig{PasswordCallback: func(c ssh.ConnMetadata, p []byte) (*ssh.Permissions, error) {
		if c.User() == "manager" && string(p) == "secret" {
			return nil, nil
		}
		return nil, fmt.Errorf("denied")
	}}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			go serve(nc, cfg)
		}
	}()
	return ln.Addr().String(), func() { ln.Close() }
}

func serve(nc net.Conn, cfg *ssh.ServerConfig) {
	_, chans, reqs, err := ssh.NewServerConn(nc, cfg)
	if err != nil {
		return
	}
	go ssh.DiscardRequests(reqs)
	for nch := range chans {
		ch, creqs, _ := nch.Accept()
		go func() {
			for r := range creqs {
				r.Reply(r.Type == "pty-req" || r.Type == "shell", nil)
				if r.Type == "shell" {
					go shell(ch)
				}
			}
		}()
	}
}

func shell(ch ssh.Channel) {
	defer ch.Close()
	io.WriteString(ch, "\x1b[1;24r\x1b[24;1HHPE Switch 2930F\r\n\x1b[2KPress any key to continue\x1b[?25h")
	b := make([]byte, 1)
	ch.Read(b)
	prompt := "\x1b[24;1H\x1b[2KSW-TEST# "
	io.WriteString(ch, "\x1b[2J"+prompt)
	paging := true
	var line []byte
	for {
		n, err := ch.Read(b)
		if err != nil || n == 0 {
			return
		}
		if b[0] != '\n' && b[0] != '\r' {
			line = append(line, b[0])
			ch.Write(b) // echo
			continue
		}
		cmd := strings.TrimSpace(string(line))
		line = nil
		io.WriteString(ch, "\r\n")
		switch cmd {
		case "no page":
			paging = false
		case "show system":
			out := "  System Name        : SW-TEST\r\n  CPU Util (%)       : 3\r\n"
			if paging {
				io.WriteString(ch, out+"-- MORE --, next page: Space, next line: Enter, quit: Control-C")
				ch.Read(b)
				io.WriteString(ch, "\x1b[2K\r")
			}
			io.WriteString(ch, out)
		case "copy cx-style":
			// AOS-CX: prompt answered with "y" plus Enter, nothing happens without the Enter
			io.WriteString(ch, "The secondary image will be deleted.\r\nContinue (y/n)? ")
			var got []byte
			for !bytes.Contains(got, []byte("\n")) && !bytes.Contains(got, []byte("\r")) {
				if n, err := ch.Read(b); err != nil || n == 0 {
					return
				}
				got = append(got, b[0])
				if b[0] != '\r' && b[0] != '\n' {
					ch.Write(b)
				}
			}
			if strings.TrimSpace(string(got)) == "y" {
				io.WriteString(ch, "\r\nCopy completed.\r\n")
			} else {
				io.WriteString(ch, "\r\nAborted\r\n")
			}
		case "copy aoss-style":
			// AOS-S: single keypress, no Enter
			io.WriteString(ch, "The Secondary OS Image will be deleted, continue [y/n]? ")
			ch.Read(b)
			if b[0] == 'y' {
				io.WriteString(ch, "\r\nValidating and Writing System Software\r\n")
			}
		case "exit":
			return
		case "":
		default:
			io.WriteString(ch, "Invalid input: "+cmd+"\r\n")
		}
		io.WriteString(ch, prompt)
	}
}

func TestSessionAgainstFakeAOSS(t *testing.T) {
	addr, stop := fakeSwitch(t)
	defer stop()
	host, port, _ := net.SplitHostPort(addr)
	var p int
	fmt.Sscan(port, &p)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	s, err := Dial(ctx, Options{Host: host, Port: p, Username: "manager", Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.Hostname != "SW-TEST" || !strings.HasPrefix(s.Fingerprint, "SHA256:") {
		t.Fatalf("hostname=%q fp=%q", s.Hostname, s.Fingerprint)
	}
	// pager is handled even before "no page"
	out, err := s.Run(ctx, "show system", 5*time.Second)
	if err != nil || !strings.Contains(out, "CPU Util (%)       : 3") || strings.Contains(out, "SW-TEST#") || strings.Contains(out, "show system") {
		t.Fatalf("out=%q err=%v", out, err)
	}
	s.Run(ctx, "no page", 5*time.Second)
	out, _ = s.Run(ctx, "show system", 5*time.Second)
	if strings.Count(out, "System Name") != 1 {
		t.Fatalf("out=%q", out)
	}
	confirmEnterAfter = 300 * time.Millisecond
	out, err = s.Run(ctx, "copy cx-style", 5*time.Second)
	if err != nil || !strings.Contains(out, "Copy completed") {
		t.Fatalf("cx-style confirm: out=%q err=%v", out, err)
	}
	out, err = s.Run(ctx, "copy aoss-style", 5*time.Second)
	if err != nil || !strings.Contains(out, "Validating") {
		t.Fatalf("aos-s-style confirm: out=%q err=%v", out, err)
	}
	// the AOS-S answer must not leave a stray Enter behind for the next command
	out, _ = s.Run(ctx, "show system", 5*time.Second)
	if strings.Count(out, "System Name") != 1 || strings.Contains(out, "Invalid") {
		t.Fatalf("after aos-s confirm: %q", out)
	}
	out, _ = s.Run(ctx, "bogus", 5*time.Second)
	if !strings.Contains(out, "Invalid input") {
		t.Fatalf("out=%q", out)
	}

	// host key pinning
	fp := s.Fingerprint
	_, err = Dial(ctx, Options{Host: host, Port: p, Username: "manager", Password: "secret", KnownKey: "SHA256:wrong"})
	if err != ErrHostKeyMismatch {
		t.Fatalf("want mismatch, got %v", err)
	}
	s2, err := Dial(ctx, Options{Host: host, Port: p, Username: "manager", Password: "secret", KnownKey: fp})
	if err != nil {
		t.Fatal(err)
	}
	s2.Close()
	if _, err := Dial(ctx, Options{Host: host, Port: p, Username: "manager", Password: "nope"}); err == nil {
		t.Fatal("bad password accepted")
	}
}
