package main

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const packagingSecretDirectoryExpression = "${HOSTD_RELAY_SECRET_DIRECTORY:?set the absolute protected secret directory}"

func validateStaticCompose(document string) error {
	root, err := parseComposeYAML(document)
	if err != nil {
		return err
	}
	if err := requireExactKeys(root, "compose", "name", "services", "secrets", "volumes", "networks"); err != nil {
		return err
	}
	if stringValue(root["name"]) != "rig-relay" {
		return fmt.Errorf("unexpected project name")
	}
	services, ok := stringMap(root["services"])
	if !ok || !sameKeys(services, "postgres", "relay") {
		return fmt.Errorf("unexpected services")
	}
	postgres, ok := stringMap(services["postgres"])
	if !ok {
		return fmt.Errorf("postgres service is not a mapping")
	}
	relay, ok := stringMap(services["relay"])
	if !ok {
		return fmt.Errorf("relay service is not a mapping")
	}
	if err := validatePostgresService(postgres); err != nil {
		return fmt.Errorf("postgres: %w", err)
	}
	if err := validateRelayService(relay); err != nil {
		return fmt.Errorf("relay: %w", err)
	}
	if err := validateTopLevelSecrets(root["secrets"]); err != nil {
		return err
	}
	volumes, ok := stringMap(root["volumes"])
	if !ok || !sameKeys(volumes, "relay-postgres-data") {
		return fmt.Errorf("unexpected named volumes")
	}
	networks, ok := stringMap(root["networks"])
	if !ok || !sameKeys(networks, "relay-database", "relay-edge") {
		return fmt.Errorf("unexpected networks")
	}
	database, ok := stringMap(networks["relay-database"])
	if !ok || !mapExactly(database, map[string]any{"internal": true}) {
		return fmt.Errorf("database network contract changed")
	}
	edge, ok := stringMap(networks["relay-edge"])
	if !ok || !mapExactly(edge, map[string]any{"external": true, "name": "${HOSTD_RELAY_EDGE_NETWORK:-rig-relay-edge}"}) {
		return fmt.Errorf("edge network contract changed")
	}
	return nil
}

func validateDirectOverlay(document string) error {
	root, err := parseComposeYAML(document)
	if err != nil {
		return err
	}
	if !sameKeys(root, "services") {
		return fmt.Errorf("overlay contains unexpected top-level keys")
	}
	services, ok := stringMap(root["services"])
	if !ok || !sameKeys(services, "relay") {
		return fmt.Errorf("overlay contains unexpected services")
	}
	relay, ok := stringMap(services["relay"])
	if !ok || !sameKeys(relay, "ports") {
		return fmt.Errorf("overlay may only publish the relay port")
	}
	ports, ok := stringSlice(relay["ports"])
	if !ok || !reflect.DeepEqual(ports, []string{"${HOSTD_RELAY_PUBLISH_ADDRESS:-127.0.0.1}:${HOSTD_RELAY_PUBLISH_PORT:-7346}:7346"}) {
		return fmt.Errorf("overlay publication is not the exact loopback-default contract")
	}
	return nil
}

func validatePostgresService(service map[string]any) error {
	if err := requireExactKeys(service, "postgres service",
		"image", "user", "init", "restart", "read_only", "environment", "secrets", "volumes", "tmpfs", "shm_size",
		"healthcheck", "networks", "security_opt", "cap_drop", "pids_limit", "mem_limit", "cpus", "stop_grace_period", "logging"); err != nil {
		return err
	}
	if stringValue(service["image"]) != packagingPostgresRef || stringValue(service["user"]) != "999:999" {
		return fmt.Errorf("image or user changed")
	}
	if !boolValue(service["init"]) || !boolValue(service["read_only"]) || stringValue(service["restart"]) != "unless-stopped" {
		return fmt.Errorf("lifecycle hardening changed")
	}
	if err := validateCommonServiceControls(service, "1g", "1.5", "30s"); err != nil {
		return err
	}
	if !exactStringList(service["secrets"], "postgres_password") || !exactStringList(service["volumes"], "relay-postgres-data:/var/lib/postgresql") {
		return fmt.Errorf("secret or volume contract changed")
	}
	if !exactStringList(service["networks"], "relay-database") {
		return fmt.Errorf("network contract changed")
	}
	if !exactStringList(service["tmpfs"],
		"/tmp:rw,noexec,nosuid,nodev,size=32m,mode=1777,uid=999,gid=999",
		"/var/run/postgresql:rw,noexec,nosuid,nodev,size=16m,mode=0700,uid=999,gid=999") || stringValue(service["shm_size"]) != "128m" {
		return fmt.Errorf("temporary storage contract changed")
	}
	environment, ok := stringMap(service["environment"])
	if !ok || !mapExactly(environment, map[string]any{
		"POSTGRES_DB":            "rig_relay",
		"POSTGRES_USER":          "rig_relay",
		"POSTGRES_PASSWORD_FILE": "/run/secrets/postgres_password",
		"POSTGRES_INITDB_ARGS":   "--auth-host=scram-sha-256 --data-checksums",
	}) {
		return fmt.Errorf("environment contract changed")
	}
	health, ok := stringMap(service["healthcheck"])
	if !ok || !mapExactly(health, map[string]any{
		"test":         []any{"CMD-SHELL", "pg_isready -q -U rig_relay -d rig_relay"},
		"interval":     "10s",
		"timeout":      "5s",
		"retries":      10,
		"start_period": "30s",
	}) {
		return fmt.Errorf("healthcheck contract changed")
	}
	return nil
}

