@echo off
setlocal EnableExtensions

set "ROOT_DIR=%~dp0"
if "%ROOT_DIR:~-1%"=="\" set "ROOT_DIR=%ROOT_DIR:~0,-1%"

set "FRONTEND_DIR=%ROOT_DIR%\frontend"
set "GO_EXE=%ROOT_DIR%\.tools\go\bin\go.exe"
set "LOCAL_ENV_FILE=%ROOT_DIR%\deploy_router.local.cmd"
set "PACKAGE_TEMPLATE_DIR=%ROOT_DIR%\packaging\router"

if not defined ROUTER_GOOS set "ROUTER_GOOS=linux"
if not defined ROUTER_GOARCH set "ROUTER_GOARCH=arm64"
if not defined ROUTER_BINARY_NAME set "ROUTER_BINARY_NAME=vpn-manager"
if not defined ROUTER_HTTP_PORT set "ROUTER_HTTP_PORT=18080"
if not defined ROUTER_PACKAGE_DIR set "ROUTER_PACKAGE_DIR=%ROOT_DIR%\build\router"
if not defined ROUTER_DATA_DIR_NAME set "ROUTER_DATA_DIR_NAME=data"
if not defined ROUTER_OPENVPN_BIN set "ROUTER_OPENVPN_BIN="
if not defined ROUTER_SINGBOX_BIN set "ROUTER_SINGBOX_BIN="
if not defined ROUTER_VERSION set "ROUTER_VERSION="
if not defined ROUTER_COMMIT set "ROUTER_COMMIT="
if not defined ROUTER_ARCHIVE_NAME set "ROUTER_ARCHIVE_NAME=vpn-manager-%ROUTER_GOOS%-%ROUTER_GOARCH%.tar.gz"

if exist "%LOCAL_ENV_FILE%" (
    call "%LOCAL_ENV_FILE%"
)

if not defined ROUTER_VERSION (
    where git.exe >nul 2>nul
    if not errorlevel 1 (
        for /f "usebackq delims=" %%v in (`git.exe -C "%ROOT_DIR%" describe --tags --always --dirty 2^>nul`) do set "ROUTER_VERSION=%%v"
    )
)

if not defined ROUTER_COMMIT (
    where git.exe >nul 2>nul
    if not errorlevel 1 (
        for /f "usebackq delims=" %%v in (`git.exe -C "%ROOT_DIR%" rev-parse --short HEAD 2^>nul`) do set "ROUTER_COMMIT=%%v"
    )
)

if not exist "%GO_EXE%" (
    where go.exe >nul 2>nul
    if errorlevel 1 (
        echo [error] Go was not found. Install Go or place it in .tools\go.
        exit /b 1
    )
    set "GO_EXE=go.exe"
)

where npm.cmd >nul 2>nul
if errorlevel 1 (
    echo [error] npm.cmd was not found in PATH.
    exit /b 1
)

if not exist "%FRONTEND_DIR%\package.json" (
    echo [error] Frontend directory was not found: %FRONTEND_DIR%
    exit /b 1
)

if not exist "%PACKAGE_TEMPLATE_DIR%\README.md" (
    echo [error] Router package template was not found: %PACKAGE_TEMPLATE_DIR%\README.md
    exit /b 1
)

if not exist "%PACKAGE_TEMPLATE_DIR%\start.sh" (
    echo [error] Router package template was not found: %PACKAGE_TEMPLATE_DIR%\start.sh
    exit /b 1
)

if not exist "%ROUTER_PACKAGE_DIR%" mkdir "%ROUTER_PACKAGE_DIR%"
if not exist "%ROUTER_PACKAGE_DIR%\%ROUTER_DATA_DIR_NAME%" mkdir "%ROUTER_PACKAGE_DIR%\%ROUTER_DATA_DIR_NAME%"
if not exist "%ROUTER_PACKAGE_DIR%\bin" mkdir "%ROUTER_PACKAGE_DIR%\bin"

echo [1/4] Building frontend...
pushd "%FRONTEND_DIR%" >nul || (
    echo [error] Failed to open frontend directory.
    exit /b 1
)

if not exist "node_modules" (
    if exist "package-lock.json" (
        echo [info] node_modules not found, running npm ci...
        call npm.cmd ci
    ) else (
        echo [info] node_modules not found, running npm install...
        call npm.cmd install
    )
    if errorlevel 1 (
        popd >nul
        echo [error] Frontend dependency install failed.
        exit /b 1
    )
)

call npm.cmd run build
if errorlevel 1 (
    popd >nul
    echo [error] Frontend build failed.
    exit /b 1
)
popd >nul

echo [2/4] Building %ROUTER_GOOS%/%ROUTER_GOARCH% binary...
pushd "%ROOT_DIR%" >nul || (
    echo [error] Failed to open project root.
    exit /b 1
)

