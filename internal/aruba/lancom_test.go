package aruba

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Outputs below follow the examples in LANCOM's "GS-2300 series CLI
// Reference", padded the way a terminal shows them.

const lancomSystem = `Model Name                  : LANCOM GS-2326P+
System Description          : 26-Port Layer-2 PoE+ Managed Gigabit Ethernet Switch with 2x SFP
Location                    : Wuerselen, Germany
Contact                     : LANCOM Systems GmbH +49 (0)2405 49936-0
Device Name                 : sw-office-1
System Uptime               : 23:31:31
Current Time                : 2014-01-01 23:31:31
Firmware Version            : 3.34.0371
Serial Number               : 275032000216
Host MAC Address            : 00-a0-57-1c-71-01
CPU Load (100ms, 1s, 10s)   : 25%, 24%, 12%`

const lancomPortConfig = `Port State    Speed Duplex Flow Control Max. Frame Excessive Power
     Description
--------------------------------------------------------------------------------
1    Disabled 100 Full     -            1600       -         -
     printer
--------------------------------------------------------------------------------
2    Enabled  Auto         -            10056      -         -
--------------------------------------------------------------------------------
3    Enabled  Auto         -            10056      -         -
--------------------------------------------------------------------------------
25   Enabled  SFP_Auto_AMS Disabled     10056      Discard   Disabled
     uplink core
--------------------------------------------------------------------------------`

const lancomPortVLANCfg = `TPID for Custom S-port : 0x88a8
Port PVID Frame Type Ingress Filter Egress Rule Port Type
---- ---- ---------- -------------- ----------- -------------
1    1    All        Disabled       Hybrid      UnAware
2    10   All        Disabled       Access      UnAware
3    1    All        Disabled       Hybrid      UnAware
25   1    All        Disabled       Trunk       UnAware`

const lancomVLANTable = `VID  VLAN Name                        User       Ports
---- -------------------------------- ---------- -----------------
1    default                          Static     1,3,25
10   office                           Static     2-3,25
20   empty                            Static`

func TestParseLancomSystem(t *testing.T) {
	f := ParseLancomSystem(lancomSystem)
	if f.Hostname != "sw-office-1" || f.Model != "LANCOM GS-2326P+" || f.Software != "3.34.0371" {
		t.Fatalf("identity: %+v", f)
	}
	if f.Serial != "275032000216" || f.BaseMAC != "00:a0:57:1c:71:01" || f.Uptime != "23:31:31" || f.CPU != 12 || f.Memory != -1 {
		t.Fatalf("details: %+v", f)
	}
	if f.Location != "Wuerselen, Germany" {
		t.Fatalf("location %q", f.Location)
	}
}

func TestParseLancomPorts(t *testing.T) {
	// from a GS-2326P+ on 3.34; 25 is a combo port listed once per medium
	status := `Port/Media  State    Link           Auto Nego  Speed Duplex  Rx Pause  Tx Pause
----------  -------- -------------- ---------  ------------  --------  --------
1/UTP       Disabled Down           Enabled    Down          Disabled  Disabled
2/UTP       Enabled  Up             Enabled    1G Full       Disabled  Disabled
3/UTP       Enabled  Up             Enabled    100 Full      Disabled  Disabled
25/UTP      Enabled  Down           Enabled    Down          Disabled  Disabled
25/SFP      Enabled  Up             Enabled    1G Full       Disabled  Disabled`
	ps := ParseLancomPorts(lancomPortConfig, status, lancomPortVLANCfg)
	if len(ps) != 4 {
		t.Fatalf("got %d ports: %+v", len(ps), ps)
	}
	if ps[0].Name != "1" || ps[0].Enabled || ps[0].Up || ps[0].Description != "printer" {
		t.Fatalf("port 1: %+v", ps[0])
	}
	if !ps[1].Enabled || !ps[1].Up || ps[1].Speed != "1G Full" || ps[1].VLAN != "10" || ps[1].Mode != "access" || ps[1].Description != "" {
		t.Fatalf("port 2: %+v", ps[1])
	}
	if !ps[2].Up || ps[2].Speed != "100 Full" {
		t.Fatalf("port 3: %+v", ps[2])
	}
	if ps[3].Type != "SFP" || !ps[3].Up || ps[3].Description != "uplink core" || ps[3].Mode != "trunk" {
		t.Fatalf("port 25: %+v", ps[3])
	}

	applyLancomPoE(ps, `Primary Power Supply [W]     : 185
      PD    Maximum   Power           Current
Port  Class Power [W] Used            Used     Priority Port Status
----- ----- --------- --------------- -------- -------- ---------------------
1     0     0.0   [W] 0.0         [W] 0   [mA] Low      No PD detected
2     3     30.0  [W] 2.3         [W] 44  [mA] Low      PoE turned ON
Total -     -         10.5[W] /185[W] 200 [mA] -        -`)
	if ps[0].PoEmW != 0 || ps[1].PoEmW != 2300 {
		t.Fatalf("PoE: %d %d", ps[0].PoEmW, ps[1].PoEmW)
	}
}

