package instanton

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/jaba0x/switchdesk/internal/aruba"
)

// Snapshot turns a switch from the portal inventory into the same snapshot
// that SSH collection produces, so the dashboard, switch page and map treat
// it like any other switch. siteAlerts may be nil.
func Snapshot(d Device, siteName string, siteAlerts []Alert) aruba.Snapshot {
	s := aruba.Snapshot{Family: aruba.FamilyInstantOn, Raw: map[string]string{}, CollectedAt: time.Now()}
	model := d.Model
	if d.SKU != "" && d.SKU != d.Model {
		model = d.SKU + " " + d.Model
	}
	sw := d.Software
	if sw == "" {
		sw = d.Firmware
	}
	s.Facts = aruba.Facts{
		Hostname: d.DisplayName(), Model: model, Serial: d.SerialNumber, BaseMAC: aruba.NormMAC(d.MACAddress),
		Software: sw, Uptime: uptime(d.Uptime), CPU: -1, Memory: -1, Location: d.Location,
	}
	if s.Facts.BaseMAC == "" {
		s.Facts.BaseMAC = aruba.NormMAC(d.ID)
	}

	// Instant On runs RSTP and always reports the bridge priority when it's on
	if d.StpPriority != nil {
		s.STP = aruba.STP{Known: true, Enabled: true, Mode: "RSTP", BridgePriority: fmt.Sprint(*d.StpPriority), BridgeMAC: s.Facts.BaseMAC}
		if d.IsStpRoot != nil && *d.IsStpRoot {
			s.STP.IsRoot = true
			s.STP.RootMAC = s.Facts.BaseMAC
			s.STP.RootPriority = s.STP.BridgePriority
		}
	}

	for _, p := range d.Ports {
		port := aruba.Port{
			Name: p.Label(), Enabled: !p.UserDeactivated, Up: p.IsLinkUp, Speed: speed(p.Speed),
			Description: p.Name, Loop: p.IsLoopDetected, Uplink: p.IsUplink != nil && *p.IsUplink,
		}
		if p.IsSfp {
			port.Type = "SFP"
		}
		if !p.IsLinkUp {
			port.Speed = ""
		}
		if p.PoEmW != nil {
			port.PoEmW = *p.PoEmW
		}
		if p.TrunkNumber != nil && *p.TrunkNumber > 0 {
			port.Mode = fmt.Sprintf("Trunk %d", *p.TrunkNumber)
		}
		s.Ports = append(s.Ports, port)

		// the portal knows which device sits on each port; that's what LLDP
		// would have told us, so it feeds the map the same way
		if p.PeerID != "" || p.PeerName != "" {
			s.Neighbors = append(s.Neighbors, aruba.Neighbor{
				Protocol: "cloud", LocalPort: port.Name, SysName: p.PeerName, ChassisID: p.PeerID,
				Description: p.PeerModel, Capabilities: capsFor(p.PeerModel),
			})
		}
	}
	// the uplink is reported on the device even when no port names it
	if d.UplinkDeviceID != "" && !hasPeer(s.Neighbors, d.UplinkDeviceID) {
		s.Neighbors = append(s.Neighbors, aruba.Neighbor{Protocol: "cloud", SysName: d.UplinkDeviceName, ChassisID: d.UplinkDeviceID, MgmtIP: d.UplinkDeviceIP, Capabilities: "Bridge"})
	}

	ci := &aruba.CloudInfo{Site: siteName, Health: d.Health, UpToDate: d.IsUpToDate, Firmware: d.Firmware, Clients: d.WiredClients}
	if d.PoEUsedW != nil {
		ci.PoEUsedW = *d.PoEUsedW
	}
	if d.PoEBudgetW != nil {
		ci.PoEBudgetW = *d.PoEBudgetW
	}
	seen := map[string]bool{}
	add := func(a Alert) {
		if a.Cleared > 0 || seen[a.ID] {
			return
		}
		seen[a.ID] = true
		ci.Alerts = append(ci.Alerts, aruba.CloudAlert{Severity: a.Severity, Type: a.Type, Text: alertText(a), Raised: epoch(a.RaisedTime)})
	}
	for _, a := range d.ActiveAlerts {
		add(a)
	}
	for _, a := range siteAlerts {
		if mentions(a, d) {
			add(a)
		}
	}
	sort.SliceStable(ci.Alerts, func(i, j int) bool { return rank(ci.Alerts[i].Severity) < rank(ci.Alerts[j].Severity) })
	s.Cloud = ci
	return s
}

