// Package localfake is a stand-in for an Instant On 1930 in local
// management mode, for tests. It copies the switch's sign-in exchange (RSA
// public key and token, encrypted credentials, session cookie) and serves XML
// sections shaped like the real ones.
package localfake

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
)

const Prefix = "/cs1a2b3c4d/hpe/"

type Switch struct {
	*httptest.Server
	User, Password string
	MaxSessions    int

	mu       sync.Mutex
	key      *rsa.PrivateKey
	token    string
	sessions map[string]bool
	Logins   int
	Logouts  int
	Sections map[string]string // section name -> inner XML
}

func New(user, pass string) *Switch {
	k, _ := rsa.GenerateKey(rand.Reader, 2048)
	s := &Switch{User: user, Password: pass, MaxSessions: 5, key: k, token: "5155a365", sessions: map[string]bool{}, Sections: Sections()}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	return s
}

func (s *Switch) Host() string { return strings.TrimPrefix(s.URL, "http://") }

func (s *Switch) Sessions() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sessions)
}

func (s *Switch) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Server", "GoAhead-Webs")
	p := r.URL.Path
	raw, _ := url.QueryUnescape(r.URL.RawQuery)
	switch {
	case p == "/":
		http.Redirect(w, r, Prefix+"config/login.htm", http.StatusFound)
	case p == Prefix+"config/login.htm":
		fmt.Fprint(w, "<html><title>Instant On</title></html>")
	case strings.HasSuffix(p, "/device/wcd") && raw == "{EncryptionSetting}":
		der := x509.MarshalPKCS1PublicKey(&s.key.PublicKey)
		pemKey := strings.Replace(string(pem.EncodeToMemory(&pem.Block{Type: "RSA PUBLIC KEY", Bytes: der})), "\n", "\n", -1)
		xmlOut(w, "<EncryptionSetting type=\"section\"><passwEncryptEnable>1</passwEncryptEnable><rsaPublicKey>"+pemKey+"</rsaPublicKey><loginToken>"+s.token+"</loginToken></EncryptionSetting>", "EncryptionSetting", "")
	case p == Prefix+"config/system.xml" && r.URL.Query().Get("action") == "login":
		s.login(w, r)
	case strings.EqualFold(p, "/System.xml") && r.URL.Query().Get("action") == "logout":
		if c, err := r.Cookie("session"); err == nil {
			s.mu.Lock()
			if s.sessions[c.Value] {
				delete(s.sessions, c.Value)
				s.Logouts++
			}
			s.mu.Unlock()
		}
		fmt.Fprint(w, "<?xml version='1.0' encoding='UTF-8'?><ResponseData><ActionStatus><statusCode>0</statusCode></ActionStatus></ResponseData>")
	case p == Prefix+"wcd":
		c, err := r.Cookie("session")
		s.mu.Lock()
		ok := err == nil && s.sessions[c.Value]
		s.mu.Unlock()
		if !ok {
			fmt.Fprint(w, "<?xml version='1.0' encoding='UTF-8'?>\n  <ResponseData>\n  <ActionStatus>\n <version>1.0</version>\n  <requestURL>"+Prefix+"wcd</requestURL>\n <statusCode>4</statusCode>\n <deviceStatusCode>0</deviceStatusCode>\n <statusString>Request Is not authenticated</statusString>\n  </ActionStatus>\n   </ResponseData>")
			return
		}
		name := strings.TrimSuffix(strings.TrimPrefix(raw, "{"), "}")
		if i := strings.IndexByte(name, '&'); i >= 0 {
			name = name[:i]
		}
		s.mu.Lock()
		body, found := s.Sections[name]
		s.mu.Unlock()
		if !found {
			xmlOut(w, "", name, "1")
			return
		}
		xmlOut(w, "<"+name+" type=\"section\">"+body+"</"+name+">", name, "")
	default:
		http.NotFound(w, r)
	}
}

