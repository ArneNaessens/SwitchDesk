package core

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"github.com/jaba0x/switchdesk/internal/aruba"
	"github.com/jaba0x/switchdesk/internal/store"
)

// fakeAOSS is a pretend 2930F: it answers the firmware commands and, on
// "copy tftp flash", really downloads the file from the TFTP port it's given.
type fakeAOSS struct {
	mu                 sync.Mutex
	primary, secondary string
	running, def       string
	tftpPort           int
	gotBytes           int
	ln                 net.Listener
	addr               string
	cfg                *ssh.ServerConfig
	down               bool

	// SSH file transfer (ip ssh filetransfer + SFTP into /os/<bank>)
	push                         bool // the switch supports it
	ftOn, tftpCliOff, tftpSrvOff bool

	cx       bool // behave like an AOS-CX 6100 instead of AOS-S
	cxNoTFTP bool // the switch can't reach our TFTP server
}

func newFakeAOSS(t *testing.T) *fakeAOSS {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(priv)
	f := &fakeAOSS{primary: "WC.16.11.0015", secondary: "WC.16.10.0012", running: "primary", def: "primary"}
	f.cfg = &ssh.ServerConfig{PasswordCallback: func(c ssh.ConnMetadata, p []byte) (*ssh.Permissions, error) { return nil, nil }}
	f.cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f.addr = ln.Addr().String()
	f.listen(ln)
	return f
}

func (f *fakeAOSS) listen(ln net.Listener) {
	f.ln = ln
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(c)
		}
	}()
}

