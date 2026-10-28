# Troubleshooting

## A switch shows as offline

SwitchDesk calls a switch online when its SSH port answers. Check that you can reach it from this computer:

```sh
ssh admin@10.0.1.11
```

If that works but SwitchDesk says offline, look at the port in **Edit** (it defaults to 22) and at any firewall between you and the switch.

## SSH login failed

The credentials are wrong or were changed. Fix them in **Settings → Credentials** or pick another set in **Edit**, then **Collect now**.

Some AOS-S switches use keyboard-interactive login; SwitchDesk handles that too.

## SSH host key changed

SwitchDesk remembers each switch's SSH host key the first time it connects. If a switch later shows a different key, SwitchDesk stops talking to it, because that's what a man-in-the-middle would look like.

The usual innocent reasons are a replaced switch, a factory reset, or a regenerated key (`crypto key generate`). If that's what happened, open the switch and click **Trust new key**.

The same goes for HTTPS. SwitchDesk remembers the certificate an AOS-CX switch shows on its first firmware upload over HTTPS, and the one an Instant On switch shows when it's first read through its web interface. A new certificate (after a reset, a renewal or a replacement) stops the upload or the reading with "certificate changed". An Instant On switch that stops offering encrypted login is treated the same way. **Trust new key** clears all of these, and the next connection learns them again.

## Older switches won't connect

Older ProCurve and AOS-S firmware only speaks old SSH algorithms (`diffie-hellman-group14-sha1`, `aes-cbc`, `ssh-rsa`). SwitchDesk still accepts them so these switches keep working, but updating the switch firmware is the better fix.

## "No CLI prompt within 20s"

SwitchDesk signed in but didn't recognise the switch's prompt, so it waited and gave up. The message shows the last line the switch sent. If that line is the switch's prompt (its hostname followed by `#` or `>`), please [open an issue](https://github.com/jaba0x/SwitchDesk/issues) and paste it.

Before 0.9.8 this happened to any switch whose hostname had a character other than letters, digits, `-`, `_` and `.`, such as `+`, `&`, `/`, a space or an umlaut. Updating fixes that.

## The switch was read, but some tabs are empty

Different firmware versions word their output differently. Open **Raw output**, find the command for that tab, and check what the switch returned. If it says `Invalid input`, the command doesn't exist on that version. If the data is there but the tab is empty, please [open an issue](https://github.com/jaba0x/SwitchDesk/issues) with the raw output (with names and addresses masked); it makes a perfect test case.

## A switch is missing from the map

The map comes from LLDP and CDP. Check **Neighbors** on the switch and on what it's plugged into. On AOS-S, turn LLDP on with `lldp run`.

## Telnet or plain HTTP is flagged, but it's turned off

SwitchDesk doesn't go by the config alone: on every reading it tries the switch's TCP port 23 (Telnet) and port 80 (plain HTTP) itself. The switch page shows what it saw under **Management access**, for example *port 23 refused*. A warning only appears when the port really answers.

If you just turned it off, click **Collect now** to read the switch again instead of waiting for the next reading.

## Firmware update problems

**"The switch never asked this computer for the image over TFTP".** The switch can't reach this computer on UDP port 69. Check your computer's firewall (allow SwitchDesk), and any ACL or firewall between the switch's management network and you. SwitchDesk gives up after two minutes without a request, and stops the copy on the switch.

**"The switch's REST API didn't answer on HTTPS" (AOS-CX).** This computer can't reach the switch on TCP 443. Allow HTTPS from your computer to the switch's management address. SwitchDesk then falls back to TFTP.

**"The switch's REST API refused the login" (AOS-CX).** The SSH account needs administrator rights for uploads. On releases before 10.08, also check that `https-server rest access-mode read-write` isn't blocked by your AAA setup.

**"UDP port 69 is taken".** Another TFTP server runs on this computer. Stop it while updating.

**AOS-CX copy fails.** SwitchDesk uses the `mgmt` VRF when the switch is managed through its management port, and `default` otherwise. Check that the switch can reach your computer from that VRF (`ping <your-ip> vrf mgmt`).

**The switch came back on the old version.** Check the job log in **Update jobs** and the firmware card on the switch page. The boot default is shown as *next boot*.

## SwitchDesk shows a warning about the data folder at startup

The custom data folder wasn't available (an unplugged disk or an unmounted share), so SwitchDesk opened the default folder. Reconnect it and restart, or choose another folder in **Settings → Data**.

## Still stuck?

Ask in [Discussions](https://github.com/jaba0x/SwitchDesk/discussions) or [open an issue](https://github.com/jaba0x/SwitchDesk/issues).

## Instant On switches

**"the switch didn't accept that username and password" (local management).** Use the login you use on the switch's own web page.

**"too many web sessions open".** The switch allows a few web sessions at a time. Close its web page in other browsers, or wait ten minutes for old sessions to time out. SwitchDesk signs out after every reading.

**Test connection says port 443 didn't answer.** HTTPS is turned off on the switch. Set the port to 80, or turn HTTPS back on in the switch's web page.


**"Instant On login failed".** The portal didn't accept the email and password. Check them by signing in at portal.instant-on.hpe.com. If the account uses two-step verification, SwitchDesk can't sign in with it: use a separate account without it, with view-only access to your sites, and update it under **Settings → Credentials**.

**"Couldn't read the Instant On portal".** The portal didn't answer, or answered in a way SwitchDesk didn't expect. The message gives the reason. The portal's API isn't documented and can change; if this persists, open an issue with the message.

**A switch shows "no longer in the site's inventory".** It was removed from the site in Instant On, or moved to another site. Remove it from SwitchDesk and add it again from the right site.

