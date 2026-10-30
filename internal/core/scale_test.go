package core

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jaba0x/switchdesk/internal/aruba"
	"github.com/jaba0x/switchdesk/internal/store"
)

// Issue #7: 284 fully read switches, started the way the app starts.
// Run with SWITCHDESK_SCALE=1.
func TestScale284(t *testing.T) {
	if os.Getenv("SWITCHDESK_SCALE") == "" {
		t.Skip("set SWITCHDESK_SCALE=1")
	}
	dir := t.TempDir()
	st, _ := store.Open(dir)
	const n = 284
	var sws []store.Switch
	for i := 0; i < n; i++ {
		sw, _ := st.SaveSwitch(store.Switch{Host: fmt.Sprintf("10.%d.%d.1", i/200, i%200), Site: fmt.Sprintf("Site %d", i/20)})
		sws = append(sws, sw)
	}
	mac := func(i int) string { return fmt.Sprintf("00:11:22:33:%02x:%02x", i/256, i%256) }
	bigText := strings.Repeat("interface 1\n   name \"something\"\n   untagged vlan 10\n   exit\n", 4000)
	for i, sw := range sws {
		s := aruba.Snapshot{Family: aruba.FamilyAOSS, CollectedAt: time.Now(), RunningConfig: bigText,
			Raw: map[string]string{"config": bigText, "lldp": bigText, "interfaces": bigText, "logs": bigText}}
		s.Facts = aruba.Facts{Hostname: fmt.Sprintf("SW-%03d+Halle", i), Model: "J9777A", Software: "YA.16.10.0012", BaseMAC: mac(i)}
		s.STP = aruba.STP{Enabled: true, Known: true, RootMAC: mac(0), BridgeMAC: mac(i)}
		for p := 1; p <= 48; p++ {
			s.Ports = append(s.Ports, aruba.Port{Name: fmt.Sprint(p), Enabled: true, Up: p%3 != 0, Speed: "1000FDx", Type: "100/1000T", Description: "desk"})
			s.STP.Ports = append(s.STP.Ports, aruba.STPPort{Port: fmt.Sprint(p), State: "Forwarding"})
		}
		for v := 1; v <= 30; v++ {
			s.VLANs = append(s.VLANs, aruba.VLAN{ID: v, Name: fmt.Sprintf("VLAN%d", v), Untagged: []string{"1", "2", "3"}, Tagged: []string{"47", "48"}})
		}
		// a mesh: every switch links to the 6 before it, plus APs and phones
		for k := 1; k <= 6; k++ {
			j := (i - k + n) % n
			s.Neighbors = append(s.Neighbors, aruba.Neighbor{Protocol: "lldp", LocalPort: fmt.Sprint(42 + k), SysName: fmt.Sprintf("SW-%03d+Halle", j), ChassisID: mac(j), PortID: fmt.Sprint(42 + k), MgmtIP: sws[j].Host})
			s.Neighbors = append(s.Neighbors, aruba.Neighbor{Protocol: "cdp", LocalPort: fmt.Sprint(42 + k), SysName: fmt.Sprintf("SW-%03d+Halle.example.com", j), PortID: "GigabitEthernet" + fmt.Sprint(42+k), MgmtIP: sws[j].Host})
		}
		for a := 0; a < 30; a++ {
			s.Neighbors = append(s.Neighbors, aruba.Neighbor{Protocol: "lldp", LocalPort: fmt.Sprint(a + 1), SysName: fmt.Sprintf("AP-%d-%d", i, a), ChassisID: fmt.Sprintf("aa:bb:%02x:%02x:%02x:01", i/256, i%256, a), Description: "Aruba AP-515", Capabilities: "wlan-access-point"})
			s.Neighbors = append(s.Neighbors, aruba.Neighbor{Protocol: "lldp", LocalPort: fmt.Sprint(a + 1), SysName: "SEP" + fmt.Sprint(i, a), Description: "Cisco IP Phone", Capabilities: "telephone"})
		}
		for l := 0; l < 60; l++ {
			s.Logs = append(s.Logs, aruba.LogLine{Text: "W 10/28/26 09:00:00 ports: port 5 is now off-line"})
		}
		st.SaveSnapshot(sw.ID, s)
	}
	t0 := time.Now()
	st2, _ := store.Open(dir)
	m := New(st2, func(string) {})
	t.Logf("open+New: %v", time.Since(t0))
	m.Start()
	defer m.Stop()
	t1 := time.Now()
	first := m.Dashboard()
	t.Logf("first Dashboard while loading: %v (loading %d)", time.Since(t1), first.Loading)
	if time.Since(t0) > time.Second {
		t.Fatalf("the window waited %v for its first answer", time.Since(t0))
	}
	t1 = time.Now()
	m.WaitLoaded()
	t.Logf("saved data loaded in the background: %v", time.Since(t1))
	for r := 0; r < 3; r++ {
		t1 := time.Now()
		d := m.Dashboard()
		t2 := time.Now()
		b, _ := json.Marshal(d)
		t.Logf("Dashboard #%d: build %v, json %v, %d KB, %d issues", r, t2.Sub(t1), time.Since(t2), len(b)/1024, len(d.Issues))
		if d.Loading != 0 || len(d.Issues) == 0 {
			t.Fatalf("dashboard after loading: loading=%d issues=%d", d.Loading, len(d.Issues))
		}
	}
	t1 = time.Now()
	topo := m.Topology(false)
	b, _ := json.Marshal(topo)
	t.Logf("Topology: %v, %d KB, %d nodes, %d edges", time.Since(t1), len(b)/1024, len(topo.Nodes), len(topo.Edges))
}
