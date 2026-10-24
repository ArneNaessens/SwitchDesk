# Instant On switches

Instant On switches managed in the Instant On app or portal have no SSH or Telnet. SwitchDesk reads them from the Instant On portal instead, using your Instant On account. Once added, they show up on the dashboard, in the switch list and on the network map like any other switch.

## Add them

1. Click **Add switch** and pick **Instant On cloud** at the top of the dialog.
2. Enter the email and password of your Instant On account, or pick one you saved earlier.
3. Click **Find switches**. SwitchDesk signs in and lists the switches on every site the account can see. Switches you already added are greyed out.
4. Untick the ones you don't want and click **Add**.

!!! warning "Two-step verification"
    The portal only lets SwitchDesk sign in with an account that has two-step verification turned off. Create a separate Instant On account for SwitchDesk, invite it to your sites with view-only access, and leave two-step verification on for your own account.

The password is stored encrypted in your data folder, like SSH passwords. It moves with an export to another computer.

## What you see

| | |
|---|---|
| Online / offline | From the portal, checked at every ping interval |
| Model, serial, software, uptime | Switch page, overview tab |
| Ports | Link, speed, uplink, PoE draw per port, and loops (shown as red LEDs) |
| Neighbors | Which device sits on each port, as the portal reports it. These feed the network map, so Instant On switches link to each other and to their uplink. |
| Spanning tree | Bridge priority and whether the switch is the root |
| Instant On panel | Site, health, firmware status, wired clients, PoE budget used |

### Problems raised

- **Switch is offline** when the portal reports it down
- **Loop detected** with the ports involved
- **Portal alerts** about the switch, with the portal's severity: major is critical, minor is a warning
- **Firmware update available** when the portal says the switch isn't up to date
- **PoE budget nearly used** at 90% of the budget
- **Instant On login failed** when the account's password changed or the portal refuses it

## What it can't do

Configuration, VLAN changes, the command console and firmware updates need SSH, so they aren't available for these switches. Make changes in the Instant On app or portal.

## How it works

SwitchDesk signs in the same way the portal's own web page does, then reads each site's device list. One list per site is fetched at every ping interval and shared by all the switches on that site, so 20 switches on one site still cost one request.

The portal's API isn't documented by HPE and can change. If the portal changes in a way SwitchDesk doesn't understand, the switches show **Couldn't read the Instant On portal** with the reason, and nothing else is affected.
