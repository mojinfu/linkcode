# linkcode Makefile
# 企微 Bot 桥接 Claude Code 的子进程管理系统
#
# 用法:
#   make run               编译 + 前台启动（可 Ctrl+C 停止）
#   make run -daemon       编译 + 后台启动
#   make restart           前台重启（不编译）
#   make restart -daemon   后台重启（不编译）
#   make build             仅编译
#   make stop              停止
#   make status            查看状态
#   make clean             清理编译产物
#
# 脚本逻辑在 make.ps1 中，支持 PowerShell Core (pwsh) 和 Windows PowerShell。

# 强制使用 Git Bash 的 sh，避免 Windows 下 make 默认用 cmd.exe
SHELL := D:/apps/git/Git/bin/sh.exe
.SHELLFLAGS := -c

# 自动检测 pwsh / powershell
PW := $(shell command -v pwsh 2>/dev/null || command -v powershell 2>/dev/null || echo powershell)

.PHONY: build run restart stop status clean

build:
	$(PW) make.ps1 build

run:
	$(PW) make.ps1 run

restart:
	$(PW) make.ps1 restart

daemon:
	$(PW) make.ps1 restart -daemon
	@echo ""
	@echo "后台运行中。查看日志:"
	@echo "  tail -f \$$TEMP/linkcode.log"
	@echo "  make status"

run-daemon:
	$(PW) make.ps1 run -daemon
	@echo ""
	@echo "后台运行中。查看日志:"
	@echo "  tail -f \$$TEMP/linkcode.log"
	@echo "  make status"

restart-daemon:
	$(PW) make.ps1 restart -daemon
	@echo ""
	@echo "后台运行中。查看日志:"
	@echo "  tail -f \$$TEMP/linkcode.log"
	@echo "  make status"

stop:
	$(PW) make.ps1 stop

status:
	$(PW) make.ps1 status

clean:
	$(PW) make.ps1 clean

help:
	@echo "linkcode make targets:"
	@echo "  make run             编译 + 前台启动"
	@echo "  make run -daemon     编译 + 后台启动"
	@echo "  make run-daemon      编译 + 后台启动（快捷方式）"
	@echo "  make restart         前台重启，不编译"
	@echo "  make restart -daemon 后台重启，不编译"
	@echo "  make restart-daemon  后台重启，不编译（快捷方式）"
	@echo "  make daemon          后台重启，不编译"
	@echo "  make build           仅编译"
	@echo "  make stop            停止"
	@echo "  make status          查看状态"
	@echo "  make clean           清理编译产物"
