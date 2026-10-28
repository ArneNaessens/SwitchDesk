package aruba

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// LANCOM GS-2300 / GS-1300 switches run LCOS SX 3.x. Their CLI is a tree of
// menus: "vlan" enters the VLAN menu (prompt "Name(vlan)#"), "show vlan" runs
// inside it and "exit" goes back. There is no running-config listing, so the
// settings menus' show output stands in for it. Formats follow LANCOM's
// "GS-2300 series CLI Reference"; where it gives no example (port link state,
// STP status, LLDP neighbors, log rows) the parsers are deliberately loose.

type lancomCmd struct {
	key, menu, cmd string
	config         bool // part of the settings text kept as the switch's config
	optional       bool // not every model has this menu (thermal, PoE)
}

var lancomCommands = []lancomCmd{
	{key: "system", menu: "system", cmd: "show"},
	{key: "firmware", menu: "firmware", cmd: "show"},
	{key: "ip", menu: "ip", cmd: "show", config: true},
	{key: "ports", menu: "port", cmd: "show configuration", config: true},
	{key: "portstatus", menu: "port", cmd: "show status"},
	{key: "vlans", menu: "vlan", cmd: "show vlan", config: true},
	{key: "vlanports", menu: "vlan", cmd: "show port-config", config: true},
	{key: "stp", menu: "stp", cmd: "show instance", config: true},
	{key: "stppriority", menu: "stp", cmd: "show priority", config: true},
	{key: "stpports", menu: "stp", cmd: "show pconf", config: true},
	{key: "stpstatus", menu: "stp", cmd: "show status 0"},
	{key: "lldpconfig", menu: "lldp", cmd: "show config", config: true},
	{key: "ssh", menu: "ssh", cmd: "show", config: true},
	{key: "https", menu: "https", cmd: "show", config: true},
	{key: "access", menu: "access", cmd: "show config", config: true},
	{key: "snmpmode", menu: "snmp", cmd: "show mode", config: true},
	{key: "snmp", menu: "snmp", cmd: "show snmp", config: true},
	{key: "ntp", menu: "time", cmd: "show ntp", config: true},
	{key: "time", menu: "time", cmd: "show daylight"},
	{key: "syslog", menu: "syslog", cmd: "show config", config: true},
	{key: "aaa", menu: "aaa", cmd: "show config", config: true},
	{key: "thermal", menu: "thermal", cmd: "show", optional: true},
	{key: "poe", menu: "poe", cmd: "show status", optional: true},
	{key: "logs", menu: "syslog", cmd: "show log"},
}

var lancomMoreRe = regexp.MustCompile(`(?i)^\s*--\s?more\s?--`)

// tidyLancom drops the blank line LCOS SX prints after every line and the
// "--More--, q to quit" markers left behind by paging.
func tidyLancom(out string) string {
	var keep []string
	for _, l := range strings.Split(out, "\n") {
		if strings.TrimSpace(l) == "" || lancomMoreRe.MatchString(l) {
			continue
		}
		keep = append(keep, l)
	}
	return strings.Join(keep, "\n")
}

// lancomBanner is what LCOS SX prints right after login.
func lancomBanner(banner string) bool {
	l := strings.ToLower(banner)
	return strings.Contains(l, "type 'help' or '?' to get help") || strings.Contains(l, "lancom") || strings.Contains(l, "lcos sx")
}

// lancomSession runs commands inside menus, entering a menu only when the
// next command needs a different one.
type lancomSession struct {
	ctx  context.Context
	r    Runner
	menu string
}

func (l *lancomSession) run(menu, cmd string, t time.Duration) (string, error) {
	if menu != l.menu {
		l.leave()
		out, err := l.r.Run(l.ctx, menu, 10*time.Second)
		if err == nil && IsCLIError(out) {
			err = fmt.Errorf("the switch has no %q menu", menu)
		}
		if err != nil {
			return out, err
		}
		l.menu = menu
	}
	out, err := l.r.Run(l.ctx, cmd, t)
	return tidyLancom(out), err
}

func (l *lancomSession) leave() {
	if l.menu != "" {
		l.r.Run(l.ctx, "exit", 5*time.Second)
		l.menu = ""
	}
}

