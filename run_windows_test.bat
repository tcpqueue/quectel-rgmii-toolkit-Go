@echo off
chcp 936 >nul
setlocal EnableExtensions

echo([信息] 正在启动 Windows 本地测试入口...
call "%~dp0windows-test\run_windows_test.bat"
