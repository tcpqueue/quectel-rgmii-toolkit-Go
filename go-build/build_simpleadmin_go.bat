@echo off
chcp 936 >nul
setlocal EnableExtensions
title 编译 SimpleAdmin Go 二进制文件

set "ROOT=%~dp0.."
for %%I in ("%ROOT%") do set "ROOT=%%~fI"
set "GO_SRC=%~dp0simpleadmin-go"
set "OUT_DIR=%ROOT%\development\simpleadmin"
set "OUT_FILE=%OUT_DIR%\simpleadmin-httpd.armv7"

rem Optional: set GO_EXE before running this script.
rem Example:
rem   set "GO_EXE=C:\Program Files\Go\bin\go.exe"
rem   build_simpleadmin_go.bat
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
echo([信息] 安装到设备不需要 Go，直接运行 toolkit.bat 即可。
echo([信息] 重新编译 simpleadmin-httpd.armv7 需要 Go for Windows。
echo([信息] 请安装 Go；如果 Go 安装在自定义路径，请先执行：
echo(set "GO_EXE=C:\Program Files\Go\bin\go.exe"
echo(build_simpleadmin_go.bat
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
call :CHECK_GO_126

if not exist "%GO_SRC%\go.mod" goto MISSING_GO_SRC
if not exist "%OUT_DIR%" goto MISSING_OUT_DIR
goto BUILD_GO

:MISSING_GO_SRC
echo([错误] 未找到 Go 源码目录：
echo(%GO_SRC%
pause
exit /b 1

:MISSING_OUT_DIR
echo([错误] 未找到输出目录：
echo(%OUT_DIR%
pause
exit /b 1

:BUILD_GO
echo([信息] 正在编译 Linux ARMv7 SimpleAdmin Go 二进制文件...
pushd "%GO_SRC%" >nul
set "GOOS=linux"
set "GOARCH=arm"
set "GOARM=7"
set "CGO_ENABLED=0"
set "GOTOOLCHAIN=local"
set "GOEXPERIMENT="
echo([信息] Go 1.26.3 可用；使用本地 toolchain 和保守可复现参数编译。
"%GO_EXE%" build -buildvcs=false -trimpath -ldflags "-s -w" -o "%OUT_FILE%" ./cmd/simpleadmin-httpd
if errorlevel 1 goto GO_BUILD_FAILED
popd >nul

if not exist "%OUT_FILE%" goto OUTPUT_MISSING
goto BUILD_OK

:OUTPUT_MISSING
echo([错误] 编译结束，但未生成输出文件：
echo(%OUT_FILE%
pause
exit /b 1

:BUILD_OK
echo([成功] 已生成：
echo(%OUT_FILE%
echo([信息] 编译产物信息：
"%GO_EXE%" version -m "%OUT_FILE%"
echo([成功] 运行 toolkit.bat 可将重新编译的二进制安装到设备。
pause
exit /b 0

:GO_BUILD_FAILED
popd >nul
echo([错误] go build 编译失败。
pause
exit /b 1

:TRY_GO
if defined GO_EXE exit /b 0
if exist "%~1" set "GO_EXE=%~1"
exit /b 0
:CHECK_GO_126
for /f "tokens=3" %%V in ('"%GO_EXE%" version') do set "GO_VERSION_TEXT=%%V"
if /i "%GO_VERSION_TEXT%"=="go1.26.3" (
  echo([信息] 当前 Go 版本是 go1.26.3，允许编译。
  echo([信息] v2.10 已修复 SMD AT 写入流程，避免通过 shell cat 写串口。
)
exit /b 0