func collectLancom(ctx context.Context, r Runner, s *Snapshot) {
	ls := &lancomSession{ctx: ctx, r: r}
	defer ls.leave()
	for _, c := range lancomCommands {
		if ctx.Err() != nil {
			s.Errors = append(s.Errors, "collection cancelled")
			break
		}
		out, err := ls.run(c.menu, c.cmd, 40*time.Second)
		if err != nil && !c.optional {
			s.Errors = append(s.Errors, fmt.Sprintf("%s %s: %v", c.menu, c.cmd, err))
		}
		if err != nil && c.optional {
			out = ""
		}
		s.Raw[c.key] = out
	}
	// LLDP neighbors are asked for per port, so the port list comes first
	var names []string
	for _, p := range ParseLancomPorts(s.Raw["ports"], "", "") {
		names = append(names, p.Name)
	}
	if len(names) > 0 && ctx.Err() == nil {
		out, err := ls.run("lldp", "show info "+CompressPorts(names), 40*time.Second)
		if err != nil {
			s.Errors = append(s.Errors, "lldp show info: "+err.Error())
		}
		s.Raw["lldp"] = out
	}
	parseLancom(s)
}

func parseLancom(s *Snapshot) {
	raw := s.Raw
	s.Facts = ParseLancomSystem(raw["system"])
	s.Ports = ParseLancomPorts(raw["ports"], raw["portstatus"], raw["vlanports"])
	applyLancomPoE(s.Ports, raw["poe"])
	s.VLANs = ParseLancomVLANs(raw["vlans"], raw["vlanports"])
	s.STP = ParseLancomSTP(raw["stp"], raw["stppriority"], raw["stpports"], raw["stpstatus"])
	s.Neighbors = ParseNeighbors(raw["lldp"], "lldp")
	s.Security = ParseLancomSecurity(raw)
	s.Env = ParseLancomThermal(raw["thermal"])
	s.Logs = ParseLancomLogs(raw["logs"], 60)
	s.RunningConfig = lancomConfigText(raw)
}

// ---------- system ----------

func ParseLancomSystem(text string) Facts {
	m := parseKV(text)
	f := Facts{CPU: -1, Memory: -1}
	f.Hostname = first(m, "Device Name")
	f.Model = first(m, "Model Name")
	f.Software = first(m, "Firmware Version")
	f.Serial = first(m, "Serial Number")
	f.BaseMAC = NormMAC(first(m, "Host MAC Address"))
	f.Uptime = first(m, "System Uptime", "System Up Time")
	f.Location = first(m, "Location")
	f.Contact = first(m, "Contact")
	// "CPU Load (100ms, 1s, 10s) : 25%, 24%, 24%": the 10 s average is the steadiest
	if v := first(m, "CPU Load (100ms, 1s, 10s)", "CPU Load"); v != "" {
		parts := strings.Split(v, ",")
		f.CPU = atoiLoose(parts[len(parts)-1])
	}
	return f
}

// ---------- ports ----------

var lancomPortRe = regexp.MustCompile(`^\d+[A-Z]?$`)

// ParseLancomPorts reads "port show configuration" (admin state, speed setting
// and a description line under each port), "port show status" for link state
// and "vlan show port-config" for each port's PVID and egress rule.
func ParseLancomPorts(config, status, vlanCfg string) []Port {
	var out []Port
	idx := map[string]int{}
	last := -1
	for _, line := range strings.Split(config, "\n") {
		t := strings.TrimSpace(line)
		if t == "" {
			continue
		}
		if strings.HasPrefix(t, "---") {
			last = -1
			continue
		}
		fs := strings.Fields(t)
		if len(fs) >= 2 && lancomPortRe.MatchString(fs[0]) && (strings.EqualFold(fs[1], "enabled") || strings.EqualFold(fs[1], "disabled")) {
			p := Port{Name: fs[0], Enabled: strings.EqualFold(fs[1], "enabled"), Type: "10/100/1000T"}
			if len(fs) > 2 && strings.HasPrefix(strings.ToUpper(fs[2]), "SFP") {
				p.Type = "SFP"
			}
			idx[p.Name] = len(out)
			last = len(out)
			out = append(out, p)
			continue
		}
		// the line under a port's row is its description
		if last >= 0 && !strings.EqualFold(fs[0], "port") {
			out[last].Description = t
		}
		last = -1
	}
	applyLancomStatus(out, idx, status)
	pvid, rule := lancomPortVLANs(vlanCfg)
	for i := range out {
		if v := pvid[out[i].Name]; v > 0 {
			out[i].VLAN = strconv.Itoa(v)
		}
		out[i].Mode = rule[out[i].Name]
	}
	return out
}

