# Instant On switches

Instant On 1830, 1930 and 1960 switches have no SSH CLI. SwitchDesk reads them in one of two ways, depending on how you manage them:

- **Local management** (you open the switch's own web page): SwitchDesk signs in to that web page with the switch's admin login. See [Local management](#local-management).
- **Cloud management** (you use the Instant On app or portal): SwitchDesk reads them from the Instant On portal with your Instant On account. See [Cloud management](#cloud-management).

Either way they show up on the dashboard, in the switch list and on the network map like any other switch.

## Local management

1. Click **Add switch** and pick **Instant On (local)** at the top, or **Instant On (web interface)** as the switch type.
2. Enter the switch's address and the login you use on its web page.
3. Leave the port on **443** (HTTPS). If the switch only answers on HTTP, use **80**. **Test connection** tries the other port too and tells you which one works.

To add many at once, use **Add many** with switch type **Instant On (web interface)**.

If you added Instant On switches over SSH in an earlier version, their page offers **Use web interface** to switch them over, one at a time or all at once.

SwitchDesk signs in the way the web page does: the switch hands out a public key and a one-time token, and the login is encrypted with it. It signs out after every reading, because the switch allows only a few web sessions at a time.

### What you see

| | |
|---|---|
| System | Model, serial, firmware, uptime, location, contact, and whether the running config is saved |
| Ports | Link, speed, SFP+, PoE draw per port, VLAN, and loops (red LEDs) |
| Spanning tree | On or off, mode, priority, root, topology changes, port roles and states |
| Neighbors | LLDP neighbors with their management address, so the switch joins the network map |
| VLANs | Every VLAN with its tagged and untagged ports |
| Hardware | Fans, power supplies and temperature |
| Logs | The switch's recent log |

### Problems raised

Everything SwitchDesk checks on other switches where the data exists (spanning tree off, root by default priority, unsaved config, fans, power, no syslog), plus:

- **Clock is off** when the switch's clock is more than five minutes out, with the reason when SwitchDesk can tell (for example, the SNTP server never answered)
- **SNTP server never answered**
- **No loop protection** when spanning tree and loop protection are both off
- **Loop detected** on a port

VLAN changes, the command console and firmware updates aren't available for these switches. Make changes on the switch's web page.

## Cloud management

### Add them

1. Click **Add switch** and pick **Instant On cloud** at the top of the dialog.
2. Enter the email and password of your Instant On account, or pick one you saved earlier.
3. Click **Find switches**. SwitchDesk signs in and lists the switches on every site the account can see. Switches you already added are greyed out.
4. Untick the ones you don't want and click **Add**.

!!! warning "Two-step verification"
    The portal only lets SwitchDesk sign in with an account that has two-step verification turned off. Create a separate Instant On account for SwitchDesk, invite it to your sites with view-only access, and leave two-step verification on for your own account.

The password is stored encrypted in your data folder, like SSH passwords. It moves with an export to another computer.

### What you see

| | |
|---|---|
| Online / offline | From the portal, checked at every ping interval |
| Model, serial, software, uptime | Switch page, overview tab |
| Ports | Link, speed, uplink, PoE draw per port, and loops (shown as red LEDs) |
| Neighbors | Which device sits on each port, as the portal reports it. These feed the network map, so Instant On switches link to each other and to their uplink. |
| Spanning tree | Bridge priority and whether the switch is the root |
| Instant On panel | Site, health, firmware status, wired clients, PoE budget used |

#### Problems raised

- **Switch is offline** when the portal reports it down
- **Loop detected** with the ports involved
- **Portal alerts** about the switch, with the portal's severity: major is critical, minor is a warning
- **Firmware update available** when the portal says the switch isn't up to date
- **PoE budget nearly used** at 90% of the budget
- **Instant On login failed** when the account's password changed or the portal refuses it

### What it can't do

Configuration, VLAN changes, the command console and firmware updates need SSH, so they aren't available for these switches. Make changes in the Instant On app or portal.

### How it works

SwitchDesk signs in the same way the portal's own web page does, then reads each site's device list. One list per site is fetched at every ping interval and shared by all the switches on that site, so 20 switches on one site still cost one request.

The portal's API isn't documented by HPE and can change. If the portal changes in a way SwitchDesk doesn't understand, the switches show **Couldn't read the Instant On portal** with the reason, and nothing else is affected.
