@echo off
rem Build the debug APK with JDK 17, Android SDK 34 and Gradle 8.7.
rem Set JAVA_HOME and ANDROID_HOME, and add Gradle to PATH before running.
cd /d "%~dp0"
call gradle --no-daemon assembleDebug
exit /b %ERRORLEVEL%
