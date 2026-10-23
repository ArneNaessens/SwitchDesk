package core

import (
	"testing"
	"time"

	"github.com/jaba0x/switchdesk/internal/aruba"
	"github.com/jaba0x/switchdesk/internal/store"
)

func TestTopologyAndIssues(t *testing.T) {
	core := store.Switch{ID: "a", Host: "10.0.0.1", Port: 22}
	acc := store.Switch{ID: "b", Host: "10.0.0.11", Port: 22}
	coreSnap := &aruba.Snapshot{
		Family: aruba.FamilyAOSCX, CollectedAt: time.Now(),
		Facts: aruba.Facts{Hostname: "core-cx", BaseMAC: "00:11:22:aa:bb:cc", CPU: 5, Memory: 20},
		STP:   aruba.STP{Known: true, Enabled: true, IsRoot: true, BridgePriority: "4096", RootMAC: "00:11:22:aa:bb:cc"},
		Neighbors: []aruba.Neighbor{
			{Protocol: "lldp", LocalPort: "1/1/1", SysName: "SW-ACCESS-01", MgmtIP: "10.0.0.11", PortID: "49"},
			{Protocol: "lldp", LocalPort: "1/1/48", SysName: "fw01", Description: "FortiGate-100F", Capabilities: "Router"},
		},
		RunningConfig: "x", Security: aruba.Security{NTPServers: []string{"1"}, LogServers: []string{"1"}, AAA: []string{"x"}},
	}
	accSnap := &aruba.Snapshot{
		Family: aruba.FamilyAOSS, CollectedAt: time.Now(),
		Facts: aruba.Facts{Hostname: "SW-ACCESS-01", CPU: 95, Memory: 10},
		STP:   aruba.STP{Known: true, Enabled: true, RootMAC: "00:11:22:aa:bb:cc", Ports: []aruba.STPPort{{Port: "50", State: "Blocking"}}},
		Neighbors: []aruba.Neighbor{
			{Protocol: "lldp", LocalPort: "49", SysName: "core-cx", ChassisID: "00 11 22 aa bb cc", PortID: "1/1/1"},
			{Protocol: "cdp", LocalPort: "49", SysName: "core-cx", PortID: "1/1/1"},
			{Protocol: "lldp", LocalPort: "5", SysName: "SEP00AABBCCDDEE", Description: "Cisco IP Phone"},
		},
		Security:      aruba.Security{TelnetEnabled: true, HTTPPlain: true, SNMPDefault: true},
		RunningConfig: "x",
		Env:           []aruba.EnvItem{{Kind: "fan", Name: "Sys-2", Status: "Fan Failed", OK: false}},
	}
	infos := []switchInfo{{sw: core, snap: coreSnap, online: true}, {sw: acc, snap: accSnap, online: true}}
	topo := buildTopology(infos, map[string]store.Point{"a": {X: 1, Y: 2}}, false)
	if len(topo.Nodes) != 3 { // core, access, firewall (phone hidden)
		t.Fatalf("nodes: %+v", topo.Nodes)
	}
	if len(topo.Edges) != 2 {
		t.Fatalf("edges: %+v", topo.Edges)
	}
	for _, e := range topo.Edges {
		if e.From == "a" && e.To == "b" && (e.FromPort != "1/1/1" || e.ToPort != "49" || len(e.Protocols) != 2) {
			t.Fatalf("core link: %+v", e)
		}
	}
	if topo.Nodes[0].X == nil || *topo.Nodes[0].X != 1 {
		t.Fatal("layout not applied")
	}
	withPhones := buildTopology(infos, nil, true)
	if len(withPhones.Nodes) != 4 {
		t.Fatalf("with endpoints: %d", len(withPhones.Nodes))
	}

	st := &Status{Online: true, Checked: true, TopologyDelta: 3}
	is := evaluate(acc, st, accSnap, store.DefaultSettings())
	want := map[string]bool{"Telnet is enabled": false, "Default SNMP community in use": false, "Fan problem": false, "High CPU: 95%": false, "3 topology change(s) since last check": false, "No manager password set": false}
	for _, i := range is {
		if _, ok := want[i.Title]; ok {
			want[i.Title] = true
		}
	}
	for k, v := range want {
		if !v {
			t.Errorf("missing issue %q in %+v", k, is)
		}
	}
	off := evaluate(core, &Status{Checked: true, Online: false, PingError: "timeout"}, nil, store.DefaultSettings())
	if len(off) != 1 || off[0].Severity != Critical {
		t.Fatalf("offline: %+v", off)
	}
	split := networkIssues(map[string]*aruba.Snapshot{"a": coreSnap, "b": {STP: aruba.STP{Enabled: true, RootMAC: "ff:ff:ff:00:00:01"}}}, map[string]string{"a": "core", "b": "acc"})
	if len(split) != 1 {
		t.Fatal("expected split-root issue")
	}
}

func TestStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.SaveCredential(store.Credential{Name: "default", Username: "admin", Password: "p@ss"})
	if err != nil || c.Password != "" {
		t.Fatal(err, c)
	}
	sw, err := s.SaveSwitch(store.Switch{Host: "10.0.0.1", CredentialID: c.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveSwitch(store.Switch{Host: "10.0.0.1"}); err == nil {
		t.Fatal("duplicate accepted")
	}
	s2, _ := store.Open(dir)
	got, err := s2.Credential(c.ID)
	if err != nil || got.Password != "p@ss" {
		t.Fatal("password roundtrip", err, got)
	}
	if err := s2.DeleteCredential(c.ID); err == nil {
		t.Fatal("deleted credential in use")
	}
	saved, _ := s2.SaveBackup(sw.ID, "hostname a\nvlan 1", 3)
	again, _ := s2.SaveBackup(sw.ID, "hostname a\nvlan 1", 3)
	if !saved || again {
		t.Fatal("backup dedupe", saved, again)
	}
}

func TestDisabledSwitch(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a, _ := st.SaveSwitch(store.Switch{Host: "10.9.9.1"})
	b, _ := st.SaveSwitch(store.Switch{Host: "10.9.9.2"})
	m := New(st, nil)
	m.status[a.ID] = &Status{Checked: true, Online: false}
	m.status[b.ID] = &Status{Checked: true, Online: false}
	if d := m.Dashboard(); d.Offline != 2 || d.Critical != 2 {
		t.Fatalf("before: %+v", d)
	}
	st.SetDisabled([]string{b.ID}, true)
	d := m.Dashboard()
	if d.Offline != 1 || d.Disabled != 1 || d.Critical != 1 || d.Total != 2 {
		t.Fatalf("after: offline=%d disabled=%d critical=%d", d.Offline, d.Disabled, d.Critical)
	}
	m.Enqueue(b.ID)
	if m.stat(b.ID).Collecting {
		t.Fatal("paused switch was queued")
	}
}
