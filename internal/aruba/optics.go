package aruba

import (
	"math"
	"regexp"
	"strconv"
	"strings"
)

// Optic is one transceiver with its digital optical monitoring (DOM) values.
// Power is in dBm. Multi-lane optics (QSFP) have one Rx/Tx value per lane.
type Optic struct {
	Port       string    `json:"port"`
	Type       string    `json:"type"`
	Model      string    `json:"model,omitempty"`
	Serial     string    `json:"serial,omitempty"`
	Wavelength string    `json:"wavelength,omitempty"`
	DOM        bool      `json:"dom"`
	TempC      *float64  `json:"tempC,omitempty"`
	Voltage    *float64  `json:"voltage,omitempty"`
	BiasmA     *float64  `json:"biasMa,omitempty"`
	Rx         []float64 `json:"rx,omitempty"`
	Tx         []float64 `json:"tx,omitempty"`
	// thresholds the transceiver reports, in dBm (AOS-CX only)
	RxLowAlarm  *float64 `json:"rxLowAlarm,omitempty"`
	RxLowWarn   *float64 `json:"rxLowWarn,omitempty"`
	RxHighWarn  *float64 `json:"rxHighWarn,omitempty"`
	RxHighAlarm *float64 `json:"rxHighAlarm,omitempty"`
	TxLowAlarm  *float64 `json:"txLowAlarm,omitempty"`
	TxLowWarn   *float64 `json:"txLowWarn,omitempty"`
	// Alarms are the alarm and warning flags that are on now (AOS-CX), or
	// the recent alarms (AOS-S), e.g. "Rx power low alarm".
	Alarms []string `json:"alarms,omitempty"`
	Recent bool     `json:"recent,omitempty"` // Alarms are AOS-S "recent alarms", not necessarily active
}

// NoLight is what a reading of 0 mW is shown as.
const NoLight = -40.0

var (
	optHeadRe  = regexp.MustCompile(`^\s*Transceiver in (\S+)`)
	dbmRe      = regexp.MustCompile(`(-?\d+(?:\.\d+)?)\s*dBm`)
	mwRe       = regexp.MustCompile(`(-?\d+(?:\.\d+)?)\s*mW`)
	numRe      = regexp.MustCompile(`-?\d+(?:\.\d+)?`)
	channelRe  = regexp.MustCompile(`^\s*(?:Channel|Lane)\s*#?\d+\s*:?\s*$`)
	flagLineRe = regexp.MustCompile(`(?i)^(temperature|voltage|tx bias|rx power|tx power) (high|low) (alarm|warning)$`)
)

// power reads "0.54mW", "0.2650mW, -5.768dBm" or "-2.6dBm" as dBm.
func power(v string) (float64, bool) {
	if m := dbmRe.FindStringSubmatch(v); m != nil {
		f, err := strconv.ParseFloat(m[1], 64)
		return f, err == nil
	}
	if m := mwRe.FindStringSubmatch(v); m != nil {
		f, err := strconv.ParseFloat(m[1], 64)
		if err != nil {
			return 0, false
		}
		if f <= 0 {
			return NoLight, true
		}
		return math.Round(10*math.Log10(f)*100) / 100, true
	}
	return 0, false
}

func number(v string) (*float64, bool) {
	m := numRe.FindString(v)
	if m == "" {
		return nil, false
	}
	f, err := strconv.ParseFloat(m, 64)
	if err != nil {
		return nil, false
	}
	return &f, true
}

