package ionlocal

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jaba0x/switchdesk/internal/aruba"
)

// sections read on every collection, in order. Only SystemGlobalSetting and
// Ports are required; the rest fill in what they can.
var sections = []string{
	"SystemGlobalSetting", "Ports", "SpanningTreeGlobalParam", "STP", "LLDPMEDNeighborList",
	"VLANList", "VLANInterfaceMembershipTable", "PoEPSEInterfaceList", "PoEPSEUnitList",
	"DiagnosticsUnitList", "LoopbackDetectionList", "SyslogServerList", "SNTPServerTable",
	"TimeSetting", "SNMPGlobalSetting", "CommunityList", "MemoryLogTable",
}

// Collect reads a switch and builds a snapshot. The client must be logged in.
func Collect(ctx context.Context, c *Client) (aruba.Snapshot, error) {
	start := time.Now()
	got := map[string]*Node{}
	var errs []string
	for _, s := range sections {
		if ctx.Err() != nil {
			return aruba.Snapshot{}, ctx.Err()
		}
		n, err := c.Get(ctx, s)
		if err == ErrSession {
			return aruba.Snapshot{}, err
		}
		if err != nil {
			if s == "SystemGlobalSetting" || s == "Ports" {
				return aruba.Snapshot{}, err
			}
			errs = append(errs, err.Error())
			continue
		}
		got[s] = n
	}
	snap := Build(got, time.Now())
	snap.Errors = append(snap.Errors, errs...)
	snap.Duration = time.Since(start).Round(100 * time.Millisecond).String()
	return snap, nil
}

