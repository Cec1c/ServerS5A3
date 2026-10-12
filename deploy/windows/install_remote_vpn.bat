@echo off
setlocal
cd /d "%~dp0..\.."
if not exist "runtime\remote\90cn.conf" (
  echo Missing private tunnel configuration: runtime\remote\90cn.conf
  exit /b 1
)
if not exist "%ProgramFiles%\WireGuard\wireguard.exe" (
  if not exist ".cache\remote\wireguard-amd64-1.1.1.msi" exit /b 1
  msiexec.exe /i "%CD%\.cache\remote\wireguard-amd64-1.1.1.msi" /qn /norestart /l*v "%CD%\runtime\remote\wireguard-install.log"
  if not exist "%ProgramFiles%\WireGuard\wireguard.exe" exit /b 1
)
sc.exe query "WireGuardTunnel$90cn" >nul 2>&1
if errorlevel 1 (
  "%ProgramFiles%\WireGuard\wireguard.exe" /installtunnelservice "%CD%\runtime\remote\90cn.conf"
  if errorlevel 1 exit /b 1
)
sc.exe start "WireGuardTunnel$90cn" >nul 2>&1
sc.exe query "WireGuardTunnel$90cn" > "%CD%\runtime\remote\vpn-install-result.txt"
exit /b 0