func TestTidyLancom(t *testing.T) {
	// every line comes with a blank one, and paging leaves its marker behind
	in := "Port  State\n\n----\n\n1     Enabled\n\n --More--, q to quit\n\n2     Enabled\n"
	if got := tidyLancom(in); got != "Port  State\n----\n1     Enabled\n2     Enabled" {
		t.Fatalf("got %q", got)
	}
}

func TestParseLancomVLANs(t *testing.T) {
	vs := ParseLancomVLANs(lancomVLANTable, lancomPortVLANCfg)
	if len(vs) != 3 {
		t.Fatalf("got %+v", vs)
	}
	// port 1 hybrid PVID 1: untagged; 25 trunk: tagged
	if vs[0].Name != "default" || !reflect.DeepEqual(vs[0].Untagged, []string{"1", "3"}) || !reflect.DeepEqual(vs[0].Tagged, []string{"25"}) {
		t.Fatalf("VLAN 1: %+v", vs[0])
	}
	// port 2 access: untagged; 3 hybrid with PVID 1: tagged in 10
	if vs[1].Name != "office" || !reflect.DeepEqual(vs[1].Untagged, []string{"2"}) || !reflect.DeepEqual(vs[1].Tagged, []string{"3", "25"}) {
		t.Fatalf("VLAN 10: %+v", vs[1])
	}
	if vs[2].Name != "empty" || len(vs[2].Untagged)+len(vs[2].Tagged) != 0 {
		t.Fatalf("VLAN 20: %+v", vs[2])
	}
}

func TestParseLancomSTP(t *testing.T) {
	instance := `STP Configuration
Protocol Version: MSTP
Max Age        : 20
Forward Delay  : 15`
	pconf := `Port Mode     AdminEdge AutoEdge restrRole restrTcn bpduGuard Point2point
---- -------- ------------------ ------------------ ---------------------
Aggr Disabled Disabled  Enabled  Disabled  Disabled Disabled  Enabled
Port Mode     AdminEdge AutoEdge restrRole restrTcn bpduGuard Point2point
---- -------- ------------------ ------------------ ---------------------
1    Enabled  Disabled  Enabled  Disabled  Disabled Disabled  Auto
2    Enabled  Disabled  Enabled  Disabled  Disabled Disabled  Auto`
	priority := `MSTI# Bridge Priority
----- ----------------
CIST  61440`
	s := ParseLancomSTP(instance, priority, pconf, "")
	if !s.Known || !s.Enabled || s.Mode != "MSTP" || s.BridgePriority != "61440" {
		t.Fatalf("got %+v", s)
	}

	off := strings.ReplaceAll(pconf, "Enabled  Disabled  Enabled  Disabled  Disabled Disabled  Auto", "Disabled Disabled  Enabled  Disabled  Disabled Disabled  Auto")
	if ParseLancomSTP(strings.Replace(instance, "MSTP", "Compatible (STP)", 1), priority, off, "").Enabled {
		t.Fatal("no port runs STP, so it's off")
	}

	// header and bridge lines from a GS-2326P+ on 3.34; the port rows are
	// made up, as that switch runs no spanning tree
	status := `CIST Bridge STP Status
Bridge ID    : 80:00-00:A0:57:3E:C6:73
Root ID      : 10:00-00:11:22:AA:BB:CC
Root Port    : 25
Root PathCost: 20000
Regional Root: 80:00-00:A0:57:3E:C6:73
TC Flag      : Steady
TC Count     : 7
TC Last      : 2011-03-07 05:36:56
Port       Port Role       State       Pri  PathCost  Edge  P2P  Uptime
---------  --------------  ----------  ---  --------  ----  ---  -------------
25         RootPort        Forwarding  128  20000     No    Yes  65d 04:41:59
3          AlternatePort   Discarding  128  20000     No    Yes  65d 04:41:59`
	s = ParseLancomSTP(instance, priority, pconf, status)
	if s.BridgeMAC != "00:a0:57:3e:c6:73" || s.RootMAC != "00:11:22:aa:bb:cc" || s.RootPriority != "4096" || s.RootPort != "25" || s.TopologyChanges != 7 || s.IsRoot {
		t.Fatalf("status: %+v", s)
	}
	if len(s.Ports) != 2 || s.Ports[1].Role != "Alternate" || s.Ports[1].State != "Discarding" {
		t.Fatalf("ports: %+v", s.Ports)
	}

	alone := `Bridge ID    : 80:00-00:A0:57:3E:C6:73
Root ID      : 80:00-00:A0:57:3E:C6:73
Root Port    : -
TC Count     : 0
TC Last      : -`
	if s = ParseLancomSTP(instance, "", pconf, alone); !s.IsRoot || s.RootPort != "" || s.LastChange != "" || s.BridgePriority != "32768" {
		t.Fatalf("own root: %+v", s)
	}
}