func (s *Switch) login(w http.ResponseWriter, r *http.Request) {
	status := func(code string) {
		fmt.Fprintf(w, "<?xml version='1.0' encoding='UTF-8'?><ResponseData><ActionStatus><version>1.0</version><requestURL>system.xml</requestURL><statusCode>%s</statusCode><deviceStatusCode>0</deviceStatusCode><statusString></statusString></ActionStatus></ResponseData>", code)
	}
	ct, err := hex.DecodeString(r.URL.Query().Get("cred"))
	if err != nil {
		status("12")
		return
	}
	plain, err := rsa.DecryptPKCS1v15(nil, s.key, ct)
	if err != nil {
		status("12")
		return
	}
	// "user=...&password=...&ssd=true&token=...&", encodeURIComponent style
	f, _ := url.ParseQuery(strings.TrimSuffix(string(plain), "&"))
	s.mu.Lock()
	defer s.mu.Unlock()
	if f.Get("user") != s.User || f.Get("password") != s.Password || f.Get("token") != s.token || f.Get("ssd") != "true" {
		status("4")
		return
	}
	if len(s.sessions) >= s.MaxSessions {
		status("5")
		return
	}
	id := fmt.Sprintf("s%d", s.Logins+1)
	s.sessions[id] = true
	s.Logins++
	http.SetCookie(w, &http.Cookie{Name: "session", Value: id, Path: "/", HttpOnly: true})
	status("0")
}

func xmlOut(w http.ResponseWriter, section, name, code string) {
	fmt.Fprintf(w, "<?xml version=\"1.0\" encoding=\"UTF-8\" ?>\n<ResponseData>\n<DeviceConfiguration>\n<version>1.0</version>\n%s\n</DeviceConfiguration>\n<ActionStatus>\n<requestURL>%s</requestURL>\n<statusCode>%s</statusCode>\n</ActionStatus>\n</ResponseData>\n", section, name, code)
}