// reboot drops SSH for a moment and comes back on the default bank.
func (f *fakeAOSS) reboot() {
	f.ln.Close()
	go func() {
		time.Sleep(300 * time.Millisecond)
		f.mu.Lock()
		f.running = f.def
		f.mu.Unlock()
		// the port can stay busy for a moment on some systems
		for i := 0; i < 100; i++ {
			if ln, err := net.Listen("tcp", f.addr); err == nil {
				f.listen(ln)
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
	}()
}

func (f *fakeAOSS) serve(nc net.Conn) {
	_, chans, reqs, err := ssh.NewServerConn(nc, f.cfg)
	if err != nil {
		return
	}
	go ssh.DiscardRequests(reqs)
	for nch := range chans {
		ch, creqs, _ := nch.Accept()
		go func() {
			for r := range creqs {
				if r.Type == "subsystem" && len(r.Payload) > 4 && string(r.Payload[4:]) == "sftp" {
					f.mu.Lock()
					on := f.ftOn
					f.mu.Unlock()
					r.Reply(on, nil)
					if on {
						go f.sftp(ch)
					}
					continue
				}
				r.Reply(r.Type == "pty-req" || r.Type == "shell", nil)
				if r.Type == "shell" {
					if f.cx {
						go f.shellCX(ch, nc)
					} else {
						go f.shell(ch, nc)
					}
				}
			}
		}()
	}
}

func (f *fakeAOSS) shell(ch ssh.Channel, nc net.Conn) {
	defer ch.Close()
	prompt := "SW-FAKE# "
	io.WriteString(ch, "\r\n"+prompt)
	b := make([]byte, 1)
	var line []byte
	copyRe := regexp.MustCompile(`^copy tftp flash (\S+) (\S+) (primary|secondary)$`)
	for {
		if n, err := ch.Read(b); err != nil || n == 0 {
			return
		}
		if b[0] != '\r' && b[0] != '\n' {
			line = append(line, b[0])
			ch.Write(b)
			continue
		}
		cmd := strings.TrimSpace(string(line))
		line = nil
		io.WriteString(ch, "\r\n")
		f.mu.Lock()
		run := f.primary
		if f.running == "secondary" {
			run = f.secondary
		}
		switch {
		case cmd == "show version":
			fmt.Fprintf(ch, "Image stamp:    /ws/swbuildm/rel_x\r\n                 %s\r\n Boot Image:     %s\r\n", run, strings.Title(f.running))
		case cmd == "show flash":
			fmt.Fprintf(ch, "Primary Image   :     33204214 10/18/23 %s\r\nSecondary Image :     32952136 03/10/22 %s\r\n\r\nDefault Boot Image   : %s\r\n", f.primary, f.secondary, strings.Title(f.def))
		case copyRe.MatchString(cmd):
			m := copyRe.FindStringSubmatch(cmd)
			f.mu.Unlock()
			io.WriteString(ch, "The Secondary OS Image will be deleted, continue [y/n]? ")
			ch.Read(b) // the 'y'
			io.WriteString(ch, "\r\n")
			// the test learns the TFTP port once the server is up, which is
			// just before this command arrives
			port := 0
			for i := 0; i < 200 && port == 0; i++ {
				f.mu.Lock()
				port = f.tftpPort
				f.mu.Unlock()
				if port == 0 {
					time.Sleep(10 * time.Millisecond)
				}
			}
			data, err := tftpGet(m[1], port, m[2])
			f.mu.Lock()
			if err != nil {
				fmt.Fprintf(ch, "TFTP download failed: %v\r\n", err)
			} else {
				f.gotBytes = len(data)
				v := regexp.MustCompile(`([A-Z]{2})_(\d+)_(\d+)_(\d+)`).FindStringSubmatch(m[2])
				ver := fmt.Sprintf("%s.%s.%s.%s", v[1], v[2], v[3], v[4])
				if m[3] == "secondary" {
					f.secondary = ver
				} else {
					f.primary = ver
				}
				io.WriteString(ch, "Validating and Writing System Software to the Filesystem ...\r\n")
			}
		case strings.HasPrefix(cmd, "boot set-default flash "):
			f.def = strings.TrimPrefix(cmd, "boot set-default flash ")
		case strings.HasPrefix(cmd, "boot system flash "):
			f.mu.Unlock()
			io.WriteString(ch, "This will reboot the system from the secondary image.\r\nContinue (y/n)? ")
			ch.Read(b)
			ch.Close()
			nc.Close()
			f.reboot()
			return
		case cmd == "show running-config":
			io.WriteString(ch, "hostname \"SW-FAKE\"\r\n")
			if f.ftOn {
				io.WriteString(ch, "ip ssh filetransfer\r\n")
			}
			if f.tftpCliOff {
				io.WriteString(ch, "no tftp client\r\n")
			}
			if f.tftpSrvOff {
				io.WriteString(ch, "no tftp server\r\n")
			}
		case f.push && cmd == "ip ssh filetransfer":
			f.ftOn, f.tftpCliOff, f.tftpSrvOff = true, true, true
			io.WriteString(ch, "Tftp and auto-tftp have been disabled.\r\n")
		case f.push && cmd == "no ip ssh filetransfer":
			f.ftOn = false
		case f.push && cmd == "tftp client":
			f.tftpCliOff = false
		case f.push && cmd == "tftp server":
			f.tftpSrvOff = false
		case f.push && (cmd == "configure terminal" || cmd == "end"):
		case cmd == "no page", cmd == "write memory", cmd == "terminal width 200", cmd == "":
		default:
			fmt.Fprintf(ch, "Invalid input: %s\r\n", cmd)
		}
		f.mu.Unlock()
		io.WriteString(ch, prompt)
	}
}

// sftp accepts uploads to /os/primary and /os/secondary and reads the
// version out of the image, like the real switch does.
func (f *fakeAOSS) sftp(ch ssh.Channel) {
	h := sftp.InMemHandler()
	h.FilePut = fakeWriter{f}
	srv := sftp.NewRequestServer(ch, h)
	srv.Serve()
	srv.Close()
}

type fakeWriter struct{ f *fakeAOSS }

func (w fakeWriter) Filewrite(r *sftp.Request) (io.WriterAt, error) {
	bank := strings.TrimPrefix(r.Filepath, "/os/")
	if bank != "primary" && bank != "secondary" {
		return nil, os.ErrPermission
	}
	return &fakeImage{f: w.f, bank: bank}, nil
}

type fakeImage struct {
	f    *fakeAOSS
	bank string
	buf  []byte
}

func (i *fakeImage) WriteAt(p []byte, off int64) (int, error) {
	if end := int(off) + len(p); end > len(i.buf) {
		i.buf = append(i.buf, make([]byte, end-len(i.buf))...)
	}
	copy(i.buf[off:], p)
	return len(p), nil
}

func (i *fakeImage) Close() error {
	ver := regexp.MustCompile(`[A-Z]{2}\.\d+\.\d+\.\d+`).Find(i.buf)
	i.f.mu.Lock()
	defer i.f.mu.Unlock()
	i.f.gotBytes = len(i.buf)
	if ver == nil {
		return nil
	}
	if i.bank == "secondary" {
		i.f.secondary = string(ver)
	} else {
		i.f.primary = string(ver)
	}
	return nil
}

func tftpGet(host string, port int, name string) ([]byte, error) {
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		return nil, err
	}
	defer c.Close()
	srv := &net.UDPAddr{IP: net.ParseIP(host), Port: port}
	req := append(append([]byte{0, 1}, name+"\x00octet\x00"...), "blksize\x001024\x00"...)
	c.WriteToUDP(req, srv)
	var out bytes.Buffer
	buf := make([]byte, 2048)
	size, expect := 512, uint16(1)
	for {
		c.SetReadDeadline(time.Now().Add(5 * time.Second))
		n, from, err := c.ReadFromUDP(buf)
		if err != nil {
			return nil, err
		}
		switch binary.BigEndian.Uint16(buf) {
		case 5:
			return nil, fmt.Errorf("%s", buf[4:n-1])
		case 6:
			size = 1024
			c.WriteToUDP([]byte{0, 4, 0, 0}, from)
		case 3:
			blk := binary.BigEndian.Uint16(buf[2:])
			c.WriteToUDP([]byte{0, 4, buf[2], buf[3]}, from)
			if blk == expect {
				out.Write(buf[4:n])
				expect++
				if n-4 < size {
					return out.Bytes(), nil
				}
			}
		}
	}
}

func TestUpgradeEndToEnd(t *testing.T) {
	tftpAddr, pollDown, pollUp, settleAfter = "127.0.0.1:0", 50*time.Millisecond, 100*time.Millisecond, 200*time.Millisecond

	dir := t.TempDir()
	st, _ := store.Open(dir)
	cred, _ := st.SaveCredential(store.Credential{Username: "manager", Password: "x"})
	fake := newFakeAOSS(t)
	host, portS, _ := net.SplitHostPort(fake.addr)
	port, _ := strconv.Atoi(portS)
	sw, _ := st.SaveSwitch(store.Switch{Host: host, Port: port, CredentialID: cred.ID})

	m := New(st, nil)
	m.Start()
	defer m.Stop()

	// put a newer image in the library; the fake learns where the TFTP server listens
	img := filepath.Join(t.TempDir(), "WC_16_11_0021.swi")
	payload := bytes.Repeat([]byte("firmware!"), 50000)
	os.WriteFile(img, payload, 0o600)
	if _, err := m.Library().AddFile(img); err != nil {
		t.Fatal(err)
	}
	m.collect(sw.ID)
	fi := m.Dashboard().Switches[0].Firmware
	if fi.Running != "WC.16.11.0015" || fi.Available != "WC.16.11.0021" || fi.Staged || !fi.Supported {
		t.Fatalf("availability: %+v", fi)
	}

	// grab the TFTP port once the job starts its server
	go func() {
		for i := 0; i < 6000; i++ { // TFTP only starts after the SSH upload is turned down
			m.up.mu.Lock()
			if m.up.tftp != nil {
				fake.mu.Lock()
				fake.tftpPort = m.up.tftp.Port()
				fake.mu.Unlock()
				m.up.mu.Unlock()
				return
			}
			m.up.mu.Unlock()
			time.Sleep(5 * time.Millisecond)
		}
	}()
	jobs, err := m.StartUpgrade([]string{sw.ID}, "", true, false)
	if err != nil || len(jobs) != 1 {
		t.Fatal(jobs, err)
	}
	deadline := time.Now().Add(60 * time.Second)
	var j Job
	for time.Now().Before(deadline) {
		j = m.Jobs()[0]
		if !j.active() {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if j.State != "done" {
		t.Fatalf("job ended %s: %s\n%s", j.State, j.Error, strings.Join(j.Log, "\n"))
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.gotBytes != len(payload) || fake.secondary != "WC.16.11.0021" || fake.running != "secondary" {
		t.Fatalf("fake switch: got %d bytes, secondary %s, running %s", fake.gotBytes, fake.secondary, fake.running)
	}
	if fi := m.Dashboard().Switches[0].Firmware; fi.Available != "" || fi.Running != "WC.16.11.0021" {
		t.Fatalf("after update: %+v", fi)
	}
	// the same image again: nothing newer, so no job
	if _, err := m.StartUpgrade([]string{sw.ID}, "", true, false); err == nil {
		t.Fatal("queued an update with nothing newer")
	}
}

func TestMaintenanceWindow(t *testing.T) {
	at := func(h, m int) time.Time { return time.Date(2026, 1, 1, h, m, 0, 0, time.Local) }
	if !inWindow(at(2, 30), "02:00", "05:00") || inWindow(at(5, 0), "02:00", "05:00") || inWindow(at(1, 59), "02:00", "05:00") {
		t.Fatal("plain window")
	}
	if !inWindow(at(23, 30), "23:00", "01:00") || !inWindow(at(0, 30), "23:00", "01:00") || inWindow(at(12, 0), "23:00", "01:00") {
		t.Fatal("window over midnight")
	}
}

// Over VPN or zero trust access the switch can't reach us, so TFTP never
// works. The job has to upload over SSH and put the switch's settings back.
func TestUpgradeOverSSHWithoutTFTP(t *testing.T) {
	tftpAddr, pollDown, pollUp, settleAfter = "127.0.0.1:0", 50*time.Millisecond, 100*time.Millisecond, 200*time.Millisecond

	dir := t.TempDir()
	st, _ := store.Open(dir)
	cred, _ := st.SaveCredential(store.Credential{Username: "manager", Password: "x"})
	fake := newFakeAOSS(t)
	fake.push = true
	fake.tftpPort = 1 // nothing listens there: a TFTP attempt would fail
	host, portS, _ := net.SplitHostPort(fake.addr)
	port, _ := strconv.Atoi(portS)
	sw, _ := st.SaveSwitch(store.Switch{Host: host, Port: port, CredentialID: cred.ID})

	m := New(st, nil)
	m.Start()
	defer m.Stop()

	img := filepath.Join(t.TempDir(), "WC_16_11_0021.swi")
	payload := append([]byte("HPE image WC.16.11.0021 "), bytes.Repeat([]byte("firmware!"), 200000)...)
	os.WriteFile(img, payload, 0o600)
	if _, err := m.Library().AddFile(img); err != nil {
		t.Fatal(err)
	}
	m.collect(sw.ID)
	if _, err := m.StartUpgrade([]string{sw.ID}, "", true, false); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(60 * time.Second)
	var j Job
	for time.Now().Before(deadline) {
		j = m.Jobs()[0]
		if !j.active() {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	log := strings.Join(j.Log, "\n")
	if j.State != "done" {
		t.Fatalf("job ended %s: %s\n%s", j.State, j.Error, log)
	}
	if !strings.Contains(log, "Uploaded with SFTP") || strings.Contains(log, "TFTP") {
		t.Fatalf("expected an SFTP upload and no TFTP:\n%s", log)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.gotBytes != len(payload) || fake.secondary != "WC.16.11.0021" || fake.running != "secondary" {
		t.Fatalf("fake switch: got %d bytes, secondary %s, running %s", fake.gotBytes, fake.secondary, fake.running)
	}
	if fake.ftOn || fake.tftpCliOff || fake.tftpSrvOff {
		t.Fatalf("settings not restored: filetransfer %v, tftp client off %v, tftp server off %v", fake.ftOn, fake.tftpCliOff, fake.tftpSrvOff)
	}
}

// readLine reads keystrokes up to Enter, echoing them, like an AOS-CX prompt.
func readLine(ch ssh.Channel) (string, bool) {
	b := make([]byte, 1)
	var line []byte
	for {
		if n, err := ch.Read(b); err != nil || n == 0 {
			return "", false
		}
		if b[0] == '\r' || b[0] == '\n' {
			return strings.TrimSpace(string(line)), true
		}
		if b[0] == 3 { // Ctrl-C
			return "\x03", true
		}
		line = append(line, b[0])
		ch.Write(b)
	}
}

func (f *fakeAOSS) shellCX(ch ssh.Channel, nc net.Conn) {
	defer ch.Close()
	prompt := "SW-CX# "
	io.WriteString(ch, "\r\n"+prompt)
	copyRe := regexp.MustCompile(`^copy tftp://([^/]+)/(\S+) (primary|secondary) vrf (\S+)$`)
	for {
		cmd, ok := readLine(ch)
		if !ok {
			return
		}
		io.WriteString(ch, "\r\n")
		f.mu.Lock()
		run := f.primary
		if f.running == "secondary" {
			run = f.secondary
		}
		switch {
		case cmd == "show version":
			fmt.Fprintf(ch, "-----\r\nArubaOS-CX\r\n(c) Copyright 2017-2026 Hewlett Packard Enterprise\r\n-----\r\nVersion      : %s\r\nActive Image : %s\r\n", run, f.running)
		case cmd == "show images":
			fmt.Fprintf(ch, "---\r\nArubaOS-CX Primary Image\r\n---\r\nVersion : %s\r\n\r\n---\r\nArubaOS-CX Secondary Image\r\n---\r\nVersion : %s\r\n\r\nDefault Image : %s\r\n", f.primary, f.secondary, f.def)
		case cmd == "show system":
			io.WriteString(ch, "Hostname        : SW-CX\r\nProduct Name    : JL678A 6100 24G CL4 4SFP+ Swch\r\n")
		case copyRe.MatchString(cmd):
			m := copyRe.FindStringSubmatch(cmd)
			noTFTP := f.cxNoTFTP
			f.mu.Unlock()
			io.WriteString(ch, "The secondary image will be deleted.\r\nContinue (y/n)? ")
			ans, ok := readLine(ch) // AOS-CX acts only after Enter
			if !ok {
				return
			}
			io.WriteString(ch, "\r\n")
			if ans != "y" {
				f.mu.Lock()
				break
			}
			if noTFTP {
				// a real switch keeps trying in silence; only Ctrl-C ends it
				for {
					c, ok := readLine(ch)
					if !ok {
						return
					}
					if c == "\x03" {
						break
					}
				}
				f.mu.Lock()
				io.WriteString(ch, "Copy aborted\r\n")
				break
			}
			port := 0
			for i := 0; i < 200 && port == 0; i++ {
				f.mu.Lock()
				port = f.tftpPort
				f.mu.Unlock()
				if port == 0 {
					time.Sleep(10 * time.Millisecond)
				}
			}
			host := strings.Split(m[1], ":")[0]
			data, err := tftpGet(host, port, m[2])
			f.mu.Lock()
			if err != nil {
				fmt.Fprintf(ch, "Failed to copy: %v\r\n", err)
			} else {
				f.gotBytes = len(data)
				v := regexp.MustCompile(`([A-Z]{2})_(\d+)_(\d+)_(\d+)`).FindStringSubmatch(m[2])
				ver := fmt.Sprintf("%s.%s.%s.%s", v[1], v[2], v[3], v[4])
				if m[3] == "secondary" {
					f.secondary = ver
				} else {
					f.primary = ver
				}
				io.WriteString(ch, "######################################################################### 100.0%\r\nVerifying and writing system firmware...\r\n")
			}
		case strings.HasPrefix(cmd, "boot set-default "):
			f.def = strings.TrimPrefix(cmd, "boot set-default ")
			io.WriteString(ch, "Default boot image set to "+f.def+" image.\r\n")
		case strings.HasPrefix(cmd, "boot system "):
			f.mu.Unlock()
			io.WriteString(ch, "Checking if the configuration needs to be saved...\r\n\r\nDo you want to continue (y/n)? ")
			if ans, ok := readLine(ch); !ok || ans != "y" {
				f.mu.Lock()
				break
			}
			f.mu.Lock()
			f.def = strings.TrimPrefix(cmd, "boot system ")
			f.mu.Unlock()
			ch.Close()
			nc.Close()
			f.reboot()
			return
		case cmd == "show running-config":
			io.WriteString(ch, "hostname SW-CX\r\n")
		case cmd == "no page", cmd == "write memory", cmd == "terminal-length 1000", cmd == "", cmd == "\x03":
		default:
			fmt.Fprintf(ch, "Invalid input: %s\r\n", cmd)
		}
		f.mu.Unlock()
		io.WriteString(ch, prompt)
	}
}

func cxSetup(t *testing.T, noTFTP bool) (*Manager, *fakeAOSS, string, []byte) {
	tftpAddr, pollDown, pollUp, settleAfter = "127.0.0.1:0", 50*time.Millisecond, 100*time.Millisecond, 200*time.Millisecond
	st, _ := store.Open(t.TempDir())
	cred, _ := st.SaveCredential(store.Credential{Username: "admin", Password: "x"})
	fake := newFakeAOSS(t)
	fake.cx, fake.cxNoTFTP = true, noTFTP
	fake.primary, fake.secondary = "PL.10.13.1080", "PL.10.08.1010"
	host, portS, _ := net.SplitHostPort(fake.addr)
	port, _ := strconv.Atoi(portS)
	sw, _ := st.SaveSwitch(store.Switch{Host: host, Port: port, CredentialID: cred.ID})
	m := New(st, nil)
	m.Start()
	t.Cleanup(m.Stop)
	img := filepath.Join(t.TempDir(), "PL_10_16_1060.swi")
	payload := bytes.Repeat([]byte("cx-image"), 40000)
	os.WriteFile(img, payload, 0o600)
	if _, err := m.Library().AddFile(img); err != nil {
		t.Fatal(err)
	}
	m.collect(sw.ID)
	if fam := m.snaps[sw.ID].Family; fam != aruba.FamilyAOSCX {
		t.Fatalf("detected %s, want aos-cx", fam)
	}
	go func() {
		for i := 0; i < 6000; i++ {
			m.up.mu.Lock()
			if m.up.tftp != nil {
				fake.mu.Lock()
				fake.tftpPort = m.up.tftp.Port()
				fake.mu.Unlock()
				m.up.mu.Unlock()
				return
			}
			m.up.mu.Unlock()
			time.Sleep(5 * time.Millisecond)
		}
	}()
	return m, fake, sw.ID, payload
}

func waitJob(t *testing.T, m *Manager, within time.Duration) Job {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if j := m.Jobs()[0]; !j.active() {
			return j
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("job still running after %s:\n%s", within, strings.Join(m.Jobs()[0].Log, "\n"))
	return Job{}
}

// The 6100 from the field: "Continue (y/n)?" needs Enter after the y, or the
// switch waits until its idle timeout logs the session out.
func TestUpgradeAOSCXAnswersPromptWithEnter(t *testing.T) {
	m, fake, id, payload := cxSetup(t, false)
	if _, err := m.StartUpgrade([]string{id}, "", true, false); err != nil {
		t.Fatal(err)
	}
	j := waitJob(t, m, 60*time.Second)
	if j.State != "done" {
		t.Fatalf("job ended %s: %s\n%s", j.State, j.Error, strings.Join(j.Log, "\n"))
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.gotBytes != len(payload) || fake.secondary != "PL.10.16.1060" || fake.running != "secondary" {
		t.Fatalf("fake switch: got %d bytes, secondary %s, running %s", fake.gotBytes, fake.secondary, fake.running)
	}
}

// A switch that can't reach our TFTP server must fail quickly with a reason,
// not hang until the switch logs us out.
func TestUpgradeTFTPNeverRequestedFailsFast(t *testing.T) {
	oldNo, oldTick := tftpNoRequest, tftpWatchTick
	tftpNoRequest, tftpWatchTick = 2*time.Second, 200*time.Millisecond
	t.Cleanup(func() { tftpNoRequest, tftpWatchTick = oldNo, oldTick })
	m, _, id, _ := cxSetup(t, true)
	start := time.Now()
	if _, err := m.StartUpgrade([]string{id}, "", true, false); err != nil {
		t.Fatal(err)
	}
	j := waitJob(t, m, 30*time.Second)
	if j.State != "failed" || !strings.Contains(j.Error, "never asked this computer") || !strings.Contains(j.Error, "UDP port 69") {
		t.Fatalf("want a fast TFTP failure, got %s: %s\n%s", j.State, j.Error, strings.Join(j.Log, "\n"))
	}
	if d := time.Since(start); d > 20*time.Second {
		t.Fatalf("took %s to give up", d)
	}
}
