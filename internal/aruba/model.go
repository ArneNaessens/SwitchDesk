package aruba

import "time"

type Family string

const (
	FamilyAuto      Family = "auto"
	FamilyAOSS      Family = "aos-s"
	FamilyAOSCX     Family = "aos-cx"
	FamilyInstantOn Family = "instant-on"
)

func (f Family) Label() string {
	switch f {
	case FamilyAOSS:
		return "AOS-S"
	case FamilyAOSCX:
		return "AOS-CX"
	case FamilyInstantOn:
		return "Instant On"
	}
	return "Unknown"
}

type Facts struct {
	Hostname    string `json:"hostname"`
	Model       string `json:"model"`
	Serial      string `json:"serial"`
	BaseMAC     string `json:"baseMac"`
	Software    string `json:"software"`
	Uptime      string `json:"uptime"`
	CPU         int    `json:"cpu"`    // percent, -1 unknown
	Memory      int    `json:"memory"` // percent used, -1 unknown
	Location    string `json:"location"`
	Contact     string `json:"contact"`
	ConfigSaved *bool  `json:"configSaved,omitempty"`
}

type STPPort struct {
	Port  string `json:"port"`
	Role  string `json:"role"`
	State string `json:"state"`
	Cost  string `json:"cost"`
}

type STP struct {
	Enabled         bool      `json:"enabled"`
	Known           bool      `json:"known"`
	Mode            string    `json:"mode"`
	BridgePriority  string    `json:"bridgePriority"`
	BridgeMAC       string    `json:"bridgeMac"`
	RootPriority    string    `json:"rootPriority"`
	RootMAC         string    `json:"rootMac"`
	RootPort        string    `json:"rootPort"`
	IsRoot          bool      `json:"isRoot"`
	TopologyChanges int       `json:"topologyChanges"`
	LastChange      string    `json:"lastChange"`
	Ports           []STPPort `json:"ports"`
}

type Security struct {
	SSHEnabled      bool     `json:"sshEnabled"`
	SSHVersion      string   `json:"sshVersion"`
	SSHPort         string   `json:"sshPort"`
	TelnetEnabled   bool     `json:"telnetEnabled"`
	HTTPPlain       bool     `json:"httpPlain"`
	HTTPSEnabled    bool     `json:"httpsEnabled"`
	SNMPCommunities []string `json:"snmpCommunities"`
	SNMPDefault     bool     `json:"snmpDefault"`
	NTPServers      []string `json:"ntpServers"`
	LogServers      []string `json:"logServers"`
	AAA             []string `json:"aaa"`
	ManagerPassword bool     `json:"managerPassword"`
	// What SwitchDesk saw when it tried the ports itself: "open", "closed",
	// "filtered" or "" when not checked. These override the config guess.
	TelnetPort string `json:"telnetPort"`
	HTTPPort   string `json:"httpPort"`
}

type VLAN struct {
	ID       int      `json:"id"`
	Name     string   `json:"name"`
	Status   string   `json:"status"`
	Tagged   []string `json:"tagged"`
	Untagged []string `json:"untagged"`
	IP       string   `json:"ip"`
}

type Port struct {
	Name        string `json:"name"`
	Enabled     bool   `json:"enabled"`
	Up          bool   `json:"up"`
	Speed       string `json:"speed"`
	Mode        string `json:"mode"`
	VLAN        string `json:"vlan"`
	Description string `json:"description"`
	Type        string `json:"type"`
}

type Neighbor struct {
	Protocol     string `json:"protocol"` // lldp or cdp
	LocalPort    string `json:"localPort"`
	SysName      string `json:"sysName"`
	ChassisID    string `json:"chassisId"`
	PortID       string `json:"portId"`
	PortDescr    string `json:"portDescr"`
	MgmtIP       string `json:"mgmtIp"`
	Description  string `json:"description"`
	Capabilities string `json:"capabilities"`
}

type EnvItem struct {
	Kind   string `json:"kind"` // fan, psu, temp
	Name   string `json:"name"`
	Status string `json:"status"`
	OK     bool   `json:"ok"`
}

type LogLine struct {
	Severity string `json:"severity"` // I W M E D
	Text     string `json:"text"`
}

// Snapshot is everything collected from one switch in one SSH pass.
type Snapshot struct {
	Family        Family            `json:"family"`
	Facts         Facts             `json:"facts"`
	STP           STP               `json:"stp"`
	Security      Security          `json:"security"`
	VLANs         []VLAN            `json:"vlans"`
	Ports         []Port            `json:"ports"`
	Neighbors     []Neighbor        `json:"neighbors"`
	Env           []EnvItem         `json:"env"`
	Logs          []LogLine         `json:"logs"`
	Raw           map[string]string `json:"raw"`
	RunningConfig string            `json:"runningConfig"`
	CollectedAt   time.Time         `json:"collectedAt"`
	Duration      string            `json:"duration"`
	Errors        []string          `json:"errors"`
	Banks         Banks             `json:"banks"`
}
