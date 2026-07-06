@echo off
chcp 936 >nul
setlocal EnableExtensions

title 编译 SimpleAdmin Windows 本地测试版

for %%I in ("%~dp0..") do set "ROOT=%%~fI"
set "SRC_DIR=%ROOT%\go-build\simpleadmin-go"
set "OUT_DIR=%ROOT%\windows-test\bin"
set "OUT_EXE=%OUT_DIR%\simpleadmin-httpd-windows-amd64.exe"

rem 可选：如果 Go 没有加入 PATH，可以先设置 GO_EXE 再运行本脚本。
rem 示例：
rem   set "GO_EXE=C:\Program Files\Go\bin\go.exe"
rem   windows-test\build_windows_test.bat
if not defined GO_EXE goto FIND_GO
set "GO_EXE=%GO_EXE:"=%"
goto CHECK_GO

:FIND_GO
where go.exe >nul 2>nul
if errorlevel 1 goto SEARCH_COMMON_PATHS
set "GO_EXE=go.exe"
goto CHECK_GO

:SEARCH_COMMON_PATHS
call :TRY_GO "%GOROOT%\bin\go.exe"
call :TRY_GO "%ProgramFiles%\Go\bin\go.exe"
call :TRY_GO "%ProgramFiles(x86)%\Go\bin\go.exe"
call :TRY_GO "%LOCALAPPDATA%\Programs\Go\bin\go.exe"
call :TRY_GO "C:\Go\bin\go.exe"
if defined GO_EXE goto CHECK_GO

echo([错误] 未找到 Go for Windows。
echo([信息] 如果只是运行本地测试，请直接运行根目录 run_windows_test.bat；只有重新编译 Windows 测试二进制时才需要 Go。
echo([信息] 请安装 Go for Windows，或先指定 GO_EXE 后再运行：
echo(set "GO_EXE=C:\Program Files\Go\bin\go.exe"
echo(windows-test\build_windows_test.bat
pause
exit /b 1

:CHECK_GO
"%GO_EXE%" version >nul 2>nul
if errorlevel 1 goto GO_VERSION_FAILED
goto GO_READY

:GO_VERSION_FAILED
echo([错误] go.exe 运行失败：
echo(%GO_EXE%
pause
exit /b 1

:GO_READY
echo([信息] 使用 Go: %GO_EXE%
"%GO_EXE%" version

if not exist "%SRC_DIR%\go.mod" goto MISSING_GO_SRC
if not exist "%OUT_DIR%" mkdir "%OUT_DIR%"
if errorlevel 1 goto MKDIR_FAILED
goto RUN_TESTS

:MISSING_GO_SRC
echo([错误] 未找到 Go 源码目录：
echo(%SRC_DIR%
pause
exit /b 1

:MKDIR_FAILED
echo([错误] 无法创建输出目录：
echo(%OUT_DIR%
pause
exit /b 1

:RUN_TESTS
pushd "%SRC_DIR%" >nul
if errorlevel 1 goto PUSHD_FAILED
set "GOOS=windows"
set "GOARCH=amd64"
set "CGO_ENABLED=0"
set "GOTOOLCHAIN=local"
set "GOEXPERIMENT="

echo([信息] 正在 Windows 目标环境下运行 Go 测试...
"%GO_EXE%" test ./...
if errorlevel 1 goto GO_TEST_FAILED

echo([信息] 正在编译 Windows 测试二进制文件...
"%GO_EXE%" build -buildvcs=false -trimpath -ldflags "-s -w" -o "%OUT_EXE%" ./cmd/simpleadmin-httpd
if errorlevel 1 goto GO_BUILD_FAILED
popd >nul

if not exist "%OUT_EXE%" goto OUTPUT_MISSING
goto BUILD_OK

:PUSHD_FAILED
echo([错误] 无法进入 Go 源码目录：
echo(%SRC_DIR%
pause
exit /b 1

:GO_TEST_FAILED
popd >nul
echo([错误] go test 测试失败。
pause
exit /b 1

:GO_BUILD_FAILED
popd >nul
echo([错误] go build 编译失败。
pause
exit /b 1

:OUTPUT_MISSING
echo([错误] 编译结束，但未生成输出文件：
echo(%OUT_EXE%
pause
exit /b 1

:BUILD_OK
echo([成功] 已生成：
echo(%OUT_EXE%
echo([信息] 可继续运行根目录 run_windows_test.bat 启动本地测试服务。
exit /b 0

:TRY_GO
if defined GO_EXE exit /b 0
if exist "%~1" set "GO_EXE=%~1"
exit /b 0