var lancomSpeedRe = regexp.MustCompile(`(?i)^\d+(\.\d+)?(m|g|mbps|gbps|mfdx|gfdx|mhdx|ghdx|fdx|hdx)?$`)

// applyLancomStatus takes link state from "port show status":
//
//	Port/Media  State    Link  Auto Nego  Speed Duplex  Rx Pause  Tx Pause
//	2/UTP       Enabled  Up    Enabled    1G Full       Disabled  Disabled
//
// A combo port may be listed once per medium; it's up if either is.
func applyLancomStatus(ports []Port, idx map[string]int, status string) {
	if IsCLIError(status) {
		return
	}
	for _, line := range strings.Split(status, "\n") {
		fs := strings.Fields(strings.ReplaceAll(line, "|", " "))
		if len(fs) < 3 {
			continue
		}
		name, _, _ := strings.Cut(fs[0], "/")
		i, ok := idx[name]
		if !ok {
			continue
		}
		up, speed := false, ""
		for j := 2; j < len(fs); j++ {
			l := strings.ToLower(fs[j])
			if j == 2 {
				up = l == "up"
				continue
			}
			if speed == "" && lancomSpeedRe.MatchString(fs[j]) {
				speed = fs[j]
				if j+1 < len(fs) && (strings.EqualFold(fs[j+1], "full") || strings.EqualFold(fs[j+1], "half")) {
					speed += " " + fs[j+1]
				}
			}
		}
		if up && !ports[i].Up {
			ports[i].Up, ports[i].Speed = true, speed
		}
	}
}

// applyLancomPoE reads "poe show status": the power each port draws.
//
//	Port  Class Power [W] Used            Used     Priority Port Status
//	2     3     30.0  [W] 2.3         [W] 44  [mA] Low      PoE turned ON
func applyLancomPoE(ports []Port, text string) {
	idx := map[string]int{}
	for i, p := range ports {
		idx[p.Name] = i
	}
	for _, line := range strings.Split(text, "\n") {
		fs := strings.Fields(line)
		if len(fs) < 6 || fs[3] != "[W]" || fs[5] != "[W]" {
			continue
		}
		i, ok := idx[fs[0]]
		if !ok {
			continue
		}
		if w, err := strconv.ParseFloat(fs[4], 64); err == nil && w > 0 {
			ports[i].PoEmW = int(w * 1000)
		}
	}
}

// lancomPortVLANs reads "vlan show port-config": Port PVID FrameType
// IngressFilter EgressRule PortType.
func lancomPortVLANs(text string) (map[string]int, map[string]string) {
	pvid, rule := map[string]int{}, map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		fs := strings.Fields(line)
		if len(fs) < 3 || !lancomPortRe.MatchString(fs[0]) {
			continue
		}
		v := atoiLoose(fs[1])
		if v < 1 || v > 4094 {
			continue
		}
		pvid[fs[0]] = v
		for _, f := range fs[2:] {
			switch l := strings.ToLower(f); l {
			case "access", "hybrid", "trunk":
				rule[fs[0]] = l
			}
		}
	}
	return pvid, rule
}

// ---------- VLANs ----------

var lancomVLANUsers = map[string]bool{"static": true, "combined": true, "gvrp": true, "mstp": true, "mvr": true, "nas": true, "voice": true, "vcl": true, "admin": true}
var lancomPortListRe = regexp.MustCompile(`^\d+[A-Z]?(-\d+[A-Z]?)?(,\d+[A-Z]?(-\d+[A-Z]?)?)*$`)

