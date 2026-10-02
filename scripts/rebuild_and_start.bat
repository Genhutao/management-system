@echo off
rem ============================================================
rem  Auto build & start for the local verify instance (port 18095).
rem  Run from cmd / Explorer double-click. ASCII-only on purpose:
rem  Chinese text in .bat garbles under the default GBK codepage.
rem  Overridable via environment: PORT, DB_PATH
rem ============================================================
setlocal enabledelayedexpansion

if "%PORT%"=="" set "PORT=18095"
set "ROOT=%~dp0.."
set "BACKEND=%ROOT%\backend"
set "DEPLOY=%BACKEND%\.verify\deploy-merged"
if "%DB_PATH%"=="" set "DB_PATH=%BACKEND%\.verify\ui_demo.db"

set "PATH=%PATH%;C:\Program Files\Go\bin"
set "CGO_ENABLED=0"
set "GOPROXY=off"

echo [1/5] Building backend/cmd/server ...
if not exist "%DEPLOY%" mkdir "%DEPLOY%"
pushd "%BACKEND%"
go build -o "%DEPLOY%\xgh_merged_new.exe" ./cmd/server
if errorlevel 1 (
  echo Build FAILED - old service left untouched. Aborting.
  popd
  exit /b 1
)
popd

echo [2/5] Syncing static/ ...
if not exist "%DEPLOY%\static" mkdir "%DEPLOY%\static"
xcopy "%BACKEND%\static\*" "%DEPLOY%\static\" /e /y /q >nul

echo [3/5] Checking uploads / rbac_model.conf / key files ...
if not exist "%DEPLOY%\uploads" if exist "%BACKEND%\uploads" xcopy "%BACKEND%\uploads" "%DEPLOY%\uploads\" /e /y /q /i >nul
if not exist "%DEPLOY%\rbac_model.conf" if exist "%BACKEND%\rbac_model.conf" copy "%BACKEND%\rbac_model.conf" "%DEPLOY%\" >nul
for %%k in (jwt_secret.key crypto_secret.key) do (
  if not exist "%DEPLOY%\%%k" if exist "%BACKEND%\%%k" (
    copy "%BACKEND%\%%k" "%DEPLOY%\%%k" >nul
    echo   copied %%k from backend ^(first time only, never overwritten^)
  )
)
if not exist "%DEPLOY%\publicity-cache" if exist "%BACKEND%\publicity-cache" (
  mklink /J "%DEPLOY%\publicity-cache" "%BACKEND%\publicity-cache" >nul 2>nul
  if not exist "%DEPLOY%\publicity-cache" xcopy "%BACKEND%\publicity-cache" "%DEPLOY%\publicity-cache\" /e /y /q /i >nul
)

echo [4/5] Stopping old service on port %PORT% ...
set "OLDPID="
for /f "tokens=5" %%p in ('netstat -ano ^| findstr /R /C:":%PORT% .*LISTENING"') do (
  taskkill /PID %%p /F >nul 2>&1
  set "OLDPID=%%p"
)
set /a WAITED=0
:waitport
set "STILL="
for /f "tokens=5" %%p in ('netstat -ano ^| findstr /R /C:":%PORT% .*LISTENING"') do set "STILL=%%p"
if defined STILL (
  if !WAITED! lss 10 (
    ping -n 2 127.0.0.1 >nul
    set /a WAITED+=1
    goto waitport
  )
  echo Port %PORT% still held after 10s - aborting.
  exit /b 1
)
if defined OLDPID (echo   stopped PID !OLDPID!) else (echo   nothing was listening)

echo [5/5] Swapping exe and starting ...
if exist "%DEPLOY%\xgh_merged.exe" move /y "%DEPLOY%\xgh_merged.exe" "%DEPLOY%\xgh_merged_prev.exe" >nul
move /y "%DEPLOY%\xgh_merged_new.exe" "%DEPLOY%\xgh_merged.exe" >nul
powershell -NoProfile -Command "$env:PORT='%PORT%'; $env:DB_PATH='%DB_PATH%'; $p=Start-Process -FilePath '.\xgh_merged.exe' -WorkingDirectory '%DEPLOY%' -PassThru -WindowStyle Hidden; $p.Id | Out-File -Encoding ascii '%DEPLOY%\service.pid'"

echo Health check ^(up to 20s^) ...
set "OK="
for /l %%i in (1,1,20) do (
  if not defined OK (
    ping -n 2 127.0.0.1 >nul
    curl -s -o nul -w "%%{http_code}" http://127.0.0.1:%PORT%/ 2>nul | findstr "200" >nul && set "OK=1"
  )
)
if not defined OK (
  echo HEALTH CHECK FAILED: GET / did not return 200 within 20s.
  echo If the process died at boot, run it in foreground to see the log:
  echo   set PORT=%PORT% ^&^& set DB_PATH=%DB_PATH% ^&^& %DEPLOY%\xgh_merged.exe
  exit /b 1
)
set /p NEWPID=<"%DEPLOY%\service.pid"
echo DONE: service ready at http://127.0.0.1:%PORT%/  ^(PID !NEWPID!^)
endlocal
