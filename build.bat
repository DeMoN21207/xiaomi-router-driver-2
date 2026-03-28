@echo off
setlocal EnableExtensions

set "ROOT_DIR=%~dp0"
if "%ROOT_DIR:~-1%"=="\" set "ROOT_DIR=%ROOT_DIR:~0,-1%"

echo [info] Windows app build is deprecated. Building Linux router bundle instead...
call "%ROOT_DIR%\package_router.bat"
exit /b %errorlevel%
