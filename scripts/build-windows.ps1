# Builds build\bin\SwitchDesk.exe. Needs Go 1.23+ (winget install GoLang.Go).
# WebView2 is already part of Windows 10/11.
param([string]$Version = "0.1.0")
$ErrorActionPreference = "Stop"
Set-Location (Join-Path $PSScriptRoot "..")

if (-not (Get-Command go -ErrorAction SilentlyContinue)) { throw "Go is not installed. Run: winget install GoLang.Go" }
$env:PATH += ";" + (Join-Path (go env GOPATH) "bin")
if (-not (Get-Command wails -ErrorAction SilentlyContinue)) {
  Write-Host "Installing the Wails CLI..."
  go install github.com/wailsapp/wails/v2/cmd/wails@v2.10.2
}

go mod tidy
go test ./internal/...
wails build -clean -platform windows/amd64 -ldflags "-X main.Version=$Version"
Write-Host "Built build\bin\SwitchDesk.exe"
