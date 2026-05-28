# Thin wrapper that delegates to make.ps1 for cross-platform support.
# Requires PowerShell Core (pwsh): https://github.com/PowerShell/PowerShell
#
#   make build      →  pwsh make.ps1 build
#   make run        →  pwsh make.ps1 run
#   make stop       →  pwsh make.ps1 stop
#   make restart    →  pwsh make.ps1 restart
#   make status     →  pwsh make.ps1 status
#   make clean      →  pwsh make.ps1 clean

.PHONY: build run stop restart status clean

build run stop restart status clean:
	pwsh make.ps1 $@
