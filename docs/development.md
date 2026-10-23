# Development

SwitchDesk is written in Go with [Wails](https://wails.io) for the desktop window. The interface is plain HTML, CSS and JavaScript with no build step.

## Run it

```sh
git clone https://github.com/jaba0x/SwitchDesk.git
cd SwitchDesk
go install github.com/wailsapp/wails/v2/cmd/wails@v2.10.2
wails dev
```

To work on the interface in a normal browser with sample data, open `frontend/dev/preview.html`.

## Tests

```sh
go test ./internal/...
```

The tests cover:

- the output parsers, against sample output from AOS-S and AOS-CX,
- SSH sessions, against a fake switch with escape codes, "Press any key" and paging,
- a full firmware update against a fake switch that really downloads the image over SwitchDesk's TFTP server, reboots and comes back on the new version,
- the TFTP server, including transfers longer than 65,535 blocks,
- export and import between two data folders with different keys.

## Layout

```text
main.go, app.go        window and the methods the interface calls
data.go                data folder, export and import
firmware.go            firmware library and update jobs
update.go              checks GitHub for new releases
internal/sshx          SSH shell sessions: prompts, paging, escape codes, legacy ciphers
internal/aruba         commands per family, output parsers, VLAN changes, firmware banks
internal/core          polling, problem rules, network map, update jobs
internal/store         inventory, encrypted credentials, snapshots, config copies, export
internal/firmware      image library, version matching, TFTP server
frontend/dist          the interface
docs/                  this documentation (MkDocs)
```

## Documentation

These pages are built with [MkDocs](https://www.mkdocs.org) and the Material theme, and published to GitHub Pages on every push to `main` that touches `docs/` or `mkdocs.yml`.

```sh
pip install mkdocs-material
mkdocs serve
```

## Releases

The **build** workflow builds the Mac app and the Windows exe and publishes them as a GitHub release. Push a tag like `v0.5.0`, or run the workflow from the Actions tab and enter a version.
