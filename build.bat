@echo off
setlocal EnableExtensions DisableDelayedExpansion
if not defined GO_EXE set "GO_EXE=go"
if "%~1"=="" (set "GOOS=windows") else (set "GOOS=%~1")
if "%~2"=="" (set "GOARCH=amd64") else (set "GOARCH=%~2")
if not "%~3"=="" exit /b 2
if not "%GOOS%"=="windows" if not "%GOOS%"=="linux" if not "%GOOS%"=="darwin" exit /b 2
if not "%GOARCH%"=="amd64" if not "%GOARCH%"=="arm64" exit /b 2
set "CGO_ENABLED=0"
set "DNF90_EXT="
if "%GOOS%"=="windows" set "DNF90_EXT=.exe"
set "DNF90_OUTPUT=%~dp0dist\%GOOS%-%GOARCH%"
if not exist "%DNF90_OUTPUT%" mkdir "%DNF90_OUTPUT%"
if errorlevel 1 exit /b 1
pushd "%~dp0go-server"
if errorlevel 1 exit /b 1
"%GO_EXE%" build -buildvcs=false -mod=readonly -trimpath -o "%DNF90_OUTPUT%\DNF90Server%DNF90_EXT%" ./cmd/server/dnf90
if errorlevel 1 goto failed
"%GO_EXE%" build -buildvcs=false -mod=readonly -trimpath -o "%DNF90_OUTPUT%\DNF90Control%DNF90_EXT%" ./cmd/server/control
if errorlevel 1 goto failed
"%GO_EXE%" build -buildvcs=false -mod=readonly -trimpath -o "%DNF90_OUTPUT%\DNF90Doctor%DNF90_EXT%" ./cmd/server/doctor
if errorlevel 1 goto failed
popd
echo Build succeeded: "%DNF90_OUTPUT%"
exit /b 0
:failed
popd
echo Build failed.
exit /b 1
