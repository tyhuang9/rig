package main

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
)

const (
	packagingBuilderRef  = "docker.io/library/golang:1.26.7-bookworm@sha256:e8c859f5632dcfde7b32d2012b4351728f6437930887c2f6a91ea242459e5514"
	packagingRuntimeRef  = "gcr.io/distroless/static-debian13:nonroot@sha256:1c2c046bc09ed40fad370b599a0b1ae7987f55b01e247cf27a7c27cd97e5bbc7"
	packagingPostgresRef = "docker.io/library/postgres:18.6-bookworm@sha256:1c59e2c3c818eaa0f0628f695b36e7c9e362d6b219b36a54a32df645cbd7e1af"
	packagingFrontendRef = "docker/dockerfile:1.7@sha256:a57df69d0ea827fb7266491f2813635de6f17269be881f696fbfdf2d83dda33e"
)

var packagingSHARef = regexp.MustCompile(`^[a-z0-9][a-z0-9._:/-]*@sha256:[0-9a-f]{64}$`)

func TestRelayPackagingContract(t *testing.T) {
	root := packagingRoot(t)
	files := map[string]string{}
	for _, name := range []string{
		"deploy/relay/Dockerfile", "deploy/relay/Dockerfile.dockerignore", "deploy/relay/compose.yaml",
		"deploy/relay/compose.direct-tls.yaml", "deploy/relay/.env.example", "deploy/relay/secrets/.gitignore",
		"docs/relay-operations.md", "scripts/check-relay-packaging.ps1", "Makefile", "README.md",
	} {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
		if err != nil {
			t.Fatalf("required packaging file unavailable: %s", name)
		}
		files[name] = normalizePackagingText(string(data))
	}

	errors := portablePackagingErrors(files)
	if len(errors) != 0 {
		t.Fatalf("packaging contract failed: %s", strings.Join(errors, ", "))
	}

	t.Run("accept_crlf", func(t *testing.T) {
		crlf := clonePackagingFiles(files)
		for name, content := range crlf {
			crlf[name] = strings.ReplaceAll(content, "\n", "\r\n")
		}
		if errors := portablePackagingErrors(crlf); len(errors) != 0 {
			t.Fatalf("CRLF packaging contract failed: %s", strings.Join(errors, ", "))
		}
	})

	mutations := []struct {
		name, file, old, replacement, want string
	}{
		{"frontend tag", "deploy/relay/Dockerfile", packagingFrontendRef, "docker/dockerfile:1.7", "docker_frontend_pin"},
		{"builder tag", "deploy/relay/Dockerfile", packagingBuilderRef, "docker.io/library/golang:latest", "docker_pins"},
		{"context expansion", "deploy/relay/Dockerfile.dockerignore", "!internal/relay/**", "!internal/relay/**\n!deploy/**", "context_allowlist"},
		{"root uid", "deploy/relay/compose.yaml", `    user: "65532:65532"`, `    user: "0:0"`, "compose_contract"},
		{"short bind", "deploy/relay/compose.yaml", "    volumes:\n      - relay-postgres-data:/var/lib/postgresql", "    volumes:\n      - /tmp:/var/lib/postgresql", "compose_contract"},
		{"long bind", "deploy/relay/compose.yaml", "    volumes:\n      - relay-postgres-data:/var/lib/postgresql", "    volumes:\n      - type: bind\n        source: /tmp\n        target: /var/lib/postgresql", "compose_contract"},
		{"quoted host network", "deploy/relay/compose.yaml", "    init: true", "    network_mode: \"host\"\n    init: true", "compose_contract"},
		{"quoted host pid", "deploy/relay/compose.yaml", "    init: true", "    pid: 'host'\n    init: true", "compose_contract"},
		{"quoted host ipc", "deploy/relay/compose.yaml", "    init: true", "    ipc: \"host\"\n    init: true", "compose_contract"},
		{"extra hosts rebind", "deploy/relay/compose.yaml", "    init: true", "    extra_hosts:\n      - api.github.com=127.0.0.1\n    init: true", "compose_contract"},
		{"dns override", "deploy/relay/compose.yaml", "    init: true", "    dns: 198.51.100.53\n    init: true", "compose_contract"},
		{"link alias", "deploy/relay/compose.yaml", "    init: true", "    links:\n      - postgres:api.github.com\n    init: true", "compose_contract"},
		{"hostname override", "deploy/relay/compose.yaml", "    init: true", "    hostname: api.github.com\n    init: true", "compose_contract"},
		{"inline public postgres port", "deploy/relay/compose.yaml", "    image: " + packagingPostgresRef, "    ports: [\"0.0.0.0:5432:5432\"]\n    image: " + packagingPostgresRef, "compose_contract"},
		{"map public relay port", "deploy/relay/compose.yaml", `    image: "${HOSTD_RELAY_IMAGE:`, "    ports:\n      - target: 7346\n        published: 7346\n        host_ip: 0.0.0.0\n    image: \"${HOSTD_RELAY_IMAGE:", "compose_contract"},
		{"unconfined seccomp", "deploy/relay/compose.yaml", "      - no-new-privileges:true", "      - no-new-privileges:true\n      - seccomp:unconfined", "compose_contract"},
		{"unconfined apparmor", "deploy/relay/compose.yaml", "      - no-new-privileges:true", "      - no-new-privileges:true\n      - apparmor:unconfined", "compose_contract"},
		{"extra capability", "deploy/relay/compose.yaml", "    cap_drop:\n      - ALL", "    cap_drop:\n      - ALL\n    cap_add:\n      - SYS_ADMIN", "compose_contract"},
		{"device", "deploy/relay/compose.yaml", "    pids_limit: 256", "    devices:\n      - /dev/kvm:/dev/kvm\n    pids_limit: 256", "compose_contract"},
		{"docker socket", "deploy/relay/compose.yaml", "    pids_limit: 256", "    volumes:\n      - /var/run/docker.sock:/var/run/docker.sock\n    pids_limit: 256", "compose_contract"},
		{"extra network", "deploy/relay/compose.yaml", "      - relay-edge", "      - relay-edge\n      - default", "compose_contract"},
		{"network alias rebind", "deploy/relay/compose.yaml", "    networks:\n      - relay-database\n      - relay-edge", "    networks:\n      relay-database: {}\n      relay-edge:\n        aliases: [api.github.com]", "compose_contract"},
		{"secret remap", "deploy/relay/compose.yaml", "      - relay_tls_ca", "      - source: relay_tls_ca\n        target: changed", "compose_contract"},
		{"pull policy", "deploy/relay/compose.yaml", "    init: true", "    pull_policy: always\n    init: true", "compose_contract"},
		{"environment file", "deploy/relay/compose.yaml", "    init: true", "    env_file: /tmp/relay.env\n    init: true", "compose_contract"},
		{"profile", "deploy/relay/compose.yaml", "    init: true", "    profiles: [unsafe]\n    init: true", "compose_contract"},
		{"unknown service key", "deploy/relay/compose.yaml", "    init: true", "    x_unreviewed_runtime: true\n    init: true", "compose_contract"},
		{"public default", "deploy/relay/compose.direct-tls.yaml", "${HOSTD_RELAY_PUBLISH_ADDRESS:-127.0.0.1}", "${HOSTD_RELAY_PUBLISH_ADDRESS:-0.0.0.0}", "direct_contract"},
		{"secret env", "deploy/relay/.env.example", "HOSTD_RELAY_GITHUB_CLIENT_ID=", "HOSTD_RELAY_WEBHOOK_SECRET=literal\nHOSTD_RELAY_GITHUB_CLIENT_ID=", "env_secret"},
		{"unknown env key", "deploy/relay/.env.example", "HOSTD_RELAY_GITHUB_CLIENT_ID=", "HOSTD_RELAY_UNREVIEWED=true\nHOSTD_RELAY_GITHUB_CLIENT_ID=", "env_defaults"},
	}
	for _, mutation := range mutations {
		t.Run("reject_"+mutation.name, func(t *testing.T) {
			mutated := clonePackagingFiles(files)
			if !strings.Contains(mutated[mutation.file], mutation.old) {
				t.Fatal("mutation anchor drifted")
			}
			mutated[mutation.file] = strings.Replace(mutated[mutation.file], mutation.old, mutation.replacement, 1)
			if !containsPackagingError(portablePackagingErrors(mutated), mutation.want) {
				t.Fatalf("mutation was not rejected with %s", mutation.want)
			}
		})
	}
}

func packagingRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate packaging test")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func clonePackagingFiles(files map[string]string) map[string]string {
	result := make(map[string]string, len(files))
	for name, content := range files {
		result[name] = content
	}
	return result
}

func containsPackagingError(errors []string, want string) bool {
	for _, value := range errors {
		if value == want {
			return true
		}
	}
	return false
}

func normalizePackagingText(content string) string {
	return strings.ReplaceAll(content, "\r\n", "\n")
}

func portablePackagingErrors(files map[string]string) []string {
	normalized := make(map[string]string, len(files))
	for name, content := range files {
		normalized[name] = normalizePackagingText(content)
	}
	files = normalized

	set := map[string]struct{}{}
	add := func(code string) { set[code] = struct{}{} }
	dockerfile := files["deploy/relay/Dockerfile"]
	if !strings.HasPrefix(dockerfile, "# syntax="+packagingFrontendRef+"\n") {
		add("docker_frontend_pin")
	}
	fromPattern := regexp.MustCompile(`(?m)^FROM\s+(?:--platform=\$BUILDPLATFORM\s+)?([^\s]+)`)
	from := fromPattern.FindAllStringSubmatch(dockerfile, -1)
	if len(from) != 2 || !packagingSHARef.MatchString(from[0][1]) || !packagingSHARef.MatchString(from[1][1]) || from[0][1] != packagingBuilderRef || from[1][1] != packagingRuntimeRef {
		add("docker_pins")
	}
	for _, required := range []string{
		"CGO_ENABLED=0", `GOOS="$TARGETOS" GOARCH="$TARGETARCH"`, "go build -mod=readonly -trimpath -buildvcs=false", `-buildid=`,
		"COPY --from=build --chmod=0555 /out/rig-relay ", "COPY --from=build --chmod=0555 /out/rig-relay-probe ",
		"USER 65532:65532", `ENTRYPOINT ["/usr/local/bin/rig-relay"]`, "org.opencontainers.image.source=",
	} {
		if !strings.Contains(dockerfile, required) {
			add("docker_build_contract")
		}
	}
	if regexp.MustCompile(`(?im)^\s*(ADD\s+|COPY\s+\.\s+)|\b(curl|wget|apt-get|apk)\b`).MatchString(dockerfile) {
		add("docker_context")
	}

	ignoreLines := effectivePackagingLines(files["deploy/relay/Dockerfile.dockerignore"])
	allowed := []string{
		"**", "!go.mod", "!go.sum", "!cmd/", "!cmd/rig-relay/", "!cmd/rig-relay/**",
		"!cmd/rig-relay-probe/", "!cmd/rig-relay-probe/**", "!internal/", "!internal/relay/", "!internal/relay/**",
	}
	if len(ignoreLines) != len(allowed) {
		add("context_allowlist")
	} else {
		for index := range allowed {
			if ignoreLines[index] != allowed[index] {
				add("context_allowlist")
			}
		}
	}

	if err := validateStaticCompose(files["deploy/relay/compose.yaml"]); err != nil {
		add("compose_contract")
	}
	if err := validateDirectOverlay(files["deploy/relay/compose.direct-tls.yaml"]); err != nil {
		add("direct_contract")
	}

	environment := files["deploy/relay/.env.example"]
	environmentKeys := map[string]struct{}{}
	for _, line := range effectivePackagingLines(environment) {
		name, _, ok := strings.Cut(line, "=")
		if !ok || name == "" {
			add("env_defaults")
			continue
		}
		environmentKeys[name] = struct{}{}
	}
	expectedEnvironmentKeys := []string{
		"HOSTD_RELAY_IMAGE", "HOSTD_RELAY_PUBLIC_BASE_URL", "HOSTD_RELAY_GITHUB_CLIENT_ID", "HOSTD_RELAY_GITHUB_APP_ID",
		"HOSTD_RELAY_TLS_SERVER_NAME", "HOSTD_RELAY_EDGE_NETWORK", "HOSTD_RELAY_SECRET_DIRECTORY", "HOSTD_RELAY_PUBLISH_ADDRESS", "HOSTD_RELAY_PUBLISH_PORT",
	}
	if len(environmentKeys) != len(expectedEnvironmentKeys) {
		add("env_defaults")
	} else {
		for _, key := range expectedEnvironmentKeys {
			if _, ok := environmentKeys[key]; !ok {
				add("env_defaults")
			}
		}
	}
	secretEnvironment := regexp.MustCompile(`^[A-Z0-9_]*(PASSWORD|SECRET|TOKEN|PRIVATE_KEY|WEBHOOK|ENROLLMENT_KEY|POSTGRES_DSN)[A-Z0-9_]*=`)
	for _, line := range strings.Split(strings.ReplaceAll(environment, "\r\n", "\n"), "\n") {
		if line != "HOSTD_RELAY_SECRET_DIRECTORY=/etc/rig-relay/secrets" && secretEnvironment.MatchString(line) {
			add("env_secret")
		}
	}
	if !strings.Contains(environment, "HOSTD_RELAY_IMAGE=registry.example.invalid/hostd/rig-relay@sha256:REPLACE_WITH_64_LOWERCASE_HEX_CHARACTERS") || !strings.Contains(environment, "HOSTD_RELAY_PUBLISH_ADDRESS=127.0.0.1") || !strings.Contains(environment, "HOSTD_RELAY_SECRET_DIRECTORY=/etc/rig-relay/secrets") {
		add("env_defaults")
	}
	if strings.Join(effectivePackagingLines(files["deploy/relay/secrets/.gitignore"]), "\n") != "*\n!.gitignore" {
		add("secrets_ignore")
	}

	docs := files["docs/relay-operations.md"]
	for _, required := range []string{
		"30 days", "7 days", "GitHub does not automatically redeliver failed webhook deliveries", "GitHub-connected deployments",
		"docker buildx build --file deploy/relay/Dockerfile", "scripts/check-relay-packaging.ps1 -SelfTest",
		"scripts/check-relay-packaging.ps1 -BehaviorTest", "HOSTD_RELAY_RUN_LINUX_PREFLIGHT_TESTS=1", "-LinuxIntegrationTest",
		"-TrustedDeploymentAnchor /etc/rig-relay", "-SecretDirectory /etc/rig-relay/secrets", "-DeploymentMode baseline", "-DeploymentMode direct-tls",
		"Docker Compose v2.30.0 or newer", "residual root/admin TOCTOU", "validated HTTPS", "SNI", "backup", "restore", "rotation", "rollback",
		"UID/GID `65532:65532`", "UID/GID `999:999`", "must contain no trailing CR or LF byte",
		"Windows Docker Desktop deployment with these file-backed secrets is unsupported",
		"Those fakes do not prove Linux `lstat`",
	} {
		if !strings.Contains(docs, required) {
			add("docs_contract")
		}
	}
	if !strings.Contains(files["Makefile"], "test-relay-probe:") || !strings.Contains(files["Makefile"], "check-relay-package:") {
		add("make_targets")
	}
	if !strings.Contains(files["README.md"], "docs/relay-operations.md") {
		add("readme_link")
	}
	result := make([]string, 0, len(set))
	for code := range set {
		result = append(result, code)
	}
	sort.Strings(result)
	return result
}

func effectivePackagingLines(text string) []string {
	result := []string{}
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			result = append(result, line)
		}
	}
	return result
}
