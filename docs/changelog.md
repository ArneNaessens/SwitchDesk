# Changelog

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
