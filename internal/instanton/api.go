package instanton

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

type Site struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Health string `json:"health"`
	Status string `json:"status"`
	Role   string `json:"userRoleOnSite"`
}

type Alert struct {
	ID         string         `json:"id"`
	Type       string         `json:"type"`
	Severity   string         `json:"severity"` // major, minor, info
	RaisedTime float64        `json:"raisedTime"`
	Cleared    float64        `json:"clearedTime"`
	Props      map[string]any `json:"alertTypeProperties"`
}

type Port struct {
	PortNumber        int    `json:"portNumber"`
	Faceplate         any    `json:"faceplatePortNumber"`
	Name              string `json:"name"`
	IsLinkUp          bool   `json:"isLinkUp"`
	Speed             string `json:"speed"`
	MaxSpeed          string `json:"maxSpeed"`
	Duplex            string `json:"duplex"`
	IsSfp             bool   `json:"isSfp"`
	IsUplink          *bool  `json:"isUplink"`
	UserDeactivated   bool   `json:"userDeactivated"`
	PoEmW             *int   `json:"powerProvidedInMilliwatts"`
	IsLoopDetected    bool   `json:"isLoopDetected"`
	IsBpduGuard       *bool  `json:"isBpduGuardDetected"`
	IsFlapping        *bool  `json:"isLinkFlappingDetected"`
	Health            string `json:"health"`
	PeerID            string `json:"directlyConnectedDeviceId"`
	PeerName          string `json:"directlyConnectedDeviceName"`
	PeerModel         string `json:"directlyConnectedDeviceModel"`
	UplinkDeviceID    string `json:"uplinkDeviceDeviceId"`
	UplinkDeviceName  string `json:"uplinkDeviceName"`
	UplinkDeviceIP    string `json:"uplinkDeviceIpAddress"`
	TrunkNumber       *int   `json:"trunkNumber"`
	SpanningTreeGuard string `json:"spanningTreeProtection"`
}

// Label is the port number printed on the switch.
func (p Port) Label() string {
	switch v := p.Faceplate.(type) {
	case float64:
		if v > 0 {
			return fmt.Sprint(int(v))
		}
	case string:
		if v != "" {
			return v
		}
	}
	return fmt.Sprint(p.PortNumber)
}

type Device struct {
	ID               string   `json:"id"` // the MAC address
	DeviceType       string   `json:"deviceType"`
	Name             string   `json:"name"`
	DefaultName      string   `json:"defaultName"`
	Status           string   `json:"status"`           // up, down
	OperationalState string   `json:"operationalState"` // active, ...
	Health           string   `json:"health"`
	HealthConditions []any    `json:"healthConditions"`
	IPAddress        string   `json:"ipAddress"`
	MACAddress       string   `json:"macAddress"`
	SerialNumber     string   `json:"serialNumber"`
	Model            string   `json:"model"`
	SKU              string   `json:"sku"`
	Uptime           int64    `json:"uptimeInSeconds"`
	Software         string   `json:"deviceSoftwareVersion"`
	Firmware         string   `json:"currentFirmwareVersion"`
	IsUpToDate       *bool    `json:"isUpToDate"`
	WiredClients     int      `json:"wiredClientsCount"`
	Ports            []Port   `json:"ethernetPorts"`
	ActiveAlerts     []Alert  `json:"activeAlerts"`
	PoEBudgetW       *float64 `json:"poePseNominalPowerInWatts"`
	PoEUsedW         *float64 `json:"poePseConsumedPowerInWatts"`
	UplinkDeviceID   string   `json:"uplinkDeviceDeviceId"`
	UplinkDeviceName string   `json:"uplinkDeviceName"`
	UplinkDeviceIP   string   `json:"uplinkDeviceIpAddress"`
	StpPriority      *int     `json:"stpBridgePriority"`
	IsStpRoot        *bool    `json:"isStpRootBridge"`
	HasExpectedLoop  *bool    `json:"hasExpectedLoop"`
	StackName        string   `json:"stackName"`
	Location         string   `json:"location"`
}

// DisplayName is what the portal shows for the device.
func (d Device) DisplayName() string {
	if d.Name != "" {
		return d.Name
	}
	return d.DefaultName
}

// IsSwitch tells switches from access points and gateways.
func (d Device) IsSwitch() bool {
	if d.DeviceType != "" {
		return d.DeviceType == "switch"
	}
	m := strings.ToUpper(d.Model)
	return strings.HasPrefix(m, "JL") || strings.HasPrefix(m, "R8") || strings.HasPrefix(m, "S0")
}

func (d Device) Online() bool { return d.Status == "up" }

type list[T any] struct {
	Elements []T `json:"elements"`
}

// Sites lists the sites the account can see.
func (c *Client) Sites(ctx context.Context) ([]Site, error) {
	var l list[Site]
	if err := c.get(ctx, "/api/sites/", &l); err != nil {
		return nil, err
	}
	return l.Elements, nil
}

// Inventory lists every device on a site, access points included.
func (c *Client) Inventory(ctx context.Context, siteID string) ([]Device, error) {
	var l list[Device]
	if err := c.get(ctx, "/api/sites/"+url.PathEscape(siteID)+"/inventory", &l); err != nil {
		return nil, err
	}
	return l.Elements, nil
}

// Alerts lists the site's alerts, cleared ones included.
func (c *Client) Alerts(ctx context.Context, siteID string) ([]Alert, error) {
	var l list[Alert]
	if err := c.get(ctx, "/api/sites/"+url.PathEscape(siteID)+"/alerts", &l); err != nil {
		return nil, err
	}
	return l.Elements, nil
}