// Build turns the sections into a snapshot. now is used to judge the clock.
func Build(got map[string]*Node, now time.Time) aruba.Snapshot {
	s := aruba.Snapshot{Family: aruba.FamilyInstantOn, Web: true, Raw: map[string]string{}, CollectedAt: now}
	sys := got["SystemGlobalSetting"]

	// ---- facts
	desc := sys.Text("systemDescription")
	s.Facts = aruba.Facts{
		Hostname: sys.Text("systemName"), Model: model(desc), Serial: sys.Text("serialNumber"),
		BaseMAC: aruba.NormMAC(sys.Text("MACAddress")), Software: firstNonEmpty(sys.Text("firmwareVersion"), version(desc)),
		Uptime: ticks(sys.Text("systemUpTime")), CPU: -1, Memory: -1,
		Location: sys.Text("systemLocation"), Contact: sys.Text("systemContact"),
	}
	switch sys.Text("isRunningConfigEqualToStartupConfig") {
	case "1":
		t := true
		s.Facts.ConfigSaved = &t
	case "2":
		f := false
		s.Facts.ConfigSaved = &f
	}
	s.Raw["system"] = desc

	// ---- ports
	names := map[string]string{} // ifIndex -> port name
	present := map[string]bool{}
	loops := map[string]bool{}
	for _, e := range got["LoopbackDetectionList"].All("Entry") {
		if e.Text("operStatus") == "3" {
			loops[e.Text("interfaceName")] = true
		}
	}
	poe := map[string]int{}
	for _, e := range got["PoEPSEInterfaceList"].All("Interface") {
		if v, err := strconv.Atoi(e.Text("outputPower")); err == nil && v > 0 {
			poe[e.Text("interfaceName")] = v
		}
	}
	for _, p := range got["Ports"].All("port") {
		name, idx := p.Text("portName"), p.Text("ifIndex")
		names[idx] = name
		lag := p.Text("swIfType") == "12" || strings.HasPrefix(strings.ToUpper(name), "TRK")
		if p.Text("operStatus") == "6" { // not present, e.g. unused trunks
			continue
		}
		present[name] = true
		up := p.Text("operStatus") == "1"
		port := aruba.Port{
			Name: name, Enabled: p.Text("adminStatus") != "2", Up: up,
			PoEmW: poe[name], Loop: loops[name],
		}
		if up {
			port.Speed = speed(p.Text("ifSpeed"))
		}
		switch {
		case lag:
			port.Type = "Trunk"
		case p.Text("transType") == "2" || p.Text("swIfType") == "6":
			port.Type = "SFP+"
		default:
			port.Type = "100/1000T"
		}
		if p.Text("operStatus") == "8" || p.Text("suspStatus") == "1" {
			port.Description = "suspended"
		}
		s.Ports = append(s.Ports, port)
	}

	// ---- spanning tree
	if g := got["SpanningTreeGlobalParam"]; g != nil {
		s.STP.Known = true
		s.STP.Enabled = g.Text("enabled") == "1"
		s.STP.Mode = map[string]string{"0": "STP", "1": "STP", "2": "RSTP", "3": "MSTP"}[g.Text("STPOperationMode")]
	}
	if st := got["STP"]; st != nil {
		gs := st.Find("GlobalSetting")
		br, root := gs.Find("BridgeSetting"), gs.Find("DesignatedRoot")
		s.STP.BridgePriority = br.Text("bridgePriority")
		s.STP.BridgeMAC = aruba.NormMAC(br.Text("address"))
		if pr, mac := bridgeID(root.Text("rootBridgeID")); mac != "" {
			s.STP.RootPriority, s.STP.RootMAC = pr, mac
		}
		_, me := bridgeID(root.Text("bridgeID"))
		if me == "" {
			me = s.STP.BridgeMAC
		}
		s.STP.IsRoot = s.STP.Enabled && me != "" && me == s.STP.RootMAC
		if rp := root.Text("rootPortID"); rp != "" && rp != "0" {
			s.STP.RootPort = firstNonEmpty(names[rp], rp)
		}
		s.STP.TopologyChanges, _ = strconv.Atoi(root.Text("topologyChangeCount"))
		if !s.STP.Enabled {
			s.STP.RootMAC, s.STP.RootPriority = "", "" // a disabled bridge reports itself as root
		}
		if s.STP.Enabled {
			for _, e := range st.All("InterfaceEntry") {
				name := e.Text("interfaceName")
				if !present[name] {
					continue
				}
				s.STP.Ports = append(s.STP.Ports, aruba.STPPort{
					Port: name, Role: role(e.Text("portRole")), State: state(e.Text("portState")), Cost: e.Text("pathCost"),
				})
			}
		}
	}

	// ---- neighbors
	for _, e := range got["LLDPMEDNeighborList"].All("NeighborEntry") {
		nb := aruba.Neighbor{
			Protocol: "lldp", LocalPort: e.Text("interfaceName"), SysName: e.Text("systemName"),
			PortID: e.Text("advertisedPortID"), PortDescr: e.Text("portDescription"),
			Description: e.Text("systemDescription"), Capabilities: caps(firstNonEmpty(e.Text("systemCapabilitiesEnabled"), e.Text("systemCapabilitiesSupported"))),
		}
		id := e.Text("deviceID")
		switch e.Text("deviceIDSubtype") {
		case "4":
			nb.ChassisID = id
		case "5":
			nb.MgmtIP = id
		default:
			if aruba.NormMAC(id) != "" {
				nb.ChassisID = id
			}
		}
		for _, a := range e.All("AddrEntry") {
			if a.Text("addrSubtype") == "1" && a.Text("addr") != "" {
				nb.MgmtIP = a.Text("addr")
				break
			}
		}
		s.Neighbors = append(s.Neighbors, nb)
	}

	// ---- VLANs
	vnames := map[int]string{}
	for _, v := range got["VLANList"].All("VLAN") {
		id, _ := strconv.Atoi(v.Text("VLANID"))
		vnames[id] = v.Text("VLANName")
	}
	for _, e := range got["VLANInterfaceMembershipTable"].All("Entry") {
		id, err := strconv.Atoi(e.Text("VLANID"))
		if err != nil {
			continue
		}
		v := aruba.VLAN{ID: id, Name: firstNonEmpty(e.Text("VLANName"), vnames[id]), Status: "Up",
			Tagged: ports(e.Text("taggedPorts"), present), Untagged: ports(e.Text("untaggedPorts"), present)}
		if e.Text("operStatus") == "2" {
			v.Status = "Down"
		}
		if v.Name == "" && id == 1 {
			v.Name = "DEFAULT_VLAN"
		}
		s.VLANs = append(s.VLANs, v)
	}
	vlanOf := map[string]string{}
	for _, v := range s.VLANs {
		for _, p := range v.Untagged {
			vlanOf[p] = strconv.Itoa(v.ID)
		}
	}
	for i := range s.Ports {
		s.Ports[i].VLAN = vlanOf[s.Ports[i].Name]
	}

	// ---- hardware
	if d := got["DiagnosticsUnitList"].Find("Entry"); d != nil {
		for i := 1; i <= 6; i++ {
			if st := d.Text(fmt.Sprintf("fan%dStatus", i)); st != "" && st != "5" {
				s.Env = append(s.Env, envItem("fan", strconv.Itoa(i), st, ""))
			}
		}
		for _, ps := range []struct{ tag, name string }{{"mainPSStatus", "Main"}, {"redundantPSStatus", "Redundant"}} {
			if st := d.Text(ps.tag); st != "" && st != "5" {
				s.Env = append(s.Env, envItem("psu", ps.name, st, ""))
			}
		}
		if st := firstNonEmpty(d.Text("tempSensorStatus"), d.Text("temperatureStatus")); st != "" && st != "5" {
			s.Env = append(s.Env, envItem("temp", "", st, d.Text("tempSensorValue")))
		}
	}

	// ---- management
	sec := &s.Security
	for _, e := range got["SyslogServerList"].All("SyslogServer") {
		if a := e.Text("IPAddr"); a != "" {
			sec.LogServers = append(sec.LogServers, a)
		}
	}
	answered := false
	for _, e := range got["SNTPServerTable"].All("SNTPServerEntry") {
		if a := e.Text("IPAddress"); a != "" {
			sec.NTPServers = append(sec.NTPServers, a)
			if lr := e.Text("lastResponse"); lr != "" && !strings.HasPrefix(lr, "0101190") {
				answered = true
			}
		}
	}
	if snmp := got["SNMPGlobalSetting"]; snmp != nil && snmp.Text("SNMPEnbl") == "1" {
		for _, n := range got["CommunityList"].All("communityName") {
			c := strings.TrimSpace(n.Value)
			sec.SNMPCommunities = append(sec.SNMPCommunities, c)
			if c == "public" || c == "private" {
				sec.SNMPDefault = true
			}
		}
	}
	sec.HTTPSEnabled = true // the web interface always offers HTTPS

	// clock: the switch reports its local time and its UTC offset
	if t := got["TimeSetting"]; t != nil {
		if clock, ok := switchClock(t); ok {
			off := now.Sub(clock)
			if off < 0 {
				off = -off
			}
			if off > 5*time.Minute {
				detail := "The switch's clock reads " + clock.Format("Jan 2 2006 15:04") + " (UTC). Log times and certificate checks depend on it."
				switch {
				case len(sec.NTPServers) > 0 && !answered:
					detail += " The SNTP server " + strings.Join(sec.NTPServers, ", ") + " has never answered."
				case len(sec.NTPServers) == 0:
					detail += " No SNTP server is set."
				}
				s.Notes = append(s.Notes, aruba.Note{Severity: "warning", Category: "Management", Title: "Clock is off by " + human(off), Detail: detail})
			}
		}
	}
	if len(sec.NTPServers) > 0 && !answered && len(s.Notes) == 0 {
		s.Notes = append(s.Notes, aruba.Note{Severity: "warning", Category: "Management", Title: "SNTP server never answered", Detail: "Configured: " + strings.Join(sec.NTPServers, ", ") + ". Check that the switch can reach it on UDP 123."})
	}

	// loop protection off on every port is worth knowing when STP is off too
	ld := got["LoopbackDetectionList"].All("Entry")
	if len(ld) > 0 && !s.STP.Enabled {
		on := false
		for _, e := range ld {
			if e.Text("adminStatus") == "1" {
				on = true
			}
		}
		if !on {
			s.Notes = append(s.Notes, aruba.Note{Severity: "warning", Category: "Spanning tree", Title: "No loop protection", Detail: "Spanning tree and loop protection are both off, so nothing stops a looped cable from flooding this segment."})
		}
	}

	// ---- logs, newest first
	type entry struct {
		no   int
		line aruba.LogLine
	}
	var logs []entry
	for _, e := range got["MemoryLogTable"].All("LogEntry") {
		no, _ := strconv.Atoi(e.Text("logNo"))
		logs = append(logs, entry{no, aruba.LogLine{Severity: logSev(e.Text("severityType")), Text: strings.TrimSpace(logTime(e.Text("dateTime")) + " " + e.Text("message"))}})
	}
	sort.SliceStable(logs, func(i, j int) bool { return logs[i].no > logs[j].no })
	for i, l := range logs {
		if i >= 60 {
			break
		}
		s.Logs = append(s.Logs, l.line)
	}
	return s
}

