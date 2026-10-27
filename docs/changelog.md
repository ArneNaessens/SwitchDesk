# Changelog

## 0.9.2

Security fixes. Please update.

- **HTTPS certificates are pinned.** AOS-CX firmware uploads and Instant On (web interface) sign-ins used to accept any certificate. Now the first certificate a switch shows is remembered, the same way SSH host keys are. If it changes later, SwitchDesk stops and asks before sending the password. **Trust new key** on the switch page clears it.
- **Instant On login can't be downgraded.** Once a switch has offered encrypted login, a later request for the password in plain text is refused.
- **Test connection keeps the host key pin.** On an existing switch it now checks the saved SSH host key, so it no longer sends the password to a switch with a different key.
- **Safer imports.** Switch and credential IDs in an imported `.sdbak` are checked and replaced when they aren't in the form SwitchDesk uses. A crafted export could otherwise reach files outside the data folder or put script into the window. In merge mode, an imported credential is matched to one of yours only when the username and password both match. Imported settings are kept within the normal ranges.
- **Firmware file names are checked everywhere.** Files copied into the firmware folder by hand get a safe name if theirs has spaces, quotes, line breaks or other unusual characters. Before, such a name could add commands to the switch's `copy` command. Every value in that command is now checked too.
- The Instant On session path and redirects must stay on the switch itself, and the Download update button only opens this project's release pages.

## 0.9.1

- Pages use the full width of the window. The switch list no longer scrolls sideways on wide screens.
- Instant On 1930 (web interface): a reading no longer stops with "the web session has ended" when the switch answers a page it doesn't have, such as PoE on the 8-port JL680A. SwitchDesk checks the session is still alive and carries on. PoE isn't read from models without PoE.
- Instant On: the sign-in also works when the switch sets its session cookie without a path.
- When a session really does end, the message now says which page was refused and what the switch said.

## 0.9.0

- New **Status check** setting under Settings → Monitoring: **SSH port** (what earlier versions did), **Ping (ICMP)** or **Off**. The SSH port check opens a connection to every switch every 30 seconds, and switches log each one as an SSH connection. Ping avoids that; Off stops background checks completely, and a switch's status then comes from its reads. Old settings keep the SSH port check.

## 0.8.3

- The window opens maximised.
- The dashboard's problems list has a quick filter: type to match the problem, switch name or details (press `/` to jump there, Esc to clear), and pick a category. It works together with the severity chips and shows how many of the problems match.

## 0.8.2

- Delete and Remove buttons work in the Mac app. They asked for confirmation with a browser dialog that the app's window doesn't show, so the answer was always "cancel". Confirmation now uses SwitchDesk's own dialog, for switches, firmware images, credentials and replacing data on import.

## 0.8.1

- **Instant On (web interface)** is back in the switch type list, in both Add switch and Edit switch. Picking it switches the dialog to the web interface: port 443 and the switch's web login. Editing an Instant On switch that was added for SSH opens it this way too, so saving moves it to the web interface.

## 0.8.0

- Optical levels for SFP, SFP+ and QSFP transceivers on AOS-S and AOS-CX: Rx and Tx power in dBm (per lane on QSFP), temperature, model and serial, in a new **Optics** table on the Ports tab and in the port LED tooltips.
- New problems: **Optical power alarm** and **Optical power warning**. On AOS-CX they use the levels the transceiver itself reports. AOS-S doesn't report levels, so its recent optical alarms are shown as a warning. Unused SFPs in ports that are down are ignored.

## 0.7.0

- Instant On switches in local management mode can be added. They have no SSH, so SwitchDesk signs in to the switch's web interface with its admin login, the same encrypted login the web page uses, and signs out after each reading. In **Add switch**, pick **Instant On (local)**; in **Add many**, pick **Instant On (web interface)**.
- They show model, firmware, ports with PoE and loops, spanning tree, LLDP neighbors for the map, VLANs, fans, power, temperature and logs.
- New problems for them: a clock that's out (with the SNTP server that never answered), and no loop protection when spanning tree is off too.
- Instant On switches added over SSH earlier get a **Use web interface** button on their page.
- AOS-S: "Unsaved changes" no longer shows when the switch says its running configuration hasn't changed since the last save.

## 0.6.0

- Instant On switches managed from the cloud can be added. They have no SSH, so SwitchDesk reads them from the Instant On portal with your Instant On account. In **Add switch**, pick **Instant On cloud**, sign in, and choose switches from every site the account can see.
- They show online/offline status, ports with PoE draw and loops, portal alerts, firmware status and PoE budget. The devices on their ports put them on the network map.
- Instant On accounts are kept with your other credentials, encrypted, and move with an export.
- The map merges a link reported from both ends even when one end doesn't name its port.

## 0.5.3

- The network map no longer draws links between branches that are only connected through routers. A neighbor is now matched to a switch by its chassis MAC first. A matching IP address or name only counts if the MAC agrees, or if the other switch reports the link back. Branches that reuse the same private subnets used to get false links this way.
- "Different spanning-tree roots" is only raised for switches joined by layer 2 links. Routed branches each have their own root, and that's no longer flagged.

## 0.5.2

- AOS-CX firmware upload over HTTPS works with switches whose self-signed certificate has a negative serial number, such as the 6100. Before, the upload failed with "x509: negative serial number" and fell back to TFTP.
- The job log lists exactly which web server settings were changed for the upload.

## 0.5.1

- Firmware updates on AOS-CX upload the image over HTTPS to the switch's REST API, so the switch no longer has to reach your computer over TFTP. The web server is turned on in the management VRF for the upload if it was off, and turned off again afterwards.
- Firmware updates on AOS-CX no longer hang at "Continue (y/n)?". AOS-CX needs Enter after the answer; SwitchDesk now presses it, and switches that act on the single key (AOS-S) still get only the key.
- A TFTP copy that the switch never starts, or that stops moving, now fails after two minutes with the reason, instead of waiting until the switch logs the session out.
- Telnet and plain HTTP are checked by trying TCP ports 23 and 80 directly. A config that couldn't be read, or an Instant On switch, no longer shows a Telnet warning that isn't true. The switch page shows what was seen on each port.

## 0.5.0

- Firmware updates on AOS-S upload the image over SSH (SFTP or SCP), so they work over a VPN or zero trust access where the switch can't reach your computer. `ip ssh filetransfer` is turned on for the upload and the previous settings are restored afterwards.
- TFTP stays as the fallback and for AOS-CX. A failed TFTP copy now says when the switch couldn't reach your computer.

## 0.4.0

- A yellow bar tells you when a newer SwitchDesk is on GitHub; **Settings → Check for updates** checks on demand.
- About section and author credit.

## 0.3.0

- Firmware updates: library of images (add files or download links), available updates per switch, primary and secondary banks, one-click update with reboot or copy only, update jobs with logs.
- Automatic firmware updates: copy to the spare bank, or install inside a maintenance window.
- Built-in TFTP server for switches to download images from.

## 0.2.0

- Switch list: checkboxes with bulk collect, disable, enable and delete; per-row buttons; search by name, address, model or state.
- Disabled switches: paused without losing their data.
- Choose where the data folder lives; move or copy it.
- Export and import between computers with a passphrase-protected `.sdbak` file.

## 0.1.0

- First release: dashboard with problems and port faceplates, switch pages (system, spanning tree, ports, VLANs, neighbors, config, logs, console, raw output), VLAN editing with review, VLAN matrix, LLDP/CDP network map, config copies with compare.
- AOS-CX, AOS-S and Instant On over SSH.
