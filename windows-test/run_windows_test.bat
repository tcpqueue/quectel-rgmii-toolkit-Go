@echo off
chcp 936 >nul
setlocal EnableExtensions

for %%I in ("%~dp0..") do set "ROOT=%%~fI"
set "TEST_DIR=%ROOT%\windows-test"
set "EXE=%TEST_DIR%\bin\simpleadmin-httpd-windows-amd64.exe"
set "STATIC_DIR=%ROOT%\development\simpleadmin\www"
set "DATA_DIR=%TEST_DIR%\data"
set "AUTH_FILE=%DATA_DIR%\simpleadmin.auth"
set "TTL_FILE=%DATA_DIR%\ttlvalue"
set "CERT_FILE=%DATA_DIR%\server.crt"
set "KEY_FILE=%DATA_DIR%\server.key"
set "HTTP_ADDR=127.0.0.1:18080"
set "URL=http://%HTTP_ADDR%/"

if not exist "%STATIC_DIR%\index.html" (
  echo([错误] 未找到静态网页目录或缺少 index.html：
  echo(%STATIC_DIR%
  pause
  exit /b 1
)

powershell -NoProfile -ExecutionPolicy Bypass -Command "$c=Test-NetConnection -ComputerName 127.0.0.1 -Port 18080 -InformationLevel Quiet; if($c){exit 1}else{exit 0}" >nul 2>nul
if errorlevel 1 (
  echo([错误] 端口 18080 已被占用。
  echo([信息] 请关闭旧测试服务，或修改本脚本中的 HTTP_ADDR。
  pause
  exit /b 1
)

if not exist "%DATA_DIR%" mkdir "%DATA_DIR%"
if not exist "%AUTH_FILE%" echo admin:admin>"%AUTH_FILE%"
if not exist "%TTL_FILE%" echo 0>"%TTL_FILE%"

if not exist "%EXE%" (
  echo([信息] 未找到 Windows 测试二进制，正在自动编译...
  call "%TEST_DIR%\build_windows_test.bat"
  if errorlevel 1 (
    pause
    exit /b 1
  )
)

echo.
echo(==============================================================
echo(SimpleAdmin Windows 本地测试服务
echo(地址:     %URL%
echo(用户名:   admin
echo(密码:     admin
echo(静态目录: %STATIC_DIR%
echo(模式:     mock 硬件接口，不启用 TLS
echo(==============================================================
echo.

start "" "%URL%"

"%EXE%" serve --mock --no-tls --http %HTTP_ADDR% --static "%STATIC_DIR%" --auth-file "%AUTH_FILE%" --ttl-file "%TTL_FILE%" --cert "%CERT_FILE%" --key "%KEY_FILE%"

echo.
echo([信息] 服务已停止。
pause