// ParseLancomVLANs reads "vlan show vlan" (VID, name, user, member ports) and
// works out tagging from each port's egress rule: access sends everything
// untagged, trunk tags everything, hybrid leaves only the PVID untagged.
func ParseLancomVLANs(table, portCfg string) []VLAN {
	pvid, rule := lancomPortVLANs(portCfg)
	var out []VLAN
	for _, line := range strings.Split(table, "\n") {
		fs := strings.Fields(line)
		if len(fs) < 2 {
			continue
		}
		id, err := strconv.Atoi(fs[0])
		if err != nil || id < 1 || id > 4094 {
			continue
		}
		rest, ports := fs[1:], ""
		if n := len(rest); n >= 2 && lancomPortListRe.MatchString(rest[n-1]) {
			ports, rest = rest[n-1], rest[:n-1]
		}
		if n := len(rest); n >= 2 && lancomVLANUsers[strings.ToLower(rest[n-1])] {
			rest = rest[:n-1]
		}
		v := VLAN{ID: id, Name: strings.Join(rest, " ")}
		for _, p := range ExpandPorts(ports) {
			switch {
			case rule[p] == "trunk", rule[p] == "hybrid" && pvid[p] != id:
				v.Tagged = append(v.Tagged, p)
			default:
				v.Untagged = append(v.Untagged, p)
			}
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// ---------- spanning tree ----------

// LCOS SX writes a bridge ID as the priority in hex, then the MAC:
// "80:00-00:A0:57:3E:C6:73" is priority 32768.
var lancomBridgeIDRe = regexp.MustCompile(`^([0-9A-Fa-f]{2}):?([0-9A-Fa-f]{2})-([0-9A-Fa-f]{2}(?:[-:.]?[0-9A-Fa-f]{2}){5})$`)

func lancomBridgeID(v string) (prio, mac string, ok bool) {
	m := lancomBridgeIDRe.FindStringSubmatch(strings.TrimSpace(v))
	if m == nil {
		return "", "", false
	}
	n, err := strconv.ParseUint(m[1]+m[2], 16, 16)
	if err != nil {
		return "", "", false
	}
	return strconv.Itoa(int(n)), NormMAC(m[3]), true
}

// ParseLancomSTP combines "stp show instance" (protocol), "show priority",
// "show pconf" (STP is switched on per port; there's no global switch) and
// "show status 0" (bridge, root and port roles; read loosely, as the manual
// has no example).
func ParseLancomSTP(instance, priority, pconf, status string) STP {
	s := STP{}
	if strings.TrimSpace(instance) == "" || IsCLIError(instance) {
		return s
	}
	s.Known = true
	v := first(parseKV(instance), "Protocol Version")
	switch u := strings.ToUpper(v); {
	case strings.Contains(u, "MSTP"):
		s.Mode = "MSTP"
	case strings.Contains(u, "RSTP"):
		s.Mode = "RSTP"
	case strings.Contains(u, "STP"):
		s.Mode = "STP"
	default:
		s.Mode = v
	}
	rows := 0
	for _, line := range strings.Split(pconf, "\n") {
		fs := strings.Fields(line)
		if len(fs) >= 2 && lancomPortRe.MatchString(fs[0]) {
			rows++
			if strings.EqualFold(fs[1], "enabled") {
				s.Enabled = true
			}
		}
	}
	if rows == 0 {
		s.Enabled = true // couldn't tell; don't claim it's off
	}
	for _, line := range strings.Split(priority, "\n") {
		if fs := strings.Fields(line); len(fs) >= 2 && strings.EqualFold(fs[0], "CIST") {
			s.BridgePriority = fs[1]
		}
	}

	if IsCLIError(status) {
		status = ""
	}
	// "stp show status 0": Bridge ID, Root ID, Root Port, TC Count, TC Last,
	// then a port table (Port, Port Role, State, ...)
	m := parseKV(status)
	if prio, mac, ok := lancomBridgeID(first(m, "Bridge ID")); ok {
		if s.BridgePriority == "" {
			s.BridgePriority = prio
		}
		s.BridgeMAC = mac
	}
	if prio, mac, ok := lancomBridgeID(first(m, "Root ID")); ok {
		s.RootPriority, s.RootMAC = prio, mac
	}
	if p := first(m, "Root Port"); p != "" && p != "-" {
		s.RootPort = p
	}
	if n := atoiLoose(first(m, "TC Count", "Topology Change Count")); n > 0 {
		s.TopologyChanges = n
	}
	if v := first(m, "TC Last", "Topology Change Last"); v != "-" {
		s.LastChange = v
	}
	for _, line := range strings.Split(status, "\n") {
		fs := strings.Fields(line)
		if len(fs) < 3 || !lancomPortRe.MatchString(fs[0]) {
			continue
		}
		p := STPPort{Port: fs[0]}
		for _, f := range fs[1:] {
			l := strings.ToLower(f)
			for _, r := range []string{"root", "designated", "alternate", "backup", "disabled", "master"} {
				if p.Role == "" && strings.HasPrefix(l, r) {
					p.Role = strings.ToUpper(r[:1]) + r[1:]
				}
			}
			for _, st := range []string{"forwarding", "discarding", "blocking", "learning", "listening"} {
				if strings.HasPrefix(l, st) {
					p.State = strings.ToUpper(st[:1]) + st[1:]
				}
			}
		}
		if p.Role == "" || p.State == "" {
			continue
		}
		s.Ports = append(s.Ports, p)
		if p.Role == "Root" && s.RootPort == "" {
			s.RootPort = p.Port
		}
	}
	switch {
	case s.BridgeMAC != "" && s.RootMAC != "":
		s.IsRoot = s.BridgeMAC == s.RootMAC
	case s.Enabled && s.RootPort == "" && len(s.Ports) > 0:
		s.IsRoot = true
	}
	return s
}

// ---------- management settings ----------

func lancomEnabled(v string) bool {
	l := strings.ToLower(v)
	return strings.HasPrefix(l, "enable") || l == "on" || l == "yes"
}

func ParseLancomSecurity(raw map[string]string) Security {
	sec := Security{}
	// read over SSH, so SSH is on unless the switch says otherwise
	v := first(parseKV(raw["ssh"]), "SSH Mode")
	sec.SSHEnabled = v == "" || lancomEnabled(v)
	web := parseKV(raw["https"])
	sec.HTTPSEnabled = lancomEnabled(first(web, "HTTPS Mode"))
	// "HTTP  Mode : Enabled" means the web page is also served unencrypted
	sec.HTTPPlain = lancomEnabled(first(web, "HTTP Mode"))

	if state := first(parseKV(raw["snmpmode"]), "SNMP State"); state == "" || lancomEnabled(state) {
		sm := parseKV(raw["snmp"])
		for _, k := range []string{"Get", "Set"} {
			if lancomEnabled(first(sm, k+" Community Mode")) {
				if c := first(sm, k+" Community"); c != "" {
					sec.SNMPCommunities = append(sec.SNMPCommunities, c)
				}
			}
		}
	}
	sec.SNMPDefault = hasDefaultCommunity(sec.SNMPCommunities)

	for _, line := range strings.Split(raw["ntp"], "\n") {
		if fs := strings.Fields(line); len(fs) >= 2 && atoiLoose(fs[0]) >= 1 && looksLikeHost(fs[1]) {
			sec.NTPServers = append(sec.NTPServers, fs[1])
		}
	}

	sl := parseKV(raw["syslog"])
	if lancomEnabled(first(sl, "Server Mode")) {
		for _, k := range []string{"Server Address 1", "Server Address 2", "Server Address"} {
			if a := first(sl, k); a != "" {
				sec.LogServers = append(sec.LogServers, a)
			}
		}
	}

	kind := ""
	for _, line := range strings.Split(raw["aaa"], "\n") {
		l := strings.ToLower(line)
		switch {
		case strings.Contains(l, "radius") && strings.Contains(l, "configuration"):
			kind = "radius"
		case strings.Contains(l, "tacacs") && strings.Contains(l, "server configuration"):
			kind = "tacacs+"
		}
		fs := strings.Fields(line)
		if kind != "" && len(fs) >= 3 && atoiLoose(fs[0]) >= 1 && lancomEnabled(fs[1]) {
			sec.AAA = append(sec.AAA, kind+" "+fs[2])
		}
	}
	return sec
}

// ---------- environment ----------

// ParseLancomThermal reads "thermal show". Its second table has a chip
// temperature and a status per port; anything but normal operation means the
// switch's thermal protection has acted on that port.
func ParseLancomThermal(text string) []EnvItem {
	if IsCLIError(text) {
		return nil
	}
	var items []EnvItem
	max := -1
	for _, line := range strings.Split(text, "\n") {
		fs := strings.Fields(line)
		if len(fs) < 5 || !lancomPortRe.MatchString(fs[0]) || !strings.EqualFold(fs[3], "C") {
			continue
		}
		if t := atoiLoose(fs[2]); t > max {
			max = t
		}
		if st := strings.Join(fs[4:], " "); !strings.Contains(strings.ToLower(st), "normal") {
			items = append(items, EnvItem{Kind: "temp", Name: "Port " + fs[0], Status: st, OK: false})
		}
	}
	if max >= 0 {
		items = append([]EnvItem{{Kind: "temp", Name: "Chip", Status: fmt.Sprintf("%d C, the hottest port", max), OK: true}}, items...)
	}
	return items
}

// ---------- logs ----------

var lancomLogRe = regexp.MustCompile(`^(\d+)\s+([A-Za-z]+)\s+(\d{4}-\d{2}-\d{2}\s+\d{2}:\d{2}:\d{2})\s*(.*)$`)

// ParseLancomLogs reads the "ID Level Time Message" rows of "syslog show
// log", newest first. Long messages go on over indented lines.
func ParseLancomLogs(text string, max int) []LogLine {
	type row struct {
		at   string
		line LogLine
	}
	var rows []row
	for _, line := range strings.Split(text, "\n") {
		m := lancomLogRe.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			if t := strings.TrimSpace(line); len(rows) > 0 && t != "" && strings.HasPrefix(line, " ") {
				rows[len(rows)-1].line.Text += " " + t
			}
			continue
		}
		sev := "I"
		switch strings.ToLower(m[2]) {
		case "emergency", "alert", "critical":
			sev = "M"
		case "error":
			sev = "E"
		case "warning":
			sev = "W"
		case "debug":
			sev = "D"
		}
		at := strings.Join(strings.Fields(m[3]), " ")
		rows = append(rows, row{at, LogLine{Severity: sev, Text: at + "  " + m[2] + "  " + m[4]}})
	}
	// the switch numbers its log newest first, but don't count on it
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].at > rows[j].at })
	var out []LogLine
	for _, r := range rows {
		if max > 0 && len(out) >= max {
			break
		}
		out = append(out, r.line)
	}
	return out
}