func envItem(kind, name, status, value string) aruba.EnvItem {
	label := map[string]string{"1": "OK", "2": "Warning", "3": "Critical", "4": "Shut down", "6": "Not working"}[status]
	if label == "" {
		label = "status " + status
	}
	if value != "" {
		label = value + "°C, " + label
	}
	return aruba.EnvItem{Kind: kind, Name: name, Status: label, OK: status == "1"}
}

var modelRe = regexp.MustCompile(`\b(JL\d{3}A|R8[A-Z0-9]{3}A|S0[A-Z0-9]{3}A)\b`)

// model turns "HPE Networking Instant On Switch 48p Gigabit CL4 PoE 4p SFP+
// 370W 1930 JL686A, InstantOn_1930_3.3.2.0 (5)" into "JL686A 1930 48p
// Gigabit CL4 PoE 4p SFP+ 370W".
func model(desc string) string {
	d := strings.SplitN(desc, ",", 2)[0]
	for _, p := range []string{"HPE Networking Instant On Switch ", "HPE Aruba Networking Instant On Switch ", "Aruba Instant On Switch ", "Instant On Switch "} {
		d = strings.TrimPrefix(d, p)
	}
	d = strings.TrimSpace(d)
	if m := modelRe.FindString(d); m != "" {
		rest := strings.TrimSpace(strings.Replace(d, m, "", 1))
		f := strings.Fields(rest)
		// move the series number (1930) to the front
		for i, w := range f {
			if len(w) == 4 && (strings.HasPrefix(w, "18") || strings.HasPrefix(w, "19")) {
				f = append([]string{w}, append(f[:i:i], f[i+1:]...)...)
				break
			}
		}
		return strings.TrimSpace(m + " " + strings.Join(f, " "))
	}
	return d
}