set "CGO_ENABLED=0"
set "GOOS=%ROUTER_GOOS%"
set "GOARCH=%ROUTER_GOARCH%"
"%GO_EXE%" build -o "%ROUTER_PACKAGE_DIR%\%ROUTER_BINARY_NAME%" ".\cmd\vpn-manager"
if errorlevel 1 (
    popd >nul
    echo [error] Go build failed.
    exit /b 1
)
popd >nul

echo [3/4] Preparing router bundle...
copy /Y "%PACKAGE_TEMPLATE_DIR%\README.md" "%ROUTER_PACKAGE_DIR%\README.md" >nul
if errorlevel 1 (
    echo [error] Failed to copy README.md into the router bundle.
    exit /b 1
)

copy /Y "%PACKAGE_TEMPLATE_DIR%\start.sh" "%ROUTER_PACKAGE_DIR%\start.sh" >nul
if errorlevel 1 (
    echo [error] Failed to copy start.sh into the router bundle.
    exit /b 1
)

if exist "%ROUTER_PACKAGE_DIR%\openvpn" del /F /Q "%ROUTER_PACKAGE_DIR%\openvpn" >nul 2>nul
if exist "%ROUTER_PACKAGE_DIR%\sing-box" del /F /Q "%ROUTER_PACKAGE_DIR%\sing-box" >nul 2>nul

set "OPENVPN_BUNDLE_PATH="
set "SINGBOX_BUNDLE_PATH="

if defined ROUTER_OPENVPN_BIN (
    if not exist "%ROUTER_OPENVPN_BIN%" (
        echo [error] ROUTER_OPENVPN_BIN does not exist: %ROUTER_OPENVPN_BIN%
        exit /b 1
    )
    copy /Y "%ROUTER_OPENVPN_BIN%" "%ROUTER_PACKAGE_DIR%\bin\openvpn" >nul
    if errorlevel 1 (
        echo [error] Failed to copy openvpn into the router bundle.
        exit /b 1
    )
    set "OPENVPN_BUNDLE_PATH=bin/openvpn"
) else if exist "%ROUTER_PACKAGE_DIR%\bin\openvpn" (
    set "OPENVPN_BUNDLE_PATH=bin/openvpn"
)

if defined ROUTER_SINGBOX_BIN (
    if not exist "%ROUTER_SINGBOX_BIN%" (
        echo [error] ROUTER_SINGBOX_BIN does not exist: %ROUTER_SINGBOX_BIN%
        exit /b 1
    )
    copy /Y "%ROUTER_SINGBOX_BIN%" "%ROUTER_PACKAGE_DIR%\bin\sing-box" >nul
    if errorlevel 1 (
        echo [error] Failed to copy sing-box into the router bundle.
        exit /b 1
    )
    set "SINGBOX_BUNDLE_PATH=bin/sing-box"
) else if exist "%ROUTER_PACKAGE_DIR%\bin\sing-box" (
    set "SINGBOX_BUNDLE_PATH=bin/sing-box"
)

(
    echo binary=%ROUTER_BINARY_NAME%
    echo goos=%ROUTER_GOOS%
    echo goarch=%ROUTER_GOARCH%
    echo default_port=%ROUTER_HTTP_PORT%
    echo package_dir=build\router
    echo data_dir=%ROUTER_DATA_DIR_NAME%
    echo openvpn_path=%OPENVPN_BUNDLE_PATH%
    echo singbox_path=%SINGBOX_BUNDLE_PATH%
) > "%ROUTER_PACKAGE_DIR%\bundle-info.txt"
if defined ROUTER_VERSION echo version=%ROUTER_VERSION%>> "%ROUTER_PACKAGE_DIR%\bundle-info.txt"
if defined ROUTER_COMMIT echo commit=%ROUTER_COMMIT%>> "%ROUTER_PACKAGE_DIR%\bundle-info.txt"

if not exist "%ROOT_DIR%\build" mkdir "%ROOT_DIR%\build"
set "ROUTER_ARCHIVE_PATH=%ROOT_DIR%\build\%ROUTER_ARCHIVE_NAME%"
where tar.exe >nul 2>nul
if errorlevel 1 (
    echo [warn] tar.exe was not found; release archive was not created.
) else (
    tar.exe -C "%ROUTER_PACKAGE_DIR%" -czf "%ROUTER_ARCHIVE_PATH%" .
    if errorlevel 1 (
        echo [error] Failed to create release archive.
        exit /b 1
    )
    echo [done] Release archive: %ROUTER_ARCHIVE_PATH%
)

echo [4/4] Router bundle ready: %ROUTER_PACKAGE_DIR%
echo [done] Copy the whole directory to the router and start it with ./start.sh
exit /b 0
