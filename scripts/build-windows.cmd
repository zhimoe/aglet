@echo off
setlocal
cd /d "%~dp0.."

where go >nul 2>nul
if errorlevel 1 (
  echo go was not found in PATH. Install Go 1.26.10 or later.
  exit /b 1
)
if not exist vendor\modules.txt (
  echo vendor\modules.txt not found. Run: go mod vendor
  exit /b 1
)

set CGO_ENABLED=0
set GOOS=windows
set GOARCH=amd64
if not exist dist mkdir dist

go build -mod=vendor -trimpath -o dist\aglet-windows-amd64.exe .\cmd\aglet
if errorlevel 1 exit /b 1

echo Built dist\aglet-windows-amd64.exe
