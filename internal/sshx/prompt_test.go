package sshx

import "testing"

// Issue #10: hostnames with characters such as "+" never matched the prompt,
// so collection waited 20 s and gave up.
func TestPromptHostnames(t *testing.T) {
	ok := map[string]string{
		"SW-7191-HalleD+Anlage40-70-39#":     "SW-7191-HalleD+Anlage40-70-39",
		"SW-TEST# ":                          "SW-TEST",
		"SW-TEST>":                           "SW-TEST",
		"SW-TEST(config)#":                   "SW-TEST",
		"core-sw01(vlan-20)# ":               "core-sw01",
		"Lager&Versand_2#":                   "Lager&Versand_2",
		"Halle 3 / Büro#":                    "Halle 3 / Büro",
		"sw@site:rack4#":                     "sw@site:rack4",
		"HP-2530-48G-PoEP#":                  "HP-2530-48G-PoEP",
		"Aruba-JL256A-2930F-48G-PoE+-4SFP+#": "Aruba-JL256A-2930F-48G-PoE+-4SFP+",
	}
	for line, host := range ok {
		m := promptRe.FindStringSubmatch(line)
		if m == nil || m[1] != host {
			t.Errorf("%q: got %v, want hostname %q", line, m, host)
		}
	}
	for _, line := range []string{
		"",
		"#",
		"########",
		" leading-space#",
		"<sw01>",
		"Port  Status  Speed",
		"Press any key to continue",
	} {
		if promptRe.MatchString(line) {
			t.Errorf("%q should not look like a prompt", line)
		}
	}
}

// Once the hostname is known only that switch's prompt is removed from output,
// so a last output line that merely ends in "#" is kept.
func TestTrimEchoKeepsOtherLinesEndingInHash(t *testing.T) {
	s := &Session{Hostname: "SW-7191-HalleD+Anlage40-70-39"}
	out := s.trimEcho("show run\nhostname \"x\"\n; comment line #\nSW-7191-HalleD+Anlage40-70-39# ", "show run")
	if out != "hostname \"x\"\n; comment line #" {
		t.Fatalf("got %q", out)
	}
	out = s.trimEcho("show x\nvalue\nother-switch#", "show x")
	if out != "value\nother-switch#" {
		t.Fatalf("got %q", out)
	}
	if !s.isPrompt("SW-7191-HalleD+Anlage40#") { // AOS-S shortens long names in the prompt
		t.Fatal("shortened prompt not recognised")
	}
}
