// Package sshx drives an interactive CLI over SSH. Aruba switches (AOS-S in
// particular) don't support exec channels, so everything runs through a PTY
// shell: send a command, read until the prompt comes back.
package sshx

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// ErrHostKeyMismatch is returned when the switch presents a different host key
// than the one recorded on first connect.
var ErrHostKeyMismatch = errors.New("host key changed since last connection")

type Options struct {
	Host     string
	Port     int
	Username string
	Password string
	// KnownKey is the SHA256 fingerprint recorded on a previous connect.
	// Empty means trust on first use.
	KnownKey string
	Timeout  time.Duration
}

type Session struct {
	client *ssh.Client
	sess   *ssh.Session
	stdin  io.WriteCloser

	mu     sync.Mutex
	buf    bytes.Buffer
	notify chan struct{}
	done   chan struct{}

	Hostname    string // learned from the first prompt
	Fingerprint string // SHA256 host key fingerprint seen on this connect
	Banner      string // everything printed before the first prompt
}

var (
	ansiCursor = regexp.MustCompile(`\x1b\[\d+;\d+[Hf]`)
	ansiAny    = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]|\x1b[()][0-9A-Za-z]|\x1b[=>78EDM]`)
	promptRe   = regexp.MustCompile(`(?m)^([A-Za-z0-9_.\-]+)(\([^)]*\))?\s?[#>]\s*$`)
	moreRe     = regexp.MustCompile(`(?i)-- ?more ?--|press any key|--more--`)
)

// Clean strips terminal control sequences and normalises line endings.
func Clean(s string) string {
	s = ansiCursor.ReplaceAllString(s, "\n")
	s = ansiAny.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	// apply backspaces
	if strings.ContainsRune(s, '\b') {
		var out []rune
		for _, r := range s {
			if r == '\b' {
				if len(out) > 0 {
					out = out[:len(out)-1]
				}
				continue
			}
			out = append(out, r)
		}
		s = string(out)
	}
	// drop other control chars except newline and tab
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || r >= 0x20 {
			return r
		}
		return -1
	}, s)
	// collapse runs of blank lines produced by cursor moves
	lines := strings.Split(s, "\n")
	out := lines[:0]
	blank := 0
	for _, l := range lines {
		l = strings.TrimRight(l, " ")
		if l == "" {
			blank++
			if blank > 1 {
				continue
			}
		} else {
			blank = 0
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}

func Fingerprint(key ssh.PublicKey) string {
	sum := sha256.Sum256(key.Marshal())
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])
}

func clientConfig(o Options, seen *string) *ssh.ClientConfig {
	cfg := &ssh.ClientConfig{
		User: o.Username,
		Auth: []ssh.AuthMethod{
			ssh.Password(o.Password),
			ssh.KeyboardInteractive(func(user, instruction string, questions []string, echos []bool) ([]string, error) {
				ans := make([]string, len(questions))
				for i := range questions {
					ans[i] = o.Password
				}
				return ans, nil
			}),
		},
		HostKeyCallback: func(hostname string, remote net.Addr, key ssh.PublicKey) error {
			fp := Fingerprint(key)
			*seen = fp
			if o.KnownKey != "" && o.KnownKey != fp {
				return ErrHostKeyMismatch
			}
			return nil
		},
		Timeout: o.Timeout,
	}
	// Older ProCurve / AOS-S firmware still negotiates legacy algorithms.
	cfg.Config.KeyExchanges = []string{
		"curve25519-sha256", "curve25519-sha256@libssh.org",
		"ecdh-sha2-nistp256", "ecdh-sha2-nistp384", "ecdh-sha2-nistp521",
		"diffie-hellman-group14-sha256", "diffie-hellman-group16-sha512",
		"diffie-hellman-group-exchange-sha256",
		"diffie-hellman-group14-sha1", "diffie-hellman-group1-sha1",
	}
	cfg.Config.Ciphers = []string{
		"aes128-gcm@openssh.com", "aes256-gcm@openssh.com", "chacha20-poly1305@openssh.com",
		"aes128-ctr", "aes192-ctr", "aes256-ctr",
		"aes128-cbc", "3des-cbc",
	}
	cfg.HostKeyAlgorithms = []string{
		ssh.KeyAlgoED25519, ssh.KeyAlgoECDSA256, ssh.KeyAlgoECDSA384, ssh.KeyAlgoECDSA521,
		ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA,
	}
	return cfg
}

