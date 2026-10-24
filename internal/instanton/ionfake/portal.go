// Package ionfake is a stand-in for the Instant On portal, for tests. It
// implements the same sign-in steps (password check, authorization code with
// PKCE, token, refresh) and the read endpoints SwitchDesk uses.
package ionfake

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
)

type Portal struct {
	*httptest.Server
	Email, Password string

	mu         sync.Mutex
	Sites      []map[string]any
	Inventory  map[string][]map[string]any // site id -> devices
	Alerts     map[string][]map[string]any
	MFA        bool // the account has two-step verification
	ExpireNext bool // reject the next API call's token, as if it expired
	Logins     int
	Refreshes  int
	APICalls   map[string]int

	codes  map[string]string // code -> challenge
	access map[string]bool
	ref    map[string]bool
	n      int
}

func New(email, password string) *Portal {
	p := &Portal{Email: email, Password: password, Inventory: map[string][]map[string]any{}, Alerts: map[string][]map[string]any{},
		APICalls: map[string]int{}, codes: map[string]string{}, access: map[string]bool{}, ref: map[string]bool{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/settings.json", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"restApiUrl": p.URL, "ssoFqdn": p.URL, "ssoRedirectUrl": p.URL + "/", "ssoClientIdAuthZ": "client-1",
		})
	})
	mux.HandleFunc("/aio/api/v1/mfa/validate/full", p.validate)
	mux.HandleFunc("/as/authorization.oauth2", p.authorize)
	mux.HandleFunc("/as/token.oauth2", p.token)
	mux.HandleFunc("/api/", p.api)
	p.Server = httptest.NewServer(mux)
	return p
}

func (p *Portal) next(prefix string) string {
	p.n++
	return fmt.Sprintf("%s-%d", prefix, p.n)
}

func (p *Portal) validate(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	p.mu.Lock()
	defer p.mu.Unlock()
	if r.Form.Get("username") != p.Email || r.Form.Get("password") != p.Password {
		w.WriteHeader(401)
		return
	}
	if p.MFA {
		json.NewEncoder(w).Encode(map[string]any{"status": "MFA_REQUIRED"})
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"access_token": "session-ok"})
}

func (p *Portal) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("sessionToken") != "session-ok" || q.Get("client_id") != "client-1" || q.Get("code_challenge_method") != "S256" {
		w.WriteHeader(400)
		return
	}
	p.mu.Lock()
	code := p.next("code")
	p.codes[code] = q.Get("code_challenge")
	p.mu.Unlock()
	http.Redirect(w, r, q.Get("redirect_uri")+"?code="+code+"&state="+url.QueryEscape(q.Get("state")), http.StatusFound)
}

func (p *Portal) token(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	p.mu.Lock()
	defer p.mu.Unlock()
	switch r.Form.Get("grant_type") {
	case "authorization_code":
		ch, ok := p.codes[r.Form.Get("code")]
		sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		if !ok || base64.RawURLEncoding.EncodeToString(sum[:]) != ch {
			w.WriteHeader(400)
			return
		}
		delete(p.codes, r.Form.Get("code"))
		p.Logins++
	case "refresh_token":
		if !p.ref[r.Form.Get("refresh_token")] {
			w.WriteHeader(400)
			return
		}
		delete(p.ref, r.Form.Get("refresh_token"))
		p.Refreshes++
	default:
		w.WriteHeader(400)
		return
	}
	a, rt := p.next("access"), p.next("refresh")
	p.access[a], p.ref[rt] = true, true
	json.NewEncoder(w).Encode(map[string]any{"access_token": a, "refresh_token": rt, "expires_in": 1800})
}

func (p *Portal) api(w http.ResponseWriter, r *http.Request) {
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ExpireNext {
		p.ExpireNext = false
		delete(p.access, tok)
	}
	if !p.access[tok] || r.Header.Get("x-ion-api-version") == "" {
		w.WriteHeader(401)
		return
	}
	path := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/sites"), "/")
	p.APICalls[r.URL.Path]++
	list := func(v []map[string]any) {
		if v == nil {
			v = []map[string]any{}
		}
		json.NewEncoder(w).Encode(map[string]any{"kind": "resourceList", "totalCount": len(v), "elements": v})
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	switch {
	case path == "":
		list(p.Sites)
	case len(parts) == 2 && parts[1] == "inventory":
		list(p.Inventory[parts[0]])
	case len(parts) == 2 && parts[1] == "alerts":
		list(p.Alerts[parts[0]])
	default:
		w.WriteHeader(404)
	}
}

// SetStatus changes a device's status ("up" or "down").
func (p *Portal) SetStatus(site, id, status string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, d := range p.Inventory[site] {
		if d["id"] == id {
			d["status"] = status
		}
	}
}

func (p *Portal) Calls(path string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.APICalls[path]
}

// Switch returns a switch record shaped like the portal's inventory entries.
func Switch(id, name, ip, model string, ports ...map[string]any) map[string]any {
	ps := make([]any, len(ports))
	for i, x := range ports {
		ps[i] = x
	}
	return map[string]any{
		"id": id, "kind": "deviceSummary", "deviceType": "switch", "name": name, "defaultName": "Device 1",
		"status": "up", "operationalState": "active", "health": "good", "ipAddress": ip, "macAddress": id,
		"serialNumber": "SG00TEST01", "model": model, "sku": model, "uptimeInSeconds": 3301365,
		"deviceSoftwareVersion": "3.4.1.0", "currentFirmwareVersion": "3.4.0.0-6", "isUpToDate": true,
		"wiredClientsCount": 3, "poePseNominalPowerInWatts": 195, "poePseConsumedPowerInWatts": 32,
		"stpBridgePriority": 32768, "isStpRootBridge": false, "ethernetPorts": ps, "activeAlerts": []any{},
	}
}

// Port returns a port record shaped like the portal's.
func Port(n int, up bool, extra map[string]any) map[string]any {
	p := map[string]any{
		"kind": "ethernetPort", "portNumber": n, "faceplatePortNumber": n, "isLinkUp": up, "speed": "mbps1000",
		"isSfp": false, "isUplink": false, "userDeactivated": false, "powerProvidedInMilliwatts": 0, "isLoopDetected": false,
		"directlyConnectedDeviceId": nil, "directlyConnectedDeviceName": nil,
	}
	for k, v := range extra {
		p[k] = v
	}
	return p
}
