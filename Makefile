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

# Windows 下 GNU make 默认用 cmd.exe 当 shell，强制改用 Git Bash 的 sh；
# Linux/macOS 默认就是 /bin/sh，无需覆盖（硬编码 Windows 路径会让 Linux 上 make 直接报错）。
ifeq ($(OS),Windows_NT)
SHELL := D:/apps/git/Git/bin/sh.exe
.SHELLFLAGS := -c
endif

# 自动检测 pwsh / powershell
# 用裸名(pwsh/powershell),让 GnuWin32 make 的 CreateProcess 走 Windows PATH 解析;
# 不能用 command -v 的输出——那会返回 MSYS 路径 /c/Windows/...,原生 make 直执行时找不到。
PW := $(shell if command -v pwsh >/dev/null 2>&1; then echo pwsh; else echo powershell; fi)

.PHONY: build run restart stop status clean mysql mysql-stop

build:
	$(PW) -File make.ps1 build

run:
	$(PW) -File make.ps1 run

restart:
	$(PW) -File make.ps1 restart

daemon:
	$(PW) -File make.ps1 restart -daemon
	@echo ""
	@echo "后台运行中。查看日志:"
	@echo "  tail -f \$$TEMP/linkcode.log"
	@echo "  make status"

run-daemon:
	$(PW) -File make.ps1 run -daemon
	@echo ""
	@echo "后台运行中。查看日志:"
	@echo "  tail -f \$$TEMP/linkcode.log"
	@echo "  make status"

restart-daemon:
	$(PW) -File make.ps1 restart -daemon
	@echo ""
	@echo "后台运行中。查看日志:"
	@echo "  tail -f \$$TEMP/linkcode.log"
	@echo "  make status"

# 短别名:make restart-d / make run-d(等价于 -daemon 版本)。
# 注意:不能用 "make restart -d"——-d 是 make 内置 debug 开关,会被 make 吞掉且不透传。
restart-d: restart-daemon
run-d: run-daemon

stop:
	$(PW) -File make.ps1 stop

status:
	$(PW) -File make.ps1 status

clean:
	$(PW) -File make.ps1 clean

mysql:
	$(PW) -File make.ps1 mysql

mysql-stop:
	$(PW) -File make.ps1 mysql-stop

help:
	@echo "linkcode make targets:"
	@echo "  make run             编译 + 前台启动"
	@echo "  make run -daemon     编译 + 后台启动"
	@echo "  make run-daemon      编译 + 后台启动（快捷方式）"
	@echo "  make restart         前台重启，不编译"
	@echo "  make restart -daemon 后台重启，不编译"
	@echo "  make restart-daemon  后台重启，不编译（快捷方式）"
	@echo "  make restart-d       后台重启，不编译（-d 短别名）"
	@echo "  make run-d           编译 + 后台启动（-d 短别名）"
	@echo "  make daemon          后台重启，不编译"
	@echo "  make build           仅编译"
	@echo "  make stop            停止"
	@echo "  make status          查看状态"
	@echo "  make clean           清理编译产物"
	@echo "  make mysql           后台启动 MySQL（数据目录: ~/mysql-data/）"
	@echo "  make mysql-stop      停止 MySQL"
