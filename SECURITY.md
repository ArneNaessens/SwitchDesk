# Security

## Reporting a vulnerability

Please don't open a public issue or discussion for a security problem.

Report it privately through GitHub instead: open the [Security tab](https://github.com/jaba0x/SwitchDesk/security) and click **Report a vulnerability**. Only you and I can see the report, and we can talk it through there. If that doesn't work for you, email jaba@jaba.ge.

Tell me the version you tested, what an attacker could do, and how to reproduce it. A proof of concept helps a lot.

I'll reply within a few days. Once a fix is out, the release notes credit you by name unless you'd rather stay anonymous.

## Supported versions

Fixes go into the latest release. SwitchDesk tells you in the app when a newer version is out, so please update before reporting.

## Scope

SwitchDesk runs on your own computer and talks to your switches over SSH, HTTPS and, during some firmware updates, TFTP. These are in scope:

- how credentials are stored and encrypted, in the data folder and in `.sdbak` exports
- SSH host key checking and the handling of switch output
- the firmware library, downloads and the built-in TFTP server
- the Instant On portal and web interface sign-in
- the update check against GitHub releases

Bugs in the switches' own firmware belong with HPE Aruba Networking.
