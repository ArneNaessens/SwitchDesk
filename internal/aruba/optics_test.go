package aruba

import (
	"strings"
	"testing"
)

// AOS-S "show interfaces transceiver detail", layout from the AOS-S
// management guide: a healthy 1000SX and a 10GbE-LR that lost its light.
const aossOptics = `
 Transceiver in 21
  Interface index    : 21
  Type               : 1000SX
  Model              : J4858C
  Connector type     : LC
  Wavelength         : 850nm
  Transfer distance  : 300m (50um), 150m (62.5um),
  Diagnostic support : DOM
  Serial number      : MY050VM9WB

 Status
  Temperature : 50.111C
  Voltage     : 3.1234V
  TX Bias     : 6mA
  TX Power    : 0.2650mW, -5.768dBm
  RX Power    : 0.3892mW, -4.098dBm

 Time stamp   :  Mon Mar 7 14:22:13 2011

 Transceiver in 23
  Interface Index   : 24
  Type              : 10GbE-LR
  Model             : J8437A
  Connector type    : SC
  Wavelength        : Channel #0: 1310nm, #1:0nm, #2:0nm, #3:0nm
  Transfer distance : 10000m (SM)
  Diagnostic support: DOM
  Serial number     : ED456SS987

 Status
  Temperature : 32.754C
  TX Bias     : 42.700mA
  TX Power    : 0.5875mW, -2.309dBm
  RX Power    : 0.0040mW, -23.979dBm

 Recent Alarms:

  Rx power low alarm
  Rx power low warning

 Recent errors:
  Receive optical power fault

 Time stamp : Mon Mar 7 16:26:06 2013
`

// AOS-CX "show interface dom detail": an SR with thresholds, a DAC without
// DOM, a weak LR with its low warning on, and a 4-lane QSFP.
const cxOptics = `Transceiver in 1/1/34
 Type: SFP+SR
  Temperature: 23.60C
  Temperature high alarm: Off
  Temperature high alarm threshold: 75.00C
  Voltage: 3.25V
  Tx Bias: 5.64mA
  Rx Power: 0.54mW
  Rx Power high alarm: Off
  Rx Power low alarm: Off
  Rx Power high warning: Off
  Rx Power low warning: Off
  Rx Power high alarm threshold: 1.00mW
  Rx Power low alarm threshold: 0.05mW
  Rx Power high warning threshold: 0.79mW
  Rx Power low warning threshold: 0.10mW
  Tx Power: 0.59mW
  Tx Power low alarm threshold: 0.15mW

Transceiver in 1/1/48
 Type: SFP28DAC0.65
 % No DOM information available

Transceiver in 1/1/49
 Type: SFP+LR
  Temperature: 30.10C
  Rx Power: 0.08mW
  Rx Power low alarm: Off
  Rx Power low warning: On
  Rx Power low alarm threshold: 0.03mW
  Rx Power low warning threshold: 0.09mW
  Tx Power: 0.50mW

Transceiver in 1/1/56
 Type: QSFP+LR4
  Temperature: 28.00C
  Voltage: 3.25V

 Channel 1:
  Tx Bias: 33.10mA
  Rx Power: 0.48mW
  Rx Power low warning threshold: 0.02mW
  Tx Power: 1.26mW

 Channel 2:
  Tx Bias: 34.44mA
  Rx Power: 0.55mW
  Tx Power: 1.21mW

 Channel 3:
  Rx Power: 0.00mW
  Tx Power: 1.18mW

 Channel 4:
  Rx Power: 0.40mW
  Tx Power: 1.24mW
`

func TestParseOpticsAOSS(t *testing.T) {
	o := ParseOptics(aossOptics)
	if len(o) != 2 {
		t.Fatalf("got %d optics", len(o))
	}
	a, b := o[0], o[1]
	if a.Port != "21" || a.Type != "1000SX" || a.Model != "J4858C" || a.Serial != "MY050VM9WB" || !a.DOM || len(a.Rx) != 1 || a.Rx[0] != -4.098 || a.Tx[0] != -5.768 || *a.TempC != 50.111 {
		t.Fatalf("21: %+v", a)
	}
	if lvl, _ := a.Judge(); lvl != "" {
		t.Fatalf("healthy optic judged %s", lvl)
	}
	if b.Port != "23" || b.Rx[0] != -23.979 || len(b.Alarms) != 2 || !b.Recent {
		t.Fatalf("23: %+v", b)
	}
	// AOS-S only reports recent alarms, so they're a warning, not an alarm
	if lvl, why := b.Judge(); lvl != "warning" || !strings.Contains(why, "recent rx power low alarm") {
		t.Fatalf("23 judged %s: %s", lvl, why)
	}
}

func TestParseOpticsCX(t *testing.T) {
	o := ParseOptics(cxOptics)
	if len(o) != 4 {
		t.Fatalf("got %d optics", len(o))
	}
	sr, dac, lr, q := o[0], o[1], o[2], o[3]
	if sr.Port != "1/1/34" || sr.Rx[0] != -2.68 || sr.Tx[0] != -2.29 || *sr.RxLowWarn != -10 || *sr.RxLowAlarm != -13.01 || *sr.RxHighAlarm != 0 {
		t.Fatalf("sr: %+v rxlw=%v", sr, *sr.RxLowWarn)
	}
	if lvl, _ := sr.Judge(); lvl != "" {
		t.Fatalf("sr judged %s", lvl)
	}
	if dac.DOM || len(dac.Rx) != 0 {
		t.Fatalf("dac: %+v", dac)
	}
	if lvl, why := lr.Judge(); lvl != "warning" || !strings.Contains(why, "below the warning level") {
		t.Fatalf("lr judged %s: %s (%+v)", lvl, why, lr)
	}
	if len(q.Rx) != 4 || len(q.Tx) != 4 || q.Rx[2] != NoLight {
		t.Fatalf("qsfp lanes: %+v", q)
	}
	if w, _ := q.WorstRx(); w != NoLight {
		t.Fatalf("worst lane %v", w)
	}
	if lvl, _ := q.Judge(); lvl != "warning" { // dark lane 3 is below the lane warning level
		t.Fatalf("qsfp judged %s", lvl)
	}
}