// Sections are modelled on a 1930 48-port PoE switch with spanning tree off,
// a phone and an AOS-CX uplink as LLDP neighbors, and a clock that stopped
// syncing in January.
func Sections() map[string]string {
	var ports, stp, poe, loop strings.Builder
	for i := 1; i <= 52; i++ {
		up := "2"
		if i == 2 || i == 10 || i == 48 || i == 7 {
			up = "1"
		}
		swType, trans, spd, poeOK := "3", "1", "1000", "1"
		if i > 48 {
			swType, trans, spd, poeOK = "6", "2", "10000", "2"
		}
		fmt.Fprintf(&ports, "<port><POESupported>%s</POESupported><ifIndex>%d</ifIndex><portName>%d</portName><row>1</row><column>1</column><relUnit>1</relUnit><transType>%s</transType><ifSpeed>%s</ifSpeed><swIfType>%s</swIfType><operStatus>%s</operStatus><adminStatus>1</adminStatus><suspStatus>2</suspStatus></port>", poeOK, i, i, trans, spd, swType, up)
		fmt.Fprintf(&stp, "<InterfaceEntry><interfaceID>%d</interfaceID><interfaceType>1</interfaceType><interfaceName>%d</interfaceName><STPEnabled>1</STPEnabled><portState>1</portState><portRole>1</portRole><pathCost>20000</pathCost></InterfaceEntry>", i, i)
		loopSt := "1"
		if i == 7 {
			loopSt = "3"
		}
		fmt.Fprintf(&loop, "<Entry><interfaceName>%d</interfaceName><interfaceType>1</interfaceType><interfaceID>%d</interfaceID><adminStatus>2</adminStatus><operStatus>%s</operStatus></Entry>", i, i, loopSt)
		if i <= 48 {
			power := "0"
			if i == 2 || i == 10 {
				power = "3200"
			}
			fmt.Fprintf(&poe, "<Interface><interfaceName>%d</interfaceName><interfaceType>1</interfaceType><interfaceID>%d</interfaceID><adminEnable>1</adminEnable><outputPower>%s</outputPower><powerLimit>30000</powerLimit></Interface>", i, i, power)
		}
	}
	for i := 1; i <= 16; i++ {
		fmt.Fprintf(&ports, "<port><POESupported>2</POESupported><ifIndex>%d</ifIndex><portName>TRK%d</portName><transType>1</transType><ifSpeed>0</ifSpeed><swIfType>12</swIfType><operStatus>6</operStatus><adminStatus>1</adminStatus><suspStatus>2</suspStatus></port>", 999+i, i)
	}
	return map[string]string{
		"SystemGlobalSetting":     "<systemName>SW-BRANCH-114</systemName><defaultSystemName></defaultSystemName><systemDescription>HPE Networking Instant On Switch 48p Gigabit CL4 PoE 4p SFP+ 370W 1930 JL686A, InstantOn_1930_3.3.2.0 (5)</systemDescription><systemLocation>Branch office</systemLocation><systemContact>Network team</systemContact><systemUpTime>617681500</systemUpTime><MACAddress>8c:85:c1:00:00:0e</MACAddress><isRunningConfigEqualToStartupConfig>2</isRunningConfigEqualToStartupConfig><firmwareVersion>InstantOn_1930_3.3.2.0 (5)</firmwareVersion><bootVersion>1.0.1.42</bootVersion><hardwareVersion>V03</hardwareVersion><serialNumber>CN0TEST14V</serialNumber>",
		"Ports":                   "<data><portsDataBase><rlStackUnitMode>2</rlStackUnitMode><numberOfPorts>52</numberOfPorts><inBandPortTable>" + ports.String() + "</inBandPortTable><outOfBandPortTable></outOfBandPortTable></portsDataBase></data>",
		"SpanningTreeGlobalParam": "<enabled>2</enabled><STPOperationMode>3</STPOperationMode><stpCountersClear>none</stpCountersClear>",
		"STP":                     "<GlobalSetting><BPDUHandlingMode>2</BPDUHandlingMode><BridgeSetting><bridgePriority>32768</bridgePriority><address>8c:85:c1:00:00:0e</address></BridgeSetting><DesignatedRoot><bridgeID>32768-8c:85:c1:00:00:0e</bridgeID><rootBridgeID>32768-8c:85:c1:00:00:0e</rootBridgeID><rootPortID>0</rootPortID><rootPathCost>0</rootPathCost><topologyChangeCount>0</topologyChangeCount></DesignatedRoot></GlobalSetting><InterfaceList>" + stp.String() + "</InterfaceList>",
		"LLDPMEDNeighborList": `<NeighborEntry><interfaceID>2</interfaceID><interfaceType>1</interfaceType><interfaceName>2</interfaceName><neighborID>54</neighborID><deviceIDSubtype>5</deviceIDSubtype><deviceID>192.168.13.121</deviceID><advertisedPortIDSubtype>3</advertisedPortIDSubtype><advertisedPortID>c0:74:ad:24:1f:36</advertisedPortID><portDescription>eth0</portDescription><systemName>GXP1625_c0:74:ad:24:1f:36</systemName><systemDescription>GXP1625 1.0.11.138</systemDescription><systemCapabilitiesSupported>0010010000000000</systemCapabilitiesSupported><systemCapabilitiesEnabled>0010010000000000</systemCapabilitiesEnabled><MngAddrList></MngAddrList></NeighborEntry>` +
			`<NeighborEntry><interfaceID>48</interfaceID><interfaceType>1</interfaceType><interfaceName>48</interfaceName><neighborID>64</neighborID><deviceIDSubtype>4</deviceIDSubtype><deviceID>ec:50:aa:71:87:40</deviceID><advertisedPortIDSubtype>5</advertisedPortIDSubtype><advertisedPortID>1/1/3</advertisedPortID><portDescription>1/1/3</portDescription><systemName>SW-BRANCH-CORE</systemName><systemDescription>Aruba R8N87A  PL.10.13.1080</systemDescription><systemCapabilitiesSupported>0010100000000000</systemCapabilitiesSupported><systemCapabilitiesEnabled>0010100000000000</systemCapabilitiesEnabled><MngAddrList> <AddrEntry> <addrSubtype>1</addrSubtype> <addr>192.168.13.2</addr> <addrIfSubtype>2</addrIfSubtype> <addrIfID>16777217</addrIfID> </AddrEntry> </MngAddrList></NeighborEntry>`,
		"VLANList":                     "<VLAN><VLANID>1</VLANID><VLANName></VLANName><authorizationType>1</authorizationType><VLANType>2</VLANType></VLAN><VLAN><VLANID>20</VLANID><VLANName>Voice</VLANName></VLAN>",
		"VLANInterfaceMembershipTable": "<Entry><VLANID>1</VLANID><VLANName></VLANName><adminState>1</adminState><operStatus>1</operStatus><taggedPorts></taggedPorts><untaggedPorts>1-48,49-52,TRK1-16</untaggedPorts></Entry><Entry><VLANID>20</VLANID><VLANName>Voice</VLANName><operStatus>1</operStatus><taggedPorts>2,10,48</taggedPorts><untaggedPorts></untaggedPorts></Entry>",
		"PoEPSEInterfaceList":          poe.String(),
		"PoEPSEUnitList":               "<UnitEntry><unitID>1</unitID><operStatus>1</operStatus><nominalPower>370</nominalPower><consumptionPower>8</consumptionPower><usageThreshold>95</usageThreshold></UnitEntry>",
		"DiagnosticsUnitList":          "<Entry><unitID>1</unitID><mainPSStatus>1</mainPSStatus><redundantPSStatus>5</redundantPSStatus><fan1Status>1</fan1Status><fan2Status>3</fan2Status><fan3Status>5</fan3Status><fan4Status>5</fan4Status><fan5Status>5</fan5Status><fan6Status>5</fan6Status><tempSensorValue>61</tempSensorValue><tempSensorStatus>1</tempSensorStatus><temperatureStatus>1</temperatureStatus></Entry>",
		"LoopbackDetectionList":        loop.String(),
		"SyslogServerList":             "<SyslogServer><index>1</index><addrType>1</addrType><IPAddr>10.0.0.195</IPAddr><portNo>514</portNo><severity>6</severity></SyslogServer>",
		"SNTPServerTable":              "<SNTPServerEntry><addrType>1</addrType><IPAddress>172.30.0.1</IPAddress><pollInterval>1024</pollInterval><stratum></stratum><lastResponse>01011900 00:00:00</lastResponse><status>4</status><lastRequest>11172025 15:31:47</lastRequest></SNTPServerEntry>",
		"TimeSetting":                  "<setTimeMode>2</setTimeMode><CurrentLocalTime><year>2026</year><month>01</month><day>28</day><hour>03</hour><minute>21</minute><second>30</second></CurrentLocalTime><TimeZone><offsetHour>04</offsetHour><offsetMinute>00</offsetMinute><timeZoneID>J</timeZoneID></TimeZone>",
		"SNMPGlobalSetting":            "<SNMPEnbl>2</SNMPEnbl><trapsEnbl>1</trapsEnbl>",
		"CommunityList":                "",
		"MemoryLogTable":               "<LogEntry><logNo>2147482724</logNo><dateTime>01282026 03:05:45</dateTime><severityType>6</severityType><message>%AAA-I-CONNECT: New http connection for user admin</message></LogEntry><LogEntry><logNo>2147482725</logNo><dateTime>01282026 03:06:01</dateTime><severityType>4</severityType><message>%LINK-W-Down:  11</message></LogEntry><LogEntry><logNo>2147482726</logNo><dateTime>01282026 03:07:10</dateTime><severityType>3</severityType><message>%STP-E-LOOP: loop detected on 7</message></LogEntry>",
	}
}
