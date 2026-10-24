package core

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jaba0x/switchdesk/internal/instanton"
	"github.com/jaba0x/switchdesk/internal/instanton/ionfake"
	"github.com/jaba0x/switchdesk/internal/store"
)

func cloudSetup(t *testing.T) (*Manager, *store.Store, *ionfake.Portal, string) {
	p := ionfake.New("ops@example.com", "secret")
	t.Cleanup(p.Close)
	p.Sites = []map[string]any{{"id": "site-1", "name": "Saburtalo"}, {"id": "site-2", "name": "Vake"}}
	p.Inventory["site-1"] = []map[string]any{
		ionfake.Switch("aa:bb:cc:00:00:01", "IO-SW-1", "10.9.0.2", "JL681A",
			ionfake.Port(1, true, map[string]any{"isUplink": true, "directlyConnectedDeviceId": "aa:bb:cc:00:00:02", "directlyConnectedDeviceName": "IO-SW-2"}),
			ionfake.Port(7, true, map[string]any{"isLoopDetected": true}),
		),
		ionfake.Switch("aa:bb:cc:00:00:02", "IO-SW-2", "10.9.0.3", "JL683A",
			ionfake.Port(24, true, map[string]any{"directlyConnectedDeviceId": "aa:bb:cc:00:00:01", "directlyConnectedDeviceName": "IO-SW-1"}),
		),
		{"id": "aa:bb:cc:00:00:09", "deviceType": "accessPoint", "name": "AP-Lobby", "model": "AP22", "status": "up"},
	}
	p.Inventory["site-2"] = []map[string]any{ionfake.Switch("aa:bb:cc:00:00:05", "VAKE-SW", "10.9.1.2", "JL681A")}
	p.Alerts["site-1"] = []map[string]any{{"id": "x1", "type": "poeBudgetExceeded", "severity": "minor", "alertTypeProperties": map[string]any{"deviceNames": []any{"IO-SW-2"}}}}

	old := newPortal
	newPortal = func(email, password string) *instanton.Client {
		c := instanton.New(email, password)
		c.Portal = p.URL
		return c
	}
	t.Cleanup(func() { newPortal = old })

	st, _ := store.Open(t.TempDir())
	cred, err := st.SaveCredential(store.Credential{Username: "ops@example.com", Password: "secret", Kind: store.KindInstantOn})
	if err != nil {
		t.Fatal(err)
	}
	m := New(st, nil)
	m.ctx, m.stop = contextWithCancel()
	t.Cleanup(m.stop)
	return m, st, p, cred.ID
}

func TestCloudDiscoverAddAndRead(t *testing.T) {
	m, st, p, cred := cloudSetup(t)
	sites, err := m.DiscoverCloud(cred)
	if err != nil {
		t.Fatal(err)
	}
	if len(sites) != 2 || sites[0].Name != "Saburtalo" || len(sites[0].Switches) != 2 || len(sites[1].Switches) != 1 {
		t.Fatalf("discovered: %+v", sites) // the access point isn't offered
	}
	var picks []CloudPick
	for _, d := range sites[0].Switches {
		picks = append(picks, CloudPick{SiteID: sites[0].ID, SiteName: sites[0].Name, DeviceID: d.DeviceID, Name: d.Name, IP: d.IP})
	}
	if n, err := m.AddCloud(cred, picks); err != nil || n != 2 {
		t.Fatalf("added %d: %v", n, err)
	}
	// adding the same switch again is refused
	if n, err := m.AddCloud(cred, picks[:1]); n != 0 || err == nil {
		t.Fatalf("duplicate added: %d %v", n, err)
	}
	again, _ := m.DiscoverCloud(cred)
	if again[0].Switches[0].AddedAs == "" {
		t.Fatal("already-added switch not marked")
	}

	var sw1, sw2 store.Switch
	for _, sw := range st.Switches() {
		switch sw.Cloud.DeviceID {
		case "aa:bb:cc:00:00:01":
			sw1 = sw
		case "aa:bb:cc:00:00:02":
			sw2 = sw
		}
	}
	m.collect(sw1.ID)
	m.collect(sw2.ID)
	d1, _ := m.Detail(sw1.ID)
	if !d1.Status.Online || d1.Snapshot == nil || d1.Snapshot.Facts.Model != "JL681A" || d1.Snapshot.Cloud == nil {
		t.Fatalf("detail: %+v", d1.Status)
	}
	titles := func(is []Issue) string {
		var t []string
		for _, i := range is {
			t = append(t, i.Title)
		}
		return strings.Join(t, " | ")
	}
	if got := titles(d1.Issues); !strings.Contains(got, "Loop detected") || strings.Contains(got, "Telnet") || strings.Contains(got, "NTP") {
		t.Fatalf("switch 1 problems: %s", got)
	}
	d2, _ := m.Detail(sw2.ID)
	if got := titles(d2.Issues); !strings.Contains(got, "PoE budget exceeded") {
		t.Fatalf("switch 2 problems: %s", got)
	}

	// one link between the two Instant On switches, reported from both sides
	topo := m.Topology(false)
	links := 0
	for _, e := range topo.Edges {
		if (e.From == sw1.ID && e.To == sw2.ID) || (e.From == sw2.ID && e.To == sw1.ID) {
			links++
		}
	}
	if links != 1 {
		t.Fatalf("links between the switches: %d (%+v)", links, topo.Edges)
	}

	// status comes from the portal; both switches share one inventory fetch
	before := p.Calls("/api/sites/site-1/inventory")
	p.SetStatus("site-1", "aa:bb:cc:00:00:02", "down")
	cloudFresh = 0
	t.Cleanup(func() { cloudFresh = 25 * time.Second })
	m.pingAll()
	d2, _ = m.Detail(sw2.ID)
	if d2.Status.Online || !strings.Contains(titles(d2.Issues), "Switch is offline") {
		t.Fatalf("down switch: %+v %s", d2.Status, titles(d2.Issues))
	}
	cloudFresh = time.Minute
	m.pingAll()
	m.pingAll()
	if calls := p.Calls("/api/sites/site-1/inventory") - before; calls > 2 {
		t.Fatalf("inventory fetched %d times for 3 pings of 2 switches", calls)
	}

	// SSH-only actions explain themselves
	if _, err := m.RunCommand(sw1.ID, "show version"); !errors.Is(err, ErrCloudOnly) {
		t.Fatalf("console: %v", err)
	}
}

func TestCloudLoginFailure(t *testing.T) {
	m, st, p, cred := cloudSetup(t)
	sw, err := st.SaveSwitch(store.Switch{CredentialID: cred, Cloud: &store.CloudRef{SiteID: "site-2", SiteName: "Vake", DeviceID: "aa:bb:cc:00:00:05"}})
	if err != nil {
		t.Fatal(err)
	}
	p.Password = "changed"
	m.collect(sw.ID)
	d, _ := m.Detail(sw.ID)
	if !d.Status.AuthFailed || !strings.Contains(d.Issues[0].Title, "Instant On login failed") {
		t.Fatalf("status %+v issues %+v", d.Status, d.Issues)
	}
}

func contextWithCancel() (context.Context, context.CancelFunc) {
	return context.WithCancel(context.Background())
}
