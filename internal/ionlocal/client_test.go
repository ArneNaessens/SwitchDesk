package ionlocal

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jaba0x/switchdesk/internal/ionlocal/localfake"
)

func TestLoginCollectLogout(t *testing.T) {
	sw := localfake.New("admin", "p@ss word&=1")
	defer sw.Close()
	c := New(sw.Host(), false)
	ctx := context.Background()
	if err := c.Login(ctx, "admin", "p@ss word&=1"); err != nil {
		t.Fatal(err)
	}
	snap, err := Collect(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	c.Logout(ctx)
	if sw.Sessions() != 0 || sw.Logouts != 1 {
		t.Fatalf("session left open: %d sessions, %d logouts", sw.Sessions(), sw.Logouts)
	}

	f := snap.Facts
	if f.Hostname != "SW-BRANCH-114" || f.Model != "JL686A 1930 48p Gigabit CL4 PoE 4p SFP+ 370W" || f.Serial != "CN0TEST14V" ||
		f.Software != "InstantOn_1930_3.3.2.0 (5)" || f.BaseMAC != "8c:85:c1:00:00:0e" || f.Uptime != "71 days, 11h 46m" {
		t.Fatalf("facts: %+v", f)
	}
	if f.ConfigSaved == nil || *f.ConfigSaved {
		t.Fatal("running config differs from startup on the fake")
	}
	if len(snap.Ports) != 52 { // the 16 absent trunks are left out
		t.Fatalf("ports: %d", len(snap.Ports))
	}
	p2, p49, p7 := snap.Ports[1], snap.Ports[48], snap.Ports[6]
	if !p2.Up || p2.Speed != "1G" || p2.PoEmW != 3200 || p49.Type != "SFP+" || p49.Up || !p7.Loop {
		t.Fatalf("port 2 %+v, 49 %+v, 7 %+v", p2, p49, p7)
	}
	if !snap.STP.Known || snap.STP.Enabled || snap.STP.IsRoot || snap.STP.Mode != "MSTP" || snap.STP.RootMAC != "" {
		t.Fatalf("stp: %+v", snap.STP)
	}
	if len(snap.Neighbors) != 2 {
		t.Fatalf("neighbors: %+v", snap.Neighbors)
	}
	phone, core := snap.Neighbors[0], snap.Neighbors[1]
	if phone.MgmtIP != "192.168.13.121" || !strings.Contains(phone.Capabilities, "telephone") || phone.LocalPort != "2" {
		t.Fatalf("phone: %+v", phone)
	}
	if core.ChassisID != "ec:50:aa:71:87:40" || core.MgmtIP != "192.168.13.2" || core.PortID != "1/1/3" || core.SysName != "SW-BRANCH-CORE" || !strings.Contains(core.Capabilities, "bridge") {
		t.Fatalf("core: %+v", core)
	}
	if len(snap.VLANs) != 2 || snap.VLANs[0].Name != "DEFAULT_VLAN" || len(snap.VLANs[0].Untagged) != 52 || len(snap.VLANs[1].Tagged) != 3 || snap.VLANs[1].Name != "Voice" {
		t.Fatalf("vlans: %+v", snap.VLANs)
	}
	if snap.Ports[0].VLAN != "1" {
		t.Fatalf("port vlan: %q", snap.Ports[0].VLAN)
	}
	var fans, bad int
	for _, e := range snap.Env {
		if e.Kind == "fan" {
			fans++
		}
		if !e.OK {
			bad++
		}
	}
	if fans != 2 || bad != 1 || len(snap.Env) != 4 { // 2 fans, main PSU, temperature
		t.Fatalf("env: %+v", snap.Env)
	}
	if len(snap.Security.LogServers) != 1 || len(snap.Security.NTPServers) != 1 {
		t.Fatalf("security: %+v", snap.Security)
	}
	if len(snap.Logs) != 3 || snap.Logs[0].Severity != "E" || !strings.HasPrefix(snap.Logs[0].Text, "01/28/26 03:07:10") {
		t.Fatalf("logs: %+v", snap.Logs)
	}
	titles := ""
	for _, n := range snap.Notes {
		titles += n.Title + " | " + n.Detail + "\n"
	}
	if !strings.Contains(titles, "Clock is off by") || !strings.Contains(titles, "172.30.0.1 has never answered") || !strings.Contains(titles, "No loop protection") {
		t.Fatalf("notes: %s", titles)
	}
}

func TestClockInSync(t *testing.T) {
	got := map[string]*Node{}
	for name, body := range localfake.Sections() {
		n, err := Parse([]byte("<" + name + ">" + body + "</" + name + ">"))
		if err != nil {
			t.Fatal(name, err)
		}
		got[name] = n.Find(name)
	}
	// 03:21:30 at UTC+4 on Jan 28 is 23:21:30 UTC on Jan 27
	now := time.Date(2026, 1, 27, 23, 22, 0, 0, time.UTC)
	for _, n := range Build(got, now).Notes {
		if strings.HasPrefix(n.Title, "Clock") {
			t.Fatalf("clock flagged while in sync: %+v", n)
		}
	}
}

func TestWrongPasswordAndSessionLimit(t *testing.T) {
	sw := localfake.New("admin", "secret")
	defer sw.Close()
	ctx := context.Background()
	if err := New(sw.Host(), false).Login(ctx, "admin", "nope"); !errors.Is(err, ErrLogin) {
		t.Fatalf("wrong password: %v", err)
	}
	sw.MaxSessions = 1
	if err := New(sw.Host(), false).Login(ctx, "admin", "secret"); err != nil {
		t.Fatal(err)
	}
	if err := New(sw.Host(), false).Login(ctx, "admin", "secret"); !errors.Is(err, ErrTooMany) {
		t.Fatalf("second session: %v", err)
	}
	// without a session the sections are refused
	if _, err := New(sw.Host(), false).Get(ctx, "Ports"); err == nil {
		t.Fatal("read without login")
	}
}

func TestModelAndHelpers(t *testing.T) {
	cases := map[string]string{
		"HPE Networking Instant On Switch 48p Gigabit CL4 PoE 4p SFP+ 370W 1930 JL686A, InstantOn_1930_3.3.2.0 (5)": "JL686A 1930 48p Gigabit CL4 PoE 4p SFP+ 370W",
		"HPE Networking Instant On Switch 24p Gigabit 4p SFP+ 1930 JL682A, InstantOn_1930_3.1.0.0":                  "JL682A 1930 24p Gigabit 4p SFP+",
		"Something else": "Something else",
	}
	for in, want := range cases {
		if got := model(in); got != want {
			t.Errorf("model(%q) = %q, want %q", in, got, want)
		}
	}
	if uriComponent("a b&c=d/é!") != "a%20b%26c%3Dd%2F%C3%A9!" {
		t.Errorf("uriComponent: %s", uriComponent("a b&c=d/é!"))
	}
	if caps("0010100000000000") != "bridge, router" {
		t.Error(caps("0010100000000000"))
	}
}

// The EncryptionSetting answer of a real 1930 on 3.3.2.0: the key and token
// must come out of it in a form the login can use.
func TestRealEncryptionSetting(t *testing.T) {
	b, err := os.ReadFile("testdata/encryption_setting.xml")
	if err != nil {
		t.Fatal(err)
	}
	n, err := Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	if n.Text("passwEncryptEnable") != "1" || n.Text("loginToken") != "5155a365" {
		t.Fatalf("settings: %q %q", n.Text("passwEncryptEnable"), n.Text("loginToken"))
	}
	k, err := publicKey(n.Text("rsaPublicKey"))
	if err != nil {
		t.Fatal(err)
	}
	if k.N.BitLen() != 2048 || k.E != 65537 {
		t.Fatalf("key: %d bits, e=%d", k.N.BitLen(), k.E)
	}
	msg := "user=" + uriComponent("admin") + "&password=" + uriComponent(strings.Repeat("x", 64)) + "&ssd=true&token=5155a365&"
	if ct, err := rsa.EncryptPKCS1v15(rand.Reader, k, []byte(msg)); err != nil || len(hex.EncodeToString(ct)) != 512 {
		t.Fatalf("a 64-character password must fit the key: %v", err)
	}
}

// An 8-port 1930 without PoE (JL680A): the switch refuses sections it doesn't
// have with the same codes as a lost session, and sets its session cookie
// without a Path. The read has to go on regardless.
func TestModelWithoutSomeSections(t *testing.T) {
	sw := localfake.New("admin", "secret")
	defer sw.Close()
	sw.CookiePath = "-"
	sw.Sections["SystemGlobalSetting"] = strings.Replace(sw.Sections["SystemGlobalSetting"],
		"48p Gigabit CL4 PoE 4p SFP+ 370W 1930 JL686A", "8p Gigabit 2p SFP 1930 JL680A", 1)
	sw.Refused = map[string]string{"LoopbackDetectionList": "3", "MemoryLogTable": "4"}
	ctx := context.Background()
	c := New(sw.Host(), false)
	if err := c.Login(ctx, "admin", "secret"); err != nil {
		t.Fatal(err)
	}
	snap, err := Collect(ctx, c)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if !strings.HasPrefix(snap.Facts.Model, "JL680A") || len(snap.Ports) == 0 {
		t.Fatalf("facts: %+v ports=%d", snap.Facts, len(snap.Ports))
	}
	for _, p := range snap.Ports {
		if p.PoEmW != 0 {
			t.Fatal("PoE read from a switch without PoE")
		}
	}
	// a session that really ends still stops the read, with the reason
	sw.Refused = nil
	c2 := New(sw.Host(), false)
	if err := c2.Login(ctx, "admin", "secret"); err != nil {
		t.Fatal(err)
	}
	c2.Logout(ctx)
	if _, err := Collect(ctx, c2); !errors.Is(err, ErrSession) || !strings.Contains(err.Error(), "not authenticated") {
		t.Fatalf("ended session: %v", err)
	}
}
