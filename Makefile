.PHONY: bootstrap generate dev-backend dev-web test test-integration test-e2e build
bootstrap:
	pnpm --dir web install
generate:
	pwsh -File scripts/check-generation.ps1
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
	pnpm --dir web build
	powershell -ExecutionPolicy Bypass -File scripts/embed-web.ps1
	pnpm --dir web e2e
build:
	pnpm --dir web build
	pwsh -File scripts/embed-web.ps1
	go build ./cmd/hostd ./cmd/hostctl