func hasPeer(nbs []aruba.Neighbor, id string) bool {
	for _, n := range nbs {
		if strings.EqualFold(n.ChassisID, id) {
			return true
		}
	}
	return false
}

func capsFor(model string) string {
	m := strings.ToUpper(model)
	switch {
	case strings.HasPrefix(m, "AP"):
		return "WLAN"
	case strings.HasPrefix(m, "JL"), strings.HasPrefix(m, "R8"), strings.HasPrefix(m, "S0"):
		return "Bridge"
	}
	return ""
}

// mentions reports whether a site alert is about device d.
func mentions(a Alert, d Device) bool {
	name, id := strings.ToLower(d.DisplayName()), strings.ToLower(d.ID)
	var walk func(v any) bool
	walk = func(v any) bool {
		switch x := v.(type) {
		case string:
			l := strings.ToLower(x)
			return l != "" && (l == id || l == name)
		case []any:
			for _, e := range x {
				if walk(e) {
					return true
				}
			}
		case map[string]any:
			for _, e := range x {
				if walk(e) {
					return true
				}
			}
		}
		return false
	}
	return walk(a.Props)
}

// alertText turns "poeBudgetExceeded" into "PoE budget exceeded", plus the
// port when the alert names one.
func alertText(a Alert) string {
	t := words(a.Type)
	if t == "" {
		t = "Alert"
	}
	for _, k := range []string{"portNumber", "faceplatePortNumber", "portName"} {
		if v, ok := a.Props[k]; ok && v != nil && fmt.Sprint(v) != "" {
			return fmt.Sprintf("%s (port %v)", t, v)
		}
	}
	return t
}

func words(camel string) string {
	var b strings.Builder
	rs := []rune(camel)
	for i, r := range rs {
		if i > 0 && unicode.IsUpper(r) && (unicode.IsLower(rs[i-1]) || (i+1 < len(rs) && unicode.IsLower(rs[i+1]))) {
			b.WriteRune(' ')
		}
		if i == 0 {
			b.WriteRune(unicode.ToUpper(r))
		} else {
			b.WriteRune(unicode.ToLower(r))
		}
	}
	out := b.String()
	for _, w := range []string{"Poe", "poe", "Stp", "stp", "Ip", "ip", "Dhcp", "dhcp", "Wan", "wan", "Lan", "lan", "Bpdu", "bpdu", "Vlan", "vlan"} {
		up := strings.ToUpper(w)
		if up == "POE" {
			up = "PoE"
		}
		out = replaceWord(out, w, up)
	}
	return out
}

func replaceWord(s, w, with string) string {
	f := strings.Fields(s)
	for i, x := range f {
		if x == w {
			f[i] = with
		}
	}
	return strings.Join(f, " ")
}

func rank(sev string) int {
	switch sev {
	case "major":
		return 0
	case "minor":
		return 1
	}
	return 2
}

func epoch(v float64) time.Time {
	if v <= 0 {
		return time.Time{}
	}
	if v > 1e12 { // milliseconds
		return time.UnixMilli(int64(v))
	}
	return time.Unix(int64(v), 0)
}

func speed(s string) string {
	switch s {
	case "mbps10":
		return "10M"
	case "mbps100":
		return "100M"
	case "mbps1000":
		return "1G"
	case "mbps2500":
		return "2.5G"
	case "mbps5000":
		return "5G"
	case "mbps10000":
		return "10G"
	}
	return strings.TrimPrefix(s, "mbps")
}

func uptime(sec int64) string {
	if sec <= 0 {
		return ""
	}
	d := time.Duration(sec) * time.Second
	days := int(d.Hours()) / 24
	h := int(d.Hours()) % 24
	m := int(d.Minutes()) % 60
	if days > 0 {
		return fmt.Sprintf("%d days, %dh %dm", days, h, m)
	}
	return fmt.Sprintf("%dh %dm", h, m)
}