// ---------- config text ----------

// lancomConfigText puts the settings menus' show output together as the
// switch's config, for the Config tab, config copies and the checks that read
// the config. RADIUS and TACACS+ secrets are shown in clear by the switch, so
// they're masked here.
func lancomConfigText(raw map[string]string) string {
	var b strings.Builder
	for _, l := range strings.Split(raw["system"], "\n") {
		k := strings.ToLower(strings.TrimSpace(l))
		for _, p := range []string{"model name", "device name", "location", "contact"} {
			if strings.HasPrefix(k, p) {
				b.WriteString(strings.TrimSpace(l) + "\n")
			}
		}
	}
	for _, c := range lancomCommands {
		out := strings.TrimSpace(raw[c.key])
		if !c.config || out == "" || IsCLIError(out) {
			continue
		}
		if c.key == "aaa" {
			out = maskLancomSecrets(out)
		}
		fmt.Fprintf(&b, "\n! %s: %s\n%s\n", c.menu, c.cmd, out)
	}
	return strings.TrimSpace(b.String())
}

// maskLancomSecrets hides the Secret column of "aaa show config" rows
// ("1 Enabled 192.168.2.22 1812 secret").
func maskLancomSecrets(text string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		fs := strings.Fields(line)
		if len(fs) >= 5 && atoiLoose(fs[0]) >= 1 && (lancomEnabled(fs[1]) || strings.EqualFold(fs[1], "disabled")) && atoiLoose(fs[3]) >= 0 {
			lines[i] = strings.Join(append(fs[:4:4], "********"), " ")
		}
	}
	return strings.Join(lines, "\n")
}