// ParseOptics reads AOS-S "show interfaces transceiver detail" and AOS-CX
// "show interface dom detail" (or "show interface transceiver detail").
func ParseOptics(text string) []Optic {
	var out []Optic
	var cur *Optic
	inAlarms := false
	flush := func() {
		if cur != nil {
			out = append(out, *cur)
		}
		cur = nil
	}
	for _, raw := range strings.Split(strings.ReplaceAll(text, "\r", ""), "\n") {
		if m := optHeadRe.FindStringSubmatch(raw); m != nil {
			flush()
			cur = &Optic{Port: m[1]}
			inAlarms = false
			continue
		}
		if cur == nil {
			continue
		}
		line := strings.TrimSpace(raw)
		if line == "" || channelRe.MatchString(line) {
			continue
		}
		low := strings.ToLower(line)
		if strings.HasPrefix(low, "recent alarms") {
			inAlarms = true
			continue
		}
		if strings.HasPrefix(low, "recent errors") || strings.HasPrefix(low, "time stamp") || strings.HasPrefix(low, "status") {
			inAlarms = false
			continue
		}
		if strings.Contains(low, "no dom information") {
			cur.DOM = false
			continue
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			if inAlarms {
				cur.Alarms, cur.Recent = append(cur.Alarms, line), true
			}
			continue
		}
		inAlarms = false
		key := strings.ToLower(strings.Join(strings.Fields(k), " "))
		v = strings.TrimSpace(v)
		switch {
		case key == "type":
			cur.Type = v
		case key == "model" || key == "part number" || key == "product number":
			cur.Model = v
		case key == "serial number" || key == "serial":
			cur.Serial = v
		case key == "wavelength":
			cur.Wavelength = v
		case key == "diagnostic support":
			cur.DOM = strings.Contains(strings.ToUpper(v), "DOM")
		case key == "temperature":
			if f, ok := number(v); ok {
				cur.TempC, cur.DOM = f, true
			}
		case key == "voltage":
			if f, ok := number(v); ok {
				cur.Voltage = f
			}
		case key == "tx bias":
			if f, ok := number(v); ok && cur.BiasmA == nil {
				cur.BiasmA = f
			}
		case key == "rx power":
			if f, ok := power(v); ok {
				cur.Rx, cur.DOM = append(cur.Rx, f), true
			}
		case key == "tx power":
			if f, ok := power(v); ok {
				cur.Tx = append(cur.Tx, f)
			}
		case strings.HasSuffix(key, " threshold"):
			f, ok := power(v)
			if !ok {
				continue
			}
			set := func(p **float64) {
				if *p == nil {
					*p = &f
				}
			}
			switch strings.TrimSuffix(key, " threshold") {
			case "rx power low alarm":
				set(&cur.RxLowAlarm)
			case "rx power low warning":
				set(&cur.RxLowWarn)
			case "rx power high warning":
				set(&cur.RxHighWarn)
			case "rx power high alarm":
				set(&cur.RxHighAlarm)
			case "tx power low alarm":
				set(&cur.TxLowAlarm)
			case "tx power low warning":
				set(&cur.TxLowWarn)
			}
		case flagLineRe.MatchString(key):
			if strings.EqualFold(v, "on") {
				name := strings.ToUpper(key[:1]) + key[1:]
				if !contains(cur.Alarms, name) {
					cur.Alarms = append(cur.Alarms, name)
				}
			}
		}
	}
	flush()
	return out
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// WorstRx is the lowest receive power across lanes.
func (o Optic) WorstRx() (float64, bool) {
	if len(o.Rx) == 0 {
		return 0, false
	}
	w := o.Rx[0]
	for _, r := range o.Rx[1:] {
		if r < w {
			w = r
		}
	}
	return w, true
}

// Judge says how a transceiver is doing: "alarm", "warning" or "" (fine or
// unknown), with a short reason. Thresholds come from the transceiver when it
// reports them, otherwise from its alarm flags.
func (o Optic) Judge() (level, reason string) {
	rx, hasRx := o.WorstRx()
	switch {
	case hasRx && o.RxLowAlarm != nil && rx <= *o.RxLowAlarm:
		return "alarm", "receive power below the alarm level"
	case hasRx && o.RxHighAlarm != nil && rx >= *o.RxHighAlarm:
		return "alarm", "receive power above the alarm level"
	}
	for _, a := range o.Alarms {
		if strings.Contains(strings.ToLower(a), "alarm") && !o.Recent {
			return "alarm", strings.ToLower(a[:1]) + a[1:]
		}
	}
	switch {
	case hasRx && o.RxLowWarn != nil && rx <= *o.RxLowWarn:
		return "warning", "receive power below the warning level"
	case hasRx && o.RxHighWarn != nil && rx >= *o.RxHighWarn:
		return "warning", "receive power above the warning level"
	}
	for _, a := range o.Alarms {
		l := strings.ToLower(a)
		if strings.Contains(l, "warning") || (o.Recent && strings.Contains(l, "alarm")) {
			if o.Recent {
				return "warning", "recent " + l
			}
			return "warning", l
		}
	}
	return "", ""
}