func TestParseLancomSecurity(t *testing.T) {
	raw := map[string]string{
		"ssh":      "SSH Mode : Enabled",
		"https":    "HTTPS Mode          : Enabled\nHTTPS Redirect Mode : Enabled",
		"snmpmode": "SNMPv3 State Show\nSNMP State       : Enabled\nSNMPv3 Engine ID : 80001455030040c7232600",
		"snmp": `SNMP Configuration
--------------------------------
Get Community Mode : Enable
Get Community      : rose
Set Community Mode : Enable
Set Community      : private`,
		"ntp": `Index Server IP host address or a host name string
----- ------------------------------------------------------
1     64.90.182.55
2
3`,
		"syslog": "Server Mode      : Enabled\nServer Address 1 :\nServer Address 2 : 192.168.6.1\nSyslog Level     : Debug",
		"aaa": `RADIUS Authentication Server Configuration:
Server Mode     IP Address or Host Name     Port  Secret
------ -------- --------------------------- ----- ------------------------------
1      Enabled  192.168.2.22                1812  topsecret
2      Disabled                             1812
TACACS+ Authentication Server Configuration:
Server Mode     IP Address or Host Name     Port  Secret
1      Disabled                             49`,
	}
	sec := ParseLancomSecurity(raw)
	if !sec.SSHEnabled || !sec.HTTPSEnabled {
		t.Fatalf("mgmt: %+v", sec)
	}
	if !reflect.DeepEqual(sec.SNMPCommunities, []string{"rose", "private"}) || !sec.SNMPDefault {
		t.Fatalf("snmp: %+v", sec)
	}
	if !reflect.DeepEqual(sec.NTPServers, []string{"64.90.182.55"}) || !reflect.DeepEqual(sec.LogServers, []string{"192.168.6.1"}) {
		t.Fatalf("ntp/syslog: %+v", sec)
	}
	if !reflect.DeepEqual(sec.AAA, []string{"radius 192.168.2.22"}) {
		t.Fatalf("aaa: %+v", sec.AAA)
	}

	raw["snmpmode"] = "SNMP State : Disabled"
	if len(ParseLancomSecurity(raw).SNMPCommunities) != 0 {
		t.Fatal("communities don't count while SNMP is off")
	}
	if cfg := lancomConfigText(raw); strings.Contains(cfg, "topsecret") || !strings.Contains(cfg, "1 Enabled 192.168.2.22 1812 ********") {
		t.Fatalf("secret not masked:\n%s", cfg)
	}
}

