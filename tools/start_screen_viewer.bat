@echo off
setlocal
cd /d "%~dp0"
python "%~dp0screen_viewer.py"
if errorlevel 1 pause
