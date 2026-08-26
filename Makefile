.PHONY: bootstrap generate check-generated check-embedded check-embedded-windows dev-backend dev-web test test-integration test-e2e test-e2e-windows build build-windows test-relay-probe check-relay-package check-relay-package-windows
bootstrap:
	pnpm --dir web install
generate:
	go run ./cmd/openapi-gen
check-generated:
	go run ./cmd/openapi-gen -check
	go test ./internal/controller -run '^TestOpenAPIContractMatchesRegisteredRoutes$$' -count=1
check-embedded:
	sh scripts/check-embedded.sh
check-embedded-windows:
	powershell -ExecutionPolicy Bypass -File scripts/check-embedded.ps1
dev-backend:
	go run ./cmd/hostd --data-root .hostd-dev --fake-runtime
dev-web:
	pnpm --dir web dev
test:
	go test ./...
	pnpm --dir web test
test-integration:
	go test ./...
test-e2e:
	sh scripts/capture-visuals.sh
test-e2e-windows:
	powershell -ExecutionPolicy Bypass -File scripts/capture-visuals.ps1
build:
	pnpm --dir web build
	sh scripts/embed-web.sh
	go build ./cmd/hostd ./cmd/hostctl ./cmd/rig-relay ./cmd/rig-relay-probe
build-windows:
	pnpm --dir web build
	powershell -ExecutionPolicy Bypass -File scripts/embed-web.ps1
	go build ./cmd/hostd ./cmd/hostctl ./cmd/rig-relay ./cmd/rig-relay-probe
test-relay-probe:
	go test ./cmd/rig-relay-probe -count=1
check-relay-package:
	go test ./cmd/rig-relay-probe -run '^TestRelayPackagingContract$$' -count=1
check-relay-package-windows:
	powershell -NoProfile -ExecutionPolicy Bypass -File scripts/check-relay-packaging.ps1 -SelfTest
