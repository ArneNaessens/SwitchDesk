package core

import (
	"net"
	"testing"
	"time"

	"github.com/jaba0x/switchdesk/internal/aruba"
)

func TestPortProbe(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	_, open, _ := net.SplitHostPort(ln.Addr().String())
	if got := probePort("127.0.0.1", open); got != "open" {
		t.Fatalf("listening port: %s", got)
	}
	// a port that was just freed is refused on loopback
	tmp, _ := net.Listen("tcp", "127.0.0.1:0")
	_, closed, _ := net.SplitHostPort(tmp.Addr().String())
	tmp.Close()
	if got := probePort("127.0.0.1", closed); got != "closed" {
		t.Fatalf("closed port: %s", got)
	}
	probeTimeout = 300 * time.Millisecond
	if got := probePort("192.0.2.1", "23"); got != "filtered" { // TEST-NET-1, never answers
		t.Logf("unroutable address gave %s (fine on some networks)", got)
	}
}

func TestTelnetNotAssumed(t *testing.T) {
	// the friend's case: Telnet turned off, but the config read didn't come
	// back, so the old code assumed the AOS-S default and flagged it
	_, sec, _ := aruba.ParseConfigAOSS("")
	if sec.TelnetEnabled || sec.HTTPPlain {
		t.Fatalf("empty config must not mean telnet on: %+v", sec)
	}
	// with the probe, a refused port 23 wins over a config that looks open
	snap := aruba.Snapshot{Family: aruba.FamilyAOSS, Security: aruba.Security{TelnetEnabled: true, HTTPPlain: true}}
	probeTimeout = 500 * time.Millisecond
	applyPortProbes(&snap, "127.0.0.1") // nothing listens on 23 or 80 here
	if snap.Security.TelnetEnabled || snap.Security.HTTPPlain || snap.Security.TelnetPort == "" {
		t.Fatalf("probe should clear telnet/http: %+v", snap.Security)
	}
}