// ---------- console ----------

var lancomConsoleRe = regexp.MustCompile(`(?i)^([a-z][a-z0-9-]*)\s+(show(?:\s+.*)?)$`)

// LancomReadOnly is the console allow-list for LANCOM: a menu followed by one
// of its show commands, such as "vlan show vlan".
func LancomReadOnly(cmd string) bool {
	c := strings.TrimSpace(cmd)
	return !strings.ContainsAny(c, "\n\r;") && lancomConsoleRe.MatchString(c)
}

// RunLancom runs "<menu> show ..." from the console: into the menu, the show
// command, and back out.
func RunLancom(ctx context.Context, r Runner, cmd string, t time.Duration) (string, error) {
	m := lancomConsoleRe.FindStringSubmatch(strings.TrimSpace(cmd))
	if m == nil {
		return "", errors.New(`type a menu and its show command, e.g. "vlan show vlan" or "system show"`)
	}
	ls := &lancomSession{ctx: ctx, r: r}
	defer ls.leave()
	return ls.run(m[1], m[2], t)
}

// ---------- VLAN changes ----------

// BuildLancomVLANCommands turns a VLAN change into LCOS SX commands. A VLAN is
// a "tag-group" with its member ports; whether a port sends it tagged depends
// on the port's PVID and egress rule, so those are adjusted too. ports gives
// each port's current PVID (Port.VLAN) and egress rule (Port.Mode).
func BuildLancomVLANCommands(c VLANChange, current []VLAN, ports []Port) ([]string, error) {
	if err := c.validate(current); err != nil {
		return nil, err
	}
	pvid, rule := map[string]int{}, map[string]string{}
	for _, p := range ports {
		pvid[p.Name], rule[p.Name] = atoiLoose(p.VLAN), p.Mode
	}
	var cur *VLAN
	for i := range current {
		if current[i].ID == c.ID {
			cur = &current[i]
		}
	}
	pick := func(list []string, keep func(string) bool) []string {
		var out []string
		for _, p := range list {
			if keep(p) {
				out = append(out, p)
			}
		}
		return out
	}
	cmds := []string{"vlan"}
	if c.Action == "delete" {
		if cur != nil {
			// ports whose PVID was this VLAN fall back to VLAN 1
			if back := pick(append(append([]string{}, cur.Untagged...), cur.Tagged...), func(p string) bool { return pvid[p] == c.ID }); len(back) > 0 {
				cmds = append(cmds, "pvid "+CompressPorts(back)+" 1")
			}
		}
		cmds = append(cmds, fmt.Sprintf("delete group %d", c.ID))
	} else {
		name := c.Name
		if name == "" && cur != nil {
			name = cur.Name
		}
		if name == "" {
			name = fmt.Sprintf("VLAN%d", c.ID)
		}
		if strings.Contains(name, " ") {
			return nil, errors.New("LANCOM VLAN names can't contain spaces")
		}
		members := map[string]bool{}
		if cur != nil {
			for _, p := range append(append([]string{}, cur.Untagged...), cur.Tagged...) {
				members[p] = true
			}
		}
		for _, p := range append(append([]string{}, c.AddUntagged...), c.AddTagged...) {
			members[p] = true
		}
		for _, p := range c.RemovePorts {
			delete(members, p)
		}
		var list []string
		for p := range members {
			list = append(list, p)
		}
		if len(list) == 0 {
			return nil, errors.New("a VLAN on a LANCOM switch needs at least one member port")
		}
		cmds = append(cmds, fmt.Sprintf("tag-group %d %s %s", c.ID, name, CompressPorts(list)))
		if len(c.AddUntagged) > 0 {
			cmds = append(cmds, fmt.Sprintf("pvid %s %d", CompressPorts(c.AddUntagged), c.ID))
			// a trunk tags everything, so it can't carry an untagged VLAN
			if t := pick(c.AddUntagged, func(p string) bool { return rule[p] == "trunk" }); len(t) > 0 {
				cmds = append(cmds, "egress-rule "+CompressPorts(t)+" hybrid")
			}
		}
		// an access port untags everything; hybrid tags all but its PVID
		if a := pick(c.AddTagged, func(p string) bool { return rule[p] == "access" }); len(a) > 0 {
			cmds = append(cmds, "egress-rule "+CompressPorts(a)+" hybrid")
		}
		if back := pick(c.RemovePorts, func(p string) bool { return pvid[p] == c.ID }); len(back) > 0 {
			cmds = append(cmds, "pvid "+CompressPorts(back)+" 1")
		}
	}
	cmds = append(cmds, "exit")
	if c.Save {
		cmds = append(cmds, "save start")
	}
	return cmds, nil
}
