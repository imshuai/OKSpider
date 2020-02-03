@echo off

@rem build linux

set GOOS=linux
set GOARCH=amd64
set CGO_ENABLED=0

go build -ldflags "-w -s" -o %1\bin\%2_x64 

@rem succeed or failed
@if %errorlevel%==0 (echo build linux-amd64 success) else (echo build linux-amd64 failed)

set GOOS=linux
set GOARCH=386
set CGO_ENABLED=0

go build -ldflags "-w -s" -o %1\bin\%2_x86 

@rem succeed or failed
@if %errorlevel%==0 (echo build linux-i386 success) else (echo build linux-i386 failed)

set GOOS=windows
set GOARCH=amd64
set CGO_ENABLED=0

go build -ldflags "-w -s" -o %1\bin\%2_x64.exe 

@rem succeed or failed
@if %errorlevel%==0 (echo build windows-amd64 success) else (echo build windows-amd64 failed)

set GOOS=windows
set GOARCH=386
set CGO_ENABLED=0

go build -ldflags "-w -s" -o %1\bin\%2_x86.exe 

@rem succeed or failed
@if %errorlevel%==0 (echo build windows-i386 success) else (echo build windows-i386 failed)

@rem build arm6

set GOARCH=arm
set GOARM=7
set GOOS=linux
set CGO_ENABLED=0

go build -ldflags "-w -s" -o %1\bin\%2_arm7 
@rem succeed or failed
@if %errorlevel%==0 (echo build linux-arm6 success) else (echo build linux-arm6 failed)