func version(desc string) string {
	if i := strings.Index(desc, ","); i >= 0 {
		return strings.TrimSpace(desc[i+1:])
	}
	return ""
}

// ticks formats sysUpTime in hundredths of a second.
func ticks(v string) string {
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n <= 0 {
		return ""
	}
	d := time.Duration(n/100) * time.Second
	days := int(d.Hours()) / 24
	if days > 0 {
		return fmt.Sprintf("%d days, %dh %dm", days, int(d.Hours())%24, int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
}

func speed(mbps string) string {
	switch mbps {
	case "10":
		return "10M"
	case "100":
		return "100M"
	case "1000":
		return "1G"
	case "2500":
		return "2.5G"
	case "5000":
		return "5G"
	case "10000":
		return "10G"
	case "", "0":
		return ""
	}
	return mbps + "M"
}

func bridgeID(s string) (prio, mac string) {
	if i := strings.Index(s, "-"); i > 0 {
		return s[:i], aruba.NormMAC(s[i+1:])
	}
	return "", aruba.NormMAC(s)
}

func role(v string) string {
	return firstNonEmpty(map[string]string{"1": "Disabled", "2": "Alternate", "3": "Backup", "4": "Root", "5": "Designated", "6": "Master"}[v], "Unknown")
}

func state(v string) string {
	return firstNonEmpty(map[string]string{"1": "Disabled", "2": "Blocking", "3": "Listening", "4": "Learning", "5": "Forwarding", "6": "Broken"}[v], "Disabled")
}

// caps reads the LLDP capability bit string (bit 0 first).
func caps(bits string) string {
	names := []string{"other", "repeater", "bridge", "wlan-access-point", "router", "telephone", "docsis", "station"}
	var out []string
	for i, c := range bits {
		if c == '1' && i < len(names) {
			out = append(out, names[i])
		}
	}
	return strings.Join(out, ", ")
}

var trkRange = regexp.MustCompile(`(?i)^(TRK)(\d+)-(\d+)$`)

// ports expands "1-48,49-52,TRK1-16", keeping only ports that exist.
func ports(list string, present map[string]bool) []string {
	var parts []string
	for _, p := range strings.Split(list, ",") {
		p = strings.TrimSpace(p)
		if m := trkRange.FindStringSubmatch(p); m != nil {
			p = m[1] + m[2] + "-" + m[1] + m[3]
		}
		if p != "" {
			parts = append(parts, p)
		}
	}
	var out []string
	for _, p := range aruba.ExpandPorts(strings.Join(parts, ",")) {
		if present[p] {
			out = append(out, p)
		}
	}
	return out
}

func switchClock(t *Node) (time.Time, bool) {
	c := t.Find("CurrentLocalTime")
	if c == nil {
		return time.Time{}, false
	}
	num := func(n *Node, k string) int { v, _ := strconv.Atoi(n.Text(k)); return v }
	if num(c, "year") < 2000 {
		return time.Time{}, false
	}
	tz := t.Find("TimeZone")
	offH, offM := 0, 0
	if tz != nil {
		offH, offM = num(tz, "offsetHour"), num(tz, "offsetMinute")
		if strings.HasPrefix(tz.Text("offsetHour"), "-") && offM > 0 {
			offM = -offM
		}
	}
	loc := time.FixedZone("switch", offH*3600+offM*60)
	return time.Date(num(c, "year"), time.Month(num(c, "month")), num(c, "day"), num(c, "hour"), num(c, "minute"), num(c, "second"), 0, loc).UTC(), true
}

func human(d time.Duration) string {
	switch {
	case d >= 48*time.Hour:
		return fmt.Sprintf("%d days", int(d.Hours()/24))
	case d >= 2*time.Hour:
		return fmt.Sprintf("%d hours", int(d.Hours()))
	}
	return fmt.Sprintf("%d minutes", int(d.Minutes()))
}

func logSev(v string) string {
	n, err := strconv.Atoi(v)
	switch {
	case err != nil:
		return "I"
	case n <= 3:
		return "E"
	case n == 4:
		return "W"
	case n >= 7:
		return "D"
	}
	return "I"
}

// logTime turns "01282026 03:05:45" into "01/28/26 03:05:45".
func logTime(v string) string {
	f := strings.Fields(v)
	if len(f) == 2 && len(f[0]) == 8 {
		return f[0][0:2] + "/" + f[0][2:4] + "/" + f[0][6:8] + " " + f[1]
	}
	return v
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}
