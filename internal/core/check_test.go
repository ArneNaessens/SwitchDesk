package core

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/jaba0x/switchdesk/internal/sshx"
	"github.com/jaba0x/switchdesk/internal/store"
)

func TestParsePing(t *testing.T) {
	cases := []struct {
		name, out string
		err       error
		ms        int
		ok        bool
	}{
		{"mac", "64 bytes from 10.0.0.1: icmp_seq=0 ttl=64 time=12.6 ms\n", nil, 13, true},
		{"linux sub-ms", "64 bytes from 10.0.0.1: icmp_seq=1 ttl=64 time=0.412 ms", nil, 0, true},
		{"windows", "Reply from 10.0.0.1: bytes=32 time=7ms TTL=64", nil, 7, true},
		{"windows fast", "Reply from 10.0.0.1: bytes=32 time<1ms TTL=64", nil, 1, true},
		{"windows german", "Antwort von 10.0.0.1: Bytes=32 Zeit=21ms TTL=63", nil, 21, true},
		// Windows exits 0 when a router answers "unreachable"
		{"windows unreachable", "Reply from 10.0.0.254: Destination host unreachable.", nil, 0, false},
		{"timeout", "Request timeout for icmp_seq 0", errors.New("exit status 2"), 0, false},
	}
	for _, c := range cases {
		ms, err := parsePing(c.out, c.err, 99)
		if (err == nil) != c.ok || (c.ok && ms != c.ms) {
			t.Errorf("%s: got %d %v", c.name, ms, err)
		}
	}
	if _, err := parsePing("ping: cannot resolve sw1: Unknown host", errors.New("exit status 68"), 0); err == nil || err.Error() != "unknown host" {
		t.Errorf("unknown host: %v", err)
	}
}

func newCheckManager(t *testing.T, method string) (*Manager, *store.Store, store.Switch) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	set := st.Settings()
	set.CheckMethod = method
	if err := st.SaveSettings(set); err != nil {
		t.Fatal(err)
	}
	sw, _ := st.SaveSwitch(store.Switch{Host: "10.9.9.1", Port: 22})
	m := New(st, nil)
	m.ctx = context.Background()
	return m, st, sw
}

func TestCheckOffMakesNoConnections(t *testing.T) {
	pinged := 0
	defer func(f func(context.Context, string, time.Duration) (int, error)) { icmpPing = f }(icmpPing)
	icmpPing = func(context.Context, string, time.Duration) (int, error) { pinged++; return 1, nil }

	m, _, sw := newCheckManager(t, store.CheckOff)
	m.pingAll()
	m.ping(sw)
	if st := m.stat(sw.ID); st.Checked || pinged != 0 {
		t.Fatalf("checks off still checked: %+v pinged=%d", st, pinged)
	}
	// a read that can't reach the switch marks it offline
	m.readReach(sw.ID, &net.OpError{Op: "dial", Err: errors.New("i/o timeout")})
	if st := m.stat(sw.ID); !st.Checked || st.Online {
		t.Fatalf("after failed read: %+v", st)
	}
	// a wrong password still means the switch is there
	m.readReach(sw.ID, errors.New("ssh: handshake failed: ssh: unable to authenticate"))
	if st := m.stat(sw.ID); !st.Online {
		t.Fatalf("auth failure should count as online: %+v", st)
	}
	m.readReach(sw.ID, sshx.ErrHostKeyMismatch)
	if !m.stat(sw.ID).Online {
		t.Fatal("host key change should count as online")
	}
}

func TestCheckPingUsesICMP(t *testing.T) {
	var host string
	defer func(f func(context.Context, string, time.Duration) (int, error)) { icmpPing = f }(icmpPing)
	icmpPing = func(_ context.Context, h string, _ time.Duration) (int, error) { host = h; return 4, nil }

	m, _, sw := newCheckManager(t, store.CheckPing)
	m.ping(sw)
	st := m.stat(sw.ID)
	if host != "10.9.9.1" || !st.Online || st.LatencyMs != 4 {
		t.Fatalf("ping check: host=%q %+v", host, st)
	}
	// reads don't override the background check when it's on
	m.readReach(sw.ID, &net.OpError{Op: "dial", Err: errors.New("refused")})
	if !m.stat(sw.ID).Online {
		t.Fatal("read changed status while checks are on")
	}
}

func TestCheckMethodSetting(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got := st.Settings().CheckMethod; got != store.CheckPort {
		t.Fatalf("default: %q", got)
	}
	set := st.Settings()
	set.CheckMethod = "bogus"
	st.SaveSettings(set)
	if got := st.Settings().CheckMethod; got != store.CheckPort {
		t.Fatalf("unknown value kept: %q", got)
	}
	// the choice survives a restart
	dir := st.Dir()
	set.CheckMethod = store.CheckOff
	st.SaveSettings(set)
	st2, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := st2.Settings().CheckMethod; got != store.CheckOff {
		t.Fatalf("reload: %q", got)
	}
}
