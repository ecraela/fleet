GO ?= go
GOFMT ?= gofmt
PROTOC ?= protoc

.PHONY: generate-control format check test build demo smoke outage down logs

format: web-format
	$(GO) fmt ./...

check: web-check
	python3 scripts/legal.py check
	test -z "$$($(GOFMT) -l .)"
	$(GO) vet ./...
	python3 -m py_compile scripts/hardening.py scripts/recovery.py scripts/test_recovery.py scripts/standalone.py scripts/connector.py
	python3 -m py_compile scripts/agentctl.py scripts/release.py scripts/agent_releases.py scripts/test_agentctl.py
	python3 -m py_compile scripts/test_release_compatibility.py
	python3 -m py_compile scripts/verify_release_images.py scripts/linux_installer_smoke.py scripts/prepare_release_lab.py
	python3 -m py_compile scripts/selfhostctl.py scripts/selfhost_smoke.py scripts/test_selfhostctl.py
	python3 -m py_compile scripts/managed_upgrade_smoke.py
	python3 -m py_compile scripts/onboarding.py
	python3 -m py_compile scripts/geography.py scripts/pair.py scripts/requests.py scripts/traffic_rules.py
	python3 -m py_compile scripts/lab.py scripts/test_lab.py
	python3 -m py_compile scripts/test_postgres.py scripts/test_test_postgres.py
	python3 -m py_compile scripts/legal.py scripts/test_legal.py
	python3 -m py_compile scripts/container_sources.py scripts/test_container_sources.py

test: web-test
	python3 scripts/test_postgres.py --variable HAPROXY_TEST_DATABASE_URL --database haproxy_manager_test --reuse-env -- $(GO) test -race ./...
	python3 -m unittest discover -s scripts -p 'test_*.py'

.PHONY: vuln hardening
vuln:
	$(GO) run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...

hardening:
	python3 scripts/hardening.py all $(HAPROXY_HARDENING_ARGS)

.PHONY: agent-releases
agent-releases:
	python3 scripts/agent_releases.py

.PHONY: onboarding
onboarding:
	docker build -t ecraela-haproxy-onboarding:local .
	python3 scripts/onboarding.py

.PHONY: geography
geography:
	python3 scripts/geography.py

.PHONY: traffic-rules
traffic-rules:
	python3 scripts/traffic_rules.py

.PHONY: selfhost-acceptance
selfhost-acceptance:
	python3 scripts/selfhost_smoke.py $(HAPROXY_SELFHOST_ARGS)

.PHONY: release-qualification
release-qualification:
	python3 scripts/selfhost_smoke.py $(HAPROXY_RELEASE_ARGS)

.PHONY: managed-upgrade-acceptance
managed-upgrade-acceptance:
	python3 scripts/managed_upgrade_smoke.py $(HAPROXY_MANAGED_ARGS)

build: web-build
	mkdir -p .build.local
	$(GO) build -trimpath -o .build.local/haproxy-manager ./cmd/haproxy-manager
	$(GO) build -trimpath -o .build.local/haproxy-agent ./cmd/haproxy-agent

demo:
	./scripts/demo.sh up

smoke:
	./scripts/demo.sh smoke

outage:
	./scripts/demo.sh outage

down:
	./scripts/demo.sh down

logs:
	./scripts/demo.sh logs

# protoc 36.2 is provided by the developer; generators are pinned here.
generate-control:
	test "$$($(PROTOC) --version)" = "libprotoc 36.2"
	mkdir -p .build.local/proto-tools
	GOBIN="$(CURDIR)/.build.local/proto-tools" $(GO) install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.12
	GOBIN="$(CURDIR)/.build.local/proto-tools" $(GO) install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.6.2
	$(PROTOC) --plugin=protoc-gen-go=$(CURDIR)/.build.local/proto-tools/protoc-gen-go --plugin=protoc-gen-go-grpc=$(CURDIR)/.build.local/proto-tools/protoc-gen-go-grpc --go_out=. --go_opt=module=github.com/ecraela/fleet --go-grpc_out=. --go-grpc_opt=module=github.com/ecraela/fleet proto/control/v1/control.proto

.PHONY: web-install web-format web-check web-test web-build
web-install:
	cd web && pnpm install --frozen-lockfile --ignore-scripts
web-format:
	cd web && pnpm format
web-check:
	cd web && pnpm format:check && pnpm check
web-test:
	cd web && pnpm test
web-build:
	cd web && pnpm build

.PHONY: web-audit
web-audit:
	cd web && pnpm audit --audit-level low

.PHONY: legal-update legal-check
legal-update:
	python3 scripts/legal.py update
legal-check:
	python3 scripts/legal.py check