func validateRelayService(service map[string]any) error {
	if err := requireExactKeys(service, "relay service",
		"image", "user", "init", "restart", "read_only", "environment", "secrets", "depends_on", "healthcheck", "networks",
		"security_opt", "cap_drop", "pids_limit", "mem_limit", "cpus", "stop_grace_period", "logging"); err != nil {
		return err
	}
	if stringValue(service["image"]) != "${HOSTD_RELAY_IMAGE:?set HOSTD_RELAY_IMAGE to a verified registry/repository@sha256 digest}" || stringValue(service["user"]) != "65532:65532" {
		return fmt.Errorf("image or user changed")
	}
	if !boolValue(service["init"]) || !boolValue(service["read_only"]) || stringValue(service["restart"]) != "unless-stopped" {
		return fmt.Errorf("lifecycle hardening changed")
	}
	if err := validateCommonServiceControls(service, "512m", "1", "30s"); err != nil {
		return err
	}
	if !exactStringList(service["secrets"],
		"relay_postgres_dsn", "github_client_secret", "github_app_private_key", "github_webhook_secret", "enrollment_key",
		"relay_tls_certificate", "relay_tls_private_key", "relay_tls_ca") {
		return fmt.Errorf("secret contract changed")
	}
	if !exactStringList(service["networks"], "relay-database", "relay-edge") {
		return fmt.Errorf("network contract changed")
	}
	environment, ok := stringMap(service["environment"])
	if !ok || !mapExactly(environment, map[string]any{
		"HOSTD_RELAY_LISTEN_ADDRESS":            "0.0.0.0:7346",
		"HOSTD_RELAY_PUBLIC_BASE_URL":           "${HOSTD_RELAY_PUBLIC_BASE_URL:?set the public HTTPS origin}",
		"HOSTD_RELAY_LOOPBACK_DEVELOPMENT":      "false",
		"HOSTD_RELAY_GITHUB_CLIENT_ID":          "${HOSTD_RELAY_GITHUB_CLIENT_ID:?set the GitHub App client ID}",
		"HOSTD_RELAY_GITHUB_APP_ID":             "${HOSTD_RELAY_GITHUB_APP_ID:?set the GitHub App ID}",
		"HOSTD_RELAY_POSTGRES_DSN_FILE":         "/run/secrets/relay_postgres_dsn",
		"HOSTD_RELAY_GITHUB_CLIENT_SECRET_FILE": "/run/secrets/github_client_secret",
		"HOSTD_RELAY_GITHUB_PRIVATE_KEY_FILE":   "/run/secrets/github_app_private_key",
		"HOSTD_RELAY_WEBHOOK_SECRET_FILE":       "/run/secrets/github_webhook_secret",
		"HOSTD_RELAY_ENROLLMENT_KEY_FILE":       "/run/secrets/enrollment_key",
		"HOSTD_RELAY_TLS_CERTIFICATE_FILE":      "/run/secrets/relay_tls_certificate",
		"HOSTD_RELAY_TLS_PRIVATE_KEY_FILE":      "/run/secrets/relay_tls_private_key",
	}) {
		return fmt.Errorf("environment contract changed")
	}
	depends, ok := stringMap(service["depends_on"])
	if !ok || !sameKeys(depends, "postgres") {
		return fmt.Errorf("dependency contract changed")
	}
	postgres, ok := stringMap(depends["postgres"])
	if !ok || !mapExactly(postgres, map[string]any{"condition": "service_healthy", "restart": true}) {
		return fmt.Errorf("dependency contract changed")
	}
	health, ok := stringMap(service["healthcheck"])
	if !ok || !mapExactly(health, map[string]any{
		"test": []any{
			"CMD", "/usr/local/bin/rig-relay-probe", "--base-url=https://127.0.0.1:7346",
			"--server-name=${HOSTD_RELAY_TLS_SERVER_NAME:?set the certificate DNS name used for SNI}",
			"--ca-file=/run/secrets/relay_tls_ca", "--endpoint=ready", "--timeout=5s",
		},
		"interval":     "15s",
		"timeout":      "7s",
		"retries":      4,
		"start_period": "45s",
	}) {
		return fmt.Errorf("healthcheck contract changed")
	}
	return nil
}

