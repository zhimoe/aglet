param(
    [ValidateSet("amd64", "arm64")]
    [string]$Arch = "amd64",
    [string]$OutDir = "dist",
    [switch]$Strip
)

$ErrorActionPreference = "Stop"
$Root = Split-Path -Parent $PSScriptRoot
Set-Location $Root

if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
    throw "go was not found in PATH. Install Go 1.26.10 or later."
}
if (-not (Test-Path "vendor/modules.txt")) {
    throw "vendor/modules.txt not found. Run: go mod vendor"
}

$env:CGO_ENABLED = "0"
$env:GOOS = "windows"
$env:GOARCH = $Arch

New-Item -ItemType Directory -Force -Path $OutDir | Out-Null
$Output = Join-Path $OutDir "aglet-windows-$Arch.exe"

$BuildArgs = @("build", "-mod=vendor", "-trimpath")
if ($Strip) {
    $BuildArgs += @("-ldflags", "-s -w")
}
$BuildArgs += @("-o", $Output, "./cmd/aglet")

Write-Host "Building $Output (GOOS=windows GOARCH=$Arch CGO_ENABLED=0)"
& go @BuildArgs
if ($LASTEXITCODE -ne 0) {
    throw "go build failed with exit code $LASTEXITCODE"
}

Write-Host "Built $Output"
