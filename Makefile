BINARY  := bin/linkcode
CONFIG  := configs/linkcode.yaml
LOG     := /tmp/linkcode.log

PIDFILE := bin/.linkcode.pid

.PHONY: build run stop restart status clean

build:
	go build -o $(BINARY) ./cmd/linkcode/

run: stop build
	@if [ -f $(PIDFILE) ]; then \
		echo "ERROR: linkcode appears to be running (pid $$(cat $(PIDFILE)))."; \
		echo "  Run 'make stop' first."; \
		exit 1; \
	fi
	nohup $(BINARY) -config $(CONFIG) > $(LOG) 2>&1 &
	sleep 2
	@cat $(PIDFILE) 2>/dev/null && echo "Started. Log: $(LOG)" || echo "WARNING: may not have started, check $(LOG)"

stop:
	@if [ -f $(PIDFILE) ]; then \
		PID=$$(cat $(PIDFILE)); \
		if kill -0 $$PID 2>/dev/null; then \
			echo "Stopping linkcode (pid $$PID)..."; \
			kill $$PID; \
			while kill -0 $$PID 2>/dev/null; do sleep 0.5; done; \
			echo "Stopped."; \
		else \
			rm -f $(PIDFILE); \
		fi; \
	fi
	@# Fallback: kill any leftover linkcode processes without PID files.
	@pkill -f "$(BINARY)" 2>/dev/null && echo "Cleaned up leftover processes." || true
	@rm -f $(PIDFILE)

restart: stop build
	nohup $(BINARY) -config $(CONFIG) > $(LOG) 2>&1 &
	sleep 2
	@cat $(PIDFILE) 2>/dev/null && echo "Restarted. Log: $(LOG)" || echo "WARNING: may not have started, check $(LOG)"

status:
	@if [ -f $(PIDFILE) ]; then \
		PID=$$(cat $(PIDFILE)); \
		if kill -0 $$PID 2>/dev/null; then \
			echo "Running (pid $$PID)"; \
		else \
			echo "Not running (stale pid file for $$PID)"; \
			rm -f $(PIDFILE); \
		fi; \
	else \
		echo "Not running."; \
	fi

clean:
	rm -f $(BINARY) $(PIDFILE)