func validateCommonServiceControls(service map[string]any, memory, cpus, grace string) error {
	if !exactStringList(service["security_opt"], "no-new-privileges:true") || !exactStringList(service["cap_drop"], "ALL") {
		return fmt.Errorf("privilege controls changed")
	}
	if intValue(service["pids_limit"]) != 256 || stringValue(service["mem_limit"]) != memory || numberString(service["cpus"]) != cpus || stringValue(service["stop_grace_period"]) != grace {
		return fmt.Errorf("resource controls changed")
	}
	logging, ok := stringMap(service["logging"])
	if !ok || !sameKeys(logging, "driver", "options") || stringValue(logging["driver"]) != "local" {
		return fmt.Errorf("logging contract changed")
	}
	options, ok := stringMap(logging["options"])
	if !ok || !mapExactly(options, map[string]any{"max-size": "10m", "max-file": "5", "compress": "true"}) {
		return fmt.Errorf("log rotation contract changed")
	}
	return nil
}

func validateTopLevelSecrets(value any) error {
	secrets, ok := stringMap(value)
	files := map[string]string{
		"postgres_password":      "postgres-password.txt",
		"relay_postgres_dsn":     "relay-postgres-dsn.txt",
		"github_client_secret":   "github-client-secret.txt",
		"github_app_private_key": "github-app-private-key.pem",
		"github_webhook_secret":  "github-webhook-secret.txt",
		"enrollment_key":         "enrollment-key.bin",
		"relay_tls_certificate":  "relay-tls-certificate.pem",
		"relay_tls_private_key":  "relay-tls-private-key.pem",
		"relay_tls_ca":           "relay-tls-ca.pem",
	}
	if !ok || len(secrets) != len(files) {
		return fmt.Errorf("top-level secret set changed")
	}
	for name, file := range files {
		definition, ok := stringMap(secrets[name])
		if !ok || !mapExactly(definition, map[string]any{"file": packagingSecretDirectoryExpression + "/" + file}) {
			return fmt.Errorf("secret %s source or remap changed", name)
		}
	}
	return nil
}

func parseComposeYAML(document string) (map[string]any, error) {
	var root map[string]any
	decoder := yaml.NewDecoder(strings.NewReader(document))
	if err := decoder.Decode(&root); err != nil {
		return nil, fmt.Errorf("invalid YAML: %w", err)
	}
	if len(root) == 0 {
		return nil, fmt.Errorf("empty YAML document")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return nil, fmt.Errorf("multiple YAML documents are not allowed")
	}
	return root, nil
}

func stringMap(value any) (map[string]any, bool) {
	result, ok := value.(map[string]any)
	return result, ok
}

func stringSlice(value any) ([]string, bool) {
	items, ok := value.([]any)
	if !ok {
		return nil, false
	}
	result := make([]string, len(items))
	for index, item := range items {
		text, ok := item.(string)
		if !ok {
			return nil, false
		}
		result[index] = text
	}
	return result, true
}

func exactStringList(value any, expected ...string) bool {
	actual, ok := stringSlice(value)
	return ok && reflect.DeepEqual(actual, expected)
}

func requireExactKeys(value map[string]any, label string, expected ...string) error {
	if !sameKeys(value, expected...) {
		actual := make([]string, 0, len(value))
		for key := range value {
			actual = append(actual, key)
		}
		sort.Strings(actual)
		return fmt.Errorf("%s keys changed: %s", label, strings.Join(actual, ","))
	}
	return nil
}

func sameKeys(value map[string]any, expected ...string) bool {
	if len(value) != len(expected) {
		return false
	}
	for _, key := range expected {
		if _, ok := value[key]; !ok {
			return false
		}
	}
	return true
}

func mapExactly(actual map[string]any, expected map[string]any) bool {
	return reflect.DeepEqual(actual, expected)
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func boolValue(value any) bool {
	result, _ := value.(bool)
	return result
}

func intValue(value any) int {
	result, _ := value.(int)
	return result
}

func numberString(value any) string {
	return fmt.Sprint(value)
}