// Dial connects, opens a PTY shell and waits for the first prompt.
func Dial(ctx context.Context, o Options) (*Session, error) {
	if o.Port == 0 {
		o.Port = 22
	}
	if o.Timeout == 0 {
		o.Timeout = 10 * time.Second
	}
	var seen string
	addr := net.JoinHostPort(o.Host, strconv.Itoa(o.Port))
	d := net.Dialer{Timeout: o.Timeout}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	c, chans, reqs, err := ssh.NewClientConn(conn, addr, clientConfig(o, &seen))
	if err != nil {
		conn.Close()
		if errors.Is(err, ErrHostKeyMismatch) || strings.Contains(err.Error(), ErrHostKeyMismatch.Error()) {
			return nil, ErrHostKeyMismatch
		}
		return nil, err
	}
	client := ssh.NewClient(c, chans, reqs)
	sess, err := client.NewSession()
	if err != nil {
		client.Close()
		return nil, err
	}
	modes := ssh.TerminalModes{ssh.ECHO: 1, ssh.TTY_OP_ISPEED: 115200, ssh.TTY_OP_OSPEED: 115200}
	if err := sess.RequestPty("vt100", 500, 200, modes); err != nil {
		client.Close()
		return nil, err
	}
	stdin, err := sess.StdinPipe()
	if err != nil {
		client.Close()
		return nil, err
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		client.Close()
		return nil, err
	}
	if err := sess.Shell(); err != nil {
		client.Close()
		return nil, err
	}
	s := &Session{client: client, sess: sess, stdin: stdin, notify: make(chan struct{}, 1), done: make(chan struct{}), Fingerprint: seen}
	go s.reader(stdout)

	banner, err := s.waitFirstPrompt(ctx, 20*time.Second)
	if err != nil {
		s.Close()
		return nil, err
	}
	s.Banner = banner
	return s, nil
}

func (s *Session) reader(r io.Reader) {
	b := make([]byte, 32*1024)
	for {
		n, err := r.Read(b)
		if n > 0 {
			s.mu.Lock()
			s.buf.Write(b[:n])
			s.mu.Unlock()
			select {
			case s.notify <- struct{}{}:
			default:
			}
		}
		if err != nil {
			close(s.done)
			return
		}
	}
}

func (s *Session) snapshot() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

func (s *Session) reset() {
	s.mu.Lock()
	s.buf.Reset()
	s.mu.Unlock()
}

func (s *Session) write(str string) error {
	_, err := io.WriteString(s.stdin, str)
	return err
}

func lastLine(s string) string {
	s = strings.TrimRight(s, "\n ")
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}

// waitFirstPrompt handles "Press any key to continue" banners and learns the hostname.
func (s *Session) waitFirstPrompt(ctx context.Context, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	nudged := 0
	lastNudge := time.Now()
	for time.Now().Before(deadline) {
		clean := Clean(s.snapshot())
		tail := lastLine(clean)
		if m := promptRe.FindStringSubmatch(tail); m != nil {
			// make sure output has settled
			if s.quiet(250 * time.Millisecond) {
				s.Hostname = m[1]
				return clean, nil
			}
			continue
		}
		if moreRe.MatchString(tail) || (time.Since(lastNudge) > 2*time.Second && nudged < 4) {
			s.write("\n")
			nudged++
			lastNudge = time.Now()
		}
		if err := s.wait(ctx, 500*time.Millisecond); err != nil {
			return "", err
		}
	}
	return "", fmt.Errorf("no CLI prompt within %s (last output: %q)", timeout, lastLine(Clean(s.snapshot())))
}

// quiet returns true if no new data arrives within d.
func (s *Session) quiet(d time.Duration) bool {
	select {
	case <-s.notify:
		return false
	case <-time.After(d):
		return true
	}
}

func (s *Session) wait(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.done:
		return errors.New("connection closed by switch")
	case <-s.notify:
	case <-time.After(d):
	}
	return nil
}

func (s *Session) isPrompt(line string) bool {
	m := promptRe.FindStringSubmatch(line)
	if m == nil {
		return false
	}
	return s.Hostname == "" || strings.EqualFold(m[1], s.Hostname) || strings.HasPrefix(strings.ToLower(s.Hostname), strings.ToLower(m[1]))
}

// Run sends one command and returns its output without the echo and prompt.
func (s *Session) Run(ctx context.Context, cmd string, timeout time.Duration) (string, error) {
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	s.reset()
	if err := s.write(cmd + "\n"); err != nil {
		return "", err
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		raw := s.snapshot()
		clean := Clean(raw)
		tail := lastLine(clean)
		if moreRe.MatchString(tail) {
			s.write(" ")
		} else if s.isPrompt(tail) && strings.Count(clean, "\n") >= 1 {
			if s.quiet(200 * time.Millisecond) {
				return trimEcho(clean, cmd), nil
			}
			continue
		} else if strings.Contains(tail, "(y/n)") || strings.HasSuffix(strings.TrimSpace(tail), "[y/n]?") {
			// confirmation prompts (e.g. write memory on some builds)
			s.write("y")
		}
		if err := s.wait(ctx, 300*time.Millisecond); err != nil {
			return trimEcho(clean, cmd), err
		}
	}
	return trimEcho(Clean(s.snapshot()), cmd), fmt.Errorf("timeout waiting for %q", cmd)
}

func trimEcho(out, cmd string) string {
	lines := strings.Split(strings.Trim(out, "\n"), "\n")
	// drop echoed command (first line containing it)
	for i := 0; i < len(lines) && i < 3; i++ {
		if strings.Contains(lines[i], strings.TrimSpace(cmd)) {
			lines = lines[i+1:]
			break
		}
	}
	// drop trailing prompt
	if n := len(lines); n > 0 && promptRe.MatchString(lines[n-1]) {
		lines = lines[:n-1]
	}
	return strings.Trim(strings.Join(lines, "\n"), "\n")
}

func (s *Session) Close() error {
	if s.stdin != nil {
		s.write("exit\n")
	}
	if s.sess != nil {
		s.sess.Close()
	}
	if s.client != nil {
		return s.client.Close()
	}
	return nil
}
