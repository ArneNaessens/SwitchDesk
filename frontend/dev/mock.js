// Fake backend for previewing the UI in a plain browser (not shipped in the app).
(function () {
  const now = new Date().toISOString();
  const mins = (m) => new Date(Date.now() - m * 60000).toISOString();
  function ports(n, prefix, upPct, fiber) {
    const out = [];
    for (let i = 1; i <= n; i++) out.push({ name: prefix + i, up: Math.sin(i * 7.3) * 0.5 + 0.5 < upPct, enabled: i % 23 !== 0, type: "100/1000T", speed: "1000FDx" });
    for (let i = n + 1; i <= n + fiber; i++) out.push({ name: prefix + i, up: i !== n + fiber, enabled: true, type: "SFP+SR", speed: "10GigFD" });
    return out;
  }
  const sws = [
    { id: "core", name: "core-cx-01", host: "10.10.0.1", fam: "AOS-CX", model: "JL658A 6300M 24SFP+ 4SFP56", sw: "FL.10.13.1000", p: ports(24, "1/1/", 0.9, 4), role: "root" },
    { id: "agg1", name: "agg-cx-a", host: "10.10.0.2", fam: "AOS-CX", model: "JL678A 6100 24G CL4", sw: "PL.10.13.1000", p: ports(24, "1/1/", 0.6, 4), role: "member" },
    { id: "acc1", name: "acc-2930-f1", host: "10.10.1.11", fam: "AOS-S", model: "JL256A 2930F-48G-PoE+-4SFP+", sw: "WC.16.11.0015", p: ports(48, "", 0.7, 4), role: "member", issues: [["critical", "Security", "Telnet is enabled", "Credentials travel in clear text. Disable with: no telnet-server"], ["warning", "Config", "Unsaved configuration changes", "Running config differs from startup. Changes will be lost on reboot."]] },
    { id: "acc2", name: "acc-2930-f2", host: "10.10.1.12", fam: "AOS-S", model: "JL256A 2930F-48G-PoE+-4SFP+", sw: "WC.16.11.0015", p: ports(48, "", 0.5, 4), role: "member", issues: [["critical", "Hardware", "Fan problem", "Sys-2  | Fan Failed  | 1"], ["warning", "Spanning tree", "4 topology change(s) since last check", "Last change: 12 mins. Frequent changes usually mean a flapping link or an edge port without admin-edge."]] },
    { id: "acc3", name: "acc-2530-wh", host: "10.10.1.13", fam: "AOS-S", model: "J9773A 2530-24G-PoEP", sw: "YA.16.10.0021", p: ports(24, "", 0.4, 4), role: "disabled", issues: [["critical", "Spanning tree", "Spanning tree is disabled", "A single looped cable can take this segment down."], ["critical", "Security", "Default SNMP community in use", 'Community "public" or "private" is configured.']] },
    { id: "acc4", name: "acc-2930-f3", host: "10.10.1.14", offline: true },
    { id: "io1", name: "office-1930", host: "10.10.2.5", fam: "Instant On", model: "JL682A 1930 24G 4SFP+", sw: "1.0.7.0", p: ports(24, "", 0.55, 4), role: "member" },
  ];
  sws[1].p[25].blocking = true;
  const views = sws.map((s) => {
    const issues = (s.issues || []).map(([severity, category, title, detail]) => ({ switchId: s.id, switchName: s.name, severity, category, title, detail }));
    if (s.offline) issues.push({ switchId: s.id, switchName: s.name, severity: "critical", category: "Reachability", title: "Switch is offline", detail: "No answer on TCP 22 since Oct 23 09:41 (timeout)" });
    if (!s.offline) issues.push({ switchId: s.id, switchName: s.name, severity: "info", category: "Management", title: "No syslog server", detail: "Events are only kept in the switch's local buffer." });
    const order = { critical: 0, warning: 1, info: 2 };
    issues.sort((a, b) => order[a.severity] - order[b.severity]);
    return {
      id: s.id, name: s.name, host: s.host, port: 22, family: "auto", credentialId: "c1", site: "HQ",
      status: { online: !s.offline, checked: true, latencyMs: s.offline ? 0 : 2 + (s.host.length % 5), since: mins(s.offline ? 47 : 3000), collecting: false, lastCollect: mins(4) },
      summary: s.offline ? { family: "AOS-S", hostname: s.name, model: "JL256A 2930F-48G-PoE+-4SFP+", software: "WC.16.11.0015", ports: [], collectedAt: mins(300) } : { family: s.fam, hostname: s.name, model: s.model, software: s.sw, serial: "SG" + s.id.toUpperCase() + "123", uptime: "41 days", cpu: s.id === "acc2" ? 37 : 6, memory: 31, vlans: 6, portsUp: s.p.filter((x) => x.up).length, portsTotal: s.p.length, neighbors: 3, stpRole: s.role, collectedAt: mins(4), ports: s.p },
      issues, worst: issues[0]?.severity || "",
    };
  });
  const allIssues = views.flatMap((v) => v.issues);
  allIssues.push({ severity: "warning", category: "Spanning tree", switchName: "Network", title: "2 different spanning-tree roots", detail: "Expected one root per STP domain. Roots seen: 00:11:22:aa:bb:cc (core-cx-01, agg-cx-a, acc-2930-f1); 00:24:a8:10:00:01 (office-1930)." });
  const order = { critical: 0, warning: 1, info: 2 };
  allIssues.sort((a, b) => order[a.severity] - order[b.severity]);
  const dash = { total: 7, online: 6, offline: 1, unknown: 0, critical: allIssues.filter((i) => i.severity === "critical").length, warning: allIssues.filter((i) => i.severity === "warning").length, info: allIssues.filter((i) => i.severity === "info").length, collecting: 0, issues: allIssues, switches: views, dataDir: "/Users/jaba/Library/Application Support/SwitchDesk" };
  const vlans = [
    { id: 1, name: "DEFAULT_VLAN", untagged: Array.from({ length: 40 }, (_, i) => String(i + 1)), tagged: [] },
    { id: 10, name: "Management", ip: "10.10.1.11 255.255.255.0", untagged: [], tagged: ["49", "50"] },
    { id: 20, name: "Servers", untagged: ["41", "42", "43"], tagged: ["49", "50"] },
    { id: 30, name: "Voice", untagged: [], tagged: ["1", "2", "3", "4", "5", "6", "49", "50"] },
    { id: 40, name: "Guest-WiFi", untagged: ["44"], tagged: ["49", "50"] },
    { id: 99, name: "Printers", untagged: ["45", "46"], tagged: ["49"] },
  ];
  const cfg = `; JL256A Configuration Editor; Created on release #WC.16.11.0015
hostname "acc-2930-f1"
module 1 type jl256a
snmp-server community "netops" operator
sntp server priority 1 10.10.0.53
timesync sntp
aaa authentication ssh login radius local
radius-server host 10.10.0.60 key "********"
web-management ssl
spanning-tree
spanning-tree 41-46 admin-edge-port
vlan 1
   name "DEFAULT_VLAN"
   untagged 1-40
   exit
vlan 10
   name "Management"
   tagged 49-50
   ip address 10.10.1.11 255.255.255.0
   exit
vlan 20
   name "Servers"
   untagged 41-43
   tagged 49-50
   exit
password manager`;
  const snap = {
    family: "aos-s", collectedAt: mins(4), duration: "6.4s",
    facts: { hostname: "acc-2930-f1", model: "JL256A 2930F-48G-PoE+-4SFP+ Switch", serial: "SG81JP1234", baseMac: "001122-334455", software: "WC.16.11.0015", uptime: "41 days", cpu: 6, memory: 31, location: "HQ floor 1, rack A2", configSaved: false },
    stp: { known: true, enabled: true, mode: "MSTP-operation", bridgePriority: "32768", bridgeMac: "00:11:22:33:44:55", rootPriority: "4096", rootMac: "00:11:22:aa:bb:cc", rootPort: "49", isRoot: false, topologyChanges: 18, lastChange: "2 hours", ports: [{ port: "49", role: "Root", state: "Forwarding", cost: "2000" }, { port: "50", role: "Alternate", state: "Blocking", cost: "2000" }, { port: "1", role: "Designated", state: "Forwarding", cost: "20000" }] },
    security: { sshEnabled: true, sshPort: "22", sshVersion: "2", telnetEnabled: true, httpPlain: false, httpsEnabled: true, snmpCommunities: ["netops"], snmpDefault: false, ntpServers: ["10.10.0.53"], logServers: [], aaa: ["aaa authentication ssh login radius local"], managerPassword: true },
    vlans, ports: sws[2].p.map((p, i) => ({ ...p, description: i === 48 ? "uplink core" : "" })),
    neighbors: [
      { protocol: "lldp", localPort: "49", sysName: "core-cx-01", chassisId: "00:11:22:aa:bb:cc", portId: "1/1/3", mgmtIp: "10.10.0.1", capabilities: "bridge, router", description: "Aruba JL658A 6300M FL.10.13.1000" },
      { protocol: "lldp", localPort: "50", sysName: "agg-cx-a", portId: "1/1/25", mgmtIp: "10.10.0.2", capabilities: "bridge", description: "Aruba JL678A 6100" },
      { protocol: "cdp", localPort: "7", sysName: "SEP00AABBCCDDEE", portId: "Port 1", mgmtIp: "10.10.30.21", capabilities: "Host Phone", description: "Cisco IP Phone 8841" },
      { protocol: "lldp", localPort: "12", sysName: "ap-lobby", portId: "eth0", mgmtIp: "10.10.40.4", capabilities: "wlan-access-point", description: "Aruba AP-505" },
    ],
    env: [{ kind: "fan", name: "Sys-1", status: "Sys-1 | Fan OK | 0", ok: true }, { kind: "psu", name: "1", status: "1 JL085A Powered", ok: true }],
    logs: [{ severity: "W", text: "W 10/23/26 21:14:02 00077 ports: port 12 is now off-line" }, { severity: "M", text: "M 10/23/26 20:02:51 00435 ports: port 50 is Disabled by Loop Protect" }, { severity: "I", text: "I 10/23/26 19:55:10 00076 ports: port 12 is now on-line" }],
    raw: { system: "  System Name        : acc-2930-f1\n  Software revision  : WC.16.11.0015", stp: "...", config: cfg },
    runningConfig: cfg, errors: [],
  };
  const topoNodes = views.map((v) => ({ id: v.id, label: v.name, kind: "switch", managed: true, host: v.host, model: v.summary?.model, online: v.status.online, severity: v.worst, issues: v.issues.length, stpRoot: v.id === "core" }));
  topoNodes.push({ id: "ext:fw", label: "fw-edge-01", kind: "router", host: "10.10.0.254", model: "FortiGate-100F", online: true });
  topoNodes.push({ id: "ext:ap", label: "ap-lobby", kind: "ap", host: "10.10.40.4", online: true });
  topoNodes.push({ id: "ext:ap2", label: "ap-meeting", kind: "ap", host: "10.10.40.5", online: true });
  const E = (from, fp, to, tp, extra = {}) => ({ id: from + fp + to, from, to, fromPort: fp, toPort: tp, protocols: ["lldp"], up: true, ...extra });
  const topo = { nodes: topoNodes, edges: [E("core", "1/1/1", "agg1", "1/1/25"), E("core", "1/1/2", "agg1", "1/1/26"), E("core", "1/1/3", "acc1", "49"), E("agg1", "1/1/27", "acc1", "50", { blocking: true }), E("core", "1/1/4", "acc2", "49"), E("agg1", "1/1/28", "acc3", "25"), E("core", "1/1/5", "acc4", "49", { up: false }), E("agg1", "1/1/20", "io1", "25"), E("core", "1/1/24", "ext:fw", "port3"), E("acc1", "12", "ext:ap", "eth0"), E("acc2", "9", "ext:ap2", "eth0")] };
  const detail = (id) => ({ ...views.find((v) => v.id === id), snapshot: id === "acc4" ? null : snap, backups: [{ name: "20261023-210400.cfg", at: mins(4), size: 2400 }, { name: "20261019-081200.cfg", at: mins(6000), size: 2300 }] });
  window.go = { main: { App: {
    Platform: async () => "darwin", Version: async () => "0.1.0",
    GetDashboard: async () => dash, GetSwitch: async (id) => detail(id), GetTopology: async () => topo, SaveLayout: async () => {},
    GetSettings: async () => ({ pingInterval: 30, collectInterval: 15, parallel: 4, keepBackups: 20, cpuWarn: 80, memWarn: 90 }),
    Credentials: async () => [{ id: "c1", name: "Network admins", username: "admin" }],
    VLANOverview: async () => views.filter((v) => v.summary?.ports?.length).map((v, i) => ({ id: v.id, name: v.name, online: true, vlans: vlans.filter((x, j) => (i + j) % 4 !== 3).map((x) => (v.id === "io1" && x.id === 30 ? { ...x, name: "VOIP" } : x)) })),
    GetBackup: async () => cfg.replace("aaa authentication ssh login radius local\n", ""),
    PreviewVLAN: async (id, ch) => ["configure terminal", `vlan ${ch.id}`, ch.name ? `name "${ch.name}"` : null, ch.addUntagged?.length ? "untagged " + ch.addUntagged.join(",") : null, ch.addTagged?.length ? "tagged " + ch.addTagged.join(",") : null, "exit", "end", ch.save ? "write memory" : null].filter(Boolean),
    ApplyVLAN: async () => ({ ok: true, transcript: ["> configure terminal", "> vlan 50", "> exit"] }),
    RunCommand: async (id, c) => "  Status and Counters - VLAN Information\n\n  VLAN ID Name     | Status\n  1       DEFAULT  | Port-based",
    RefreshAll: async () => {}, Refresh: async () => {},
    DataFolder: async () => ({ path: "/Users/jaba/Library/Application Support/SwitchDesk", defaultPath: "/Users/jaba/Library/Application Support/SwitchDesk", custom: false }),
    PickDataFolder: async () => ({ path: "/Users/jaba/Dropbox/SwitchDesk", hasData: false, switches: 0, writable: true, same: false }),
    PickImportFile: async () => "/Users/jaba/Downloads/SwitchDesk-2026-10-23.sdbak",
    InspectImport: async () => ({ switches: 7, createdAt: new Date().toISOString(), history: true }),
    StartupNotice: async () => "", OpenDataFolder: async () => {},
  } } };
  window.runtime = { EventsOn: () => {} };
})();