func TestParseLancomThermalAndLogs(t *testing.T) {
	thermal := `Priority Temperature
-------- -----------
0        255 C
Port Priority Chip Temperature Port status
---- -------- -------------------------------------------------------------
1    0        59 C             Port link operating normally
2    0        71 C             Port link is shut down by thermal protection`
	env := ParseLancomThermal(thermal)
	if len(env) != 2 || !env[0].OK || !strings.HasPrefix(env[0].Status, "71 C") || env[1].OK || env[1].Name != "Port 2" {
		t.Fatalf("thermal: %+v", env)
	}

	// from a GS-2326P+ on 3.34: ID 1 is the newest, long messages wrap
	logs := `<4> Warning  : 60
    All      : 74
ID   Level     Time                Message
---- --------- ------------------- --------------------------------------
1    Info      2011-03-07 05:42:31  Login passed for user 'admin'
                                    through SSH from 10.98.75.2:64637 and authenticated by
                                     local method
8    Warning   2011-03-07 05:36:56  Link down on port 6
74   Warning   2011-01-01 01:00:02  Switch just made a cold boot`
	ls := ParseLancomLogs(logs, 60)
	if len(ls) != 3 || ls[0].Severity != "I" || ls[1].Severity != "W" || !strings.Contains(ls[2].Text, "cold boot") {
		t.Fatalf("logs: %+v", ls)
	}
	if want := "2011-03-07 05:42:31  Info  Login passed for user 'admin' through SSH from 10.98.75.2:64637 and authenticated by local method"; ls[0].Text != want {
		t.Fatalf("continuation:\n got %q\nwant %q", ls[0].Text, want)
	}
}

func TestDetectLancom(t *testing.T) {
	if DetectFamily("", "Type 'help' or '?' to get help.\nsw-office-1#") != FamilyLancom {
		t.Fatal("banner")
	}
	f, _ := Detect(context.Background(), nil, FamilyAuto, "Type 'help' or '?' to get help.")
	if f != FamilyLancom {
		t.Fatal("Detect should stop at the banner without running commands")
	}
}

func TestLancomConsole(t *testing.T) {
	for cmd, ok := range map[string]bool{"vlan show vlan": true, "system show": true, "LMC show": true, "show vlan": false, "vlan tag-group 5 x 1": false, "vlan show vlan; reboot": false} {
		if LancomReadOnly(cmd) != ok {
			t.Errorf("%q: want %v", cmd, ok)
		}
	}
	r := &fakeRunner{}
	if _, err := RunLancom(context.Background(), r, "vlan show port-config", time.Second); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(r.sent, []string{"vlan", "show port-config", "exit"}) {
		t.Fatalf("sent %v", r.sent)
	}
}

func TestBuildLancomVLANCommands(t *testing.T) {
	cur := ParseLancomVLANs(lancomVLANTable, lancomPortVLANCfg)
	ports := ParseLancomPorts(lancomPortConfig, "", lancomPortVLANCfg)

	got, err := BuildLancomVLANCommands(VLANChange{Action: "create", ID: 30, Name: "voice", AddUntagged: []string{"1"}, AddTagged: []string{"2"}, Save: true}, cur, ports)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"vlan", "tag-group 30 voice 1-2", "pvid 1 30", "egress-rule 2 hybrid", "exit", "save start"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("create:\n got %q\nwant %q", got, want)
	}

	got, _ = BuildLancomVLANCommands(VLANChange{Action: "update", ID: 10, RemovePorts: []string{"2"}}, cur, ports)
	want = []string{"vlan", "tag-group 10 office 3,25", "pvid 2 1", "exit"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("remove:\n got %q\nwant %q", got, want)
	}

	got, _ = BuildLancomVLANCommands(VLANChange{Action: "delete", ID: 10, Save: true}, cur, ports)
	want = []string{"vlan", "pvid 2 1", "delete group 10", "exit", "save start"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("delete:\n got %q\nwant %q", got, want)
	}

	if _, err := BuildLancomVLANCommands(VLANChange{Action: "create", ID: 40, Name: "no ports"}, cur, ports); err == nil {
		t.Fatal("a VLAN without ports or with a space in its name must be refused")
	}
}

type fakeRunner struct{ sent []string }

func (f *fakeRunner) Run(_ context.Context, cmd string, _ time.Duration) (string, error) {
	f.sent = append(f.sent, cmd)
	return "", nil
}
