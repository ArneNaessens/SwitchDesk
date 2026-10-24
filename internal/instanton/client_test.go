package instanton

import (
	"context"
	"errors"
	"testing"

	"github.com/jaba0x/switchdesk/internal/instanton/ionfake"
)

func portal(t *testing.T) *ionfake.Portal {
	p := ionfake.New("ops@example.com", "secret")
	t.Cleanup(p.Close)
	p.Sites = []map[string]any{{"id": "site-1", "name": "Branch One", "kind": "site", "status": "up"}}
	p.Inventory["site-1"] = []map[string]any{
		ionfake.Switch("aa:bb:cc:00:00:01", "IO-SW-1", "10.9.0.2", "JL681A",
			ionfake.Port(1, true, map[string]any{"isUplink": true, "directlyConnectedDeviceId": "aa:bb:cc:00:00:02", "directlyConnectedDeviceName": "IO-SW-2", "directlyConnectedDeviceModel": "JL683A"}),
			ionfake.Port(2, true, map[string]any{"powerProvidedInMilliwatts": 6400, "directlyConnectedDeviceId": "aa:bb:cc:00:00:09", "directlyConnectedDeviceName": "AP-Lobby", "directlyConnectedDeviceModel": "AP22"}),
			ionfake.Port(3, false, nil),
			ionfake.Port(4, true, map[string]any{"isLoopDetected": true}),
		),
		{"id": "aa:bb:cc:00:00:09", "deviceType": "accessPoint", "name": "AP-Lobby", "model": "AP22", "status": "up"},
	}
	return p
}

func client(p *ionfake.Portal, email, pass string) *Client {
	c := New(email, pass)
	c.Portal = p.URL
	return c
}

func TestLoginAndInventory(t *testing.T) {
	p := portal(t)
	c := client(p, "ops@example.com", "secret")
	ctx := context.Background()
	sites, err := c.Sites(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(sites) != 1 || sites[0].Name != "Branch One" {
		t.Fatalf("sites: %+v", sites)
	}
	devs, err := c.Inventory(ctx, "site-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(devs) != 2 || !devs[0].IsSwitch() || devs[1].IsSwitch() {
		t.Fatalf("devices: %+v", devs)
	}
	if p.Logins != 1 {
		t.Fatalf("logins: %d", p.Logins)
	}
}

func TestBadPasswordAndMFA(t *testing.T) {
	p := portal(t)
	if _, err := client(p, "ops@example.com", "wrong").Sites(context.Background()); !errors.Is(err, ErrLogin) {
		t.Fatalf("wrong password: %v", err)
	}
	p.MFA = true
	if _, err := client(p, "ops@example.com", "secret").Sites(context.Background()); !errors.Is(err, ErrLogin) {
		t.Fatalf("MFA account: %v", err)
	}
}

// A token the portal stops accepting is refreshed once, without logging in again.
func TestExpiredTokenIsRefreshed(t *testing.T) {
	p := portal(t)
	c := client(p, "ops@example.com", "secret")
	ctx := context.Background()
	if _, err := c.Sites(ctx); err != nil {
		t.Fatal(err)
	}
	p.ExpireNext = true
	if _, err := c.Inventory(ctx, "site-1"); err != nil {
		t.Fatal(err)
	}
	if p.Logins != 1 || p.Refreshes != 1 {
		t.Fatalf("logins %d, refreshes %d", p.Logins, p.Refreshes)
	}
}

func TestSnapshot(t *testing.T) {
	p := portal(t)
	devs, err := client(p, "ops@example.com", "secret").Inventory(context.Background(), "site-1")
	if err != nil {
		t.Fatal(err)
	}
	alerts := []Alert{
		{ID: "a1", Type: "poeBudgetExceeded", Severity: "minor", RaisedTime: 1761200000000, Props: map[string]any{"deviceNames": []any{"IO-SW-1"}}},
		{ID: "a2", Type: "deviceDisconnected", Severity: "major", Props: map[string]any{"deviceNames": []any{"someone else"}}},
		{ID: "a3", Type: "loopDetected", Severity: "major", Cleared: 1761200000001, Props: map[string]any{"deviceNames": []any{"IO-SW-1"}}},
	}
	s := Snapshot(devs[0], "Branch One", alerts)
	if s.Facts.Hostname != "IO-SW-1" || s.Facts.BaseMAC != "aa:bb:cc:00:00:01" || s.Facts.Uptime != "38 days, 5h 2m" || s.Facts.CPU != -1 {
		t.Fatalf("facts: %+v", s.Facts)
	}
	if len(s.Ports) != 4 || !s.Ports[0].Up || !s.Ports[0].Uplink || s.Ports[1].PoEmW != 6400 || s.Ports[2].Up || !s.Ports[3].Loop || s.Ports[0].Speed != "1G" {
		t.Fatalf("ports: %+v", s.Ports)
	}
	if len(s.Neighbors) != 2 || s.Neighbors[0].ChassisID != "aa:bb:cc:00:00:02" || s.Neighbors[0].LocalPort != "1" || s.Neighbors[1].Capabilities != "WLAN" {
		t.Fatalf("neighbors: %+v", s.Neighbors)
	}
	if !s.STP.Known || !s.STP.Enabled || s.STP.BridgePriority != "32768" || s.STP.IsRoot {
		t.Fatalf("stp: %+v", s.STP)
	}
	c := s.Cloud
	if c == nil || c.Site != "Branch One" || c.PoEBudgetW != 195 || len(c.Alerts) != 1 || c.Alerts[0].Text != "PoE budget exceeded" || c.Alerts[0].Raised.IsZero() {
		t.Fatalf("cloud: %+v", c)
	}
	if s.Security.TelnetEnabled || s.RunningConfig != "" {
		t.Fatal("cloud switches have no config or telnet to judge")
	}
}

func TestWords(t *testing.T) {
	for in, want := range map[string]string{"poeBudgetExceeded": "PoE budget exceeded", "deviceDisconnected": "Device disconnected", "stpTopologyChange": "STP topology change", "": ""} {
		if got := words(in); got != want {
			t.Errorf("words(%q) = %q, want %q", in, got, want)
		}
	}
}
