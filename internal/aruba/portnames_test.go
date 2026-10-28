package aruba

import (
	"reflect"
	"testing"
)

// Issue #9: show the port names set in the switch config.
func TestPortNames(t *testing.T) {
	aoss := "hostname \"sw1\"\n" +
		"interface 3\n   name \"Printer-2nd-floor\"\n   exit\n" +
		"interface 4\n   name \"AccessPoint Office\"\n   exit\n" +
		"interface 5\n   disable\n   exit\n" +
		"interface 21-22\n   name \"Cameras\"\n   exit\n" +
		"interface Trk1\n   name \"Uplink-Switch02\"\n   exit\n" +
		"vlan 1\n   name \"DEFAULT_VLAN\"\n   untagged 1-24\n   exit\n"
	want := map[string]string{"3": "Printer-2nd-floor", "4": "AccessPoint Office", "21": "Cameras", "22": "Cameras", "Trk1": "Uplink-Switch02"}
	if got := PortNames(aoss); !reflect.DeepEqual(got, want) {
		t.Fatalf("aos-s: %v", got)
	}

	cx := "interface 1/1/1\n    description Core uplink to dc-sw01\n    no shutdown\n" +
		"interface 1/1/2\n    no shutdown\n" +
		"interface vlan 20\n    description Servers\n    ip address 10.0.20.1/24\n"
	if got := PortNames(cx); !reflect.DeepEqual(got, map[string]string{"1/1/1": "Core uplink to dc-sw01"}) {
		t.Fatalf("aos-cx: %v", got)
	}
}

func TestCollectFillsPortNames(t *testing.T) {
	brief := " 3     100/1000T  | No        Yes     Up     1000FDx    MDIX off  0\n" +
		" 4     100/1000T  | No        Yes     Down   1000FDx    Auto off  0\n"
	s := &Snapshot{Family: FamilyAOSS, Raw: map[string]string{"interfaces": brief, "config": "interface 3\n   name \"Printer\"\n   exit\n"}}
	parseInto(s)
	if len(s.Ports) != 2 || s.Ports[0].Description != "Printer" || s.Ports[1].Description != "" {
		t.Fatalf("ports: %+v", s.Ports)
	}
}
