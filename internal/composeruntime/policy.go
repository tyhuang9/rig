package composeruntime

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/hostd/hostd/internal/pathsecurity"
)

const (
	PolicyVersion         = "compose-runtime-v1"
	MaxEffectiveJSONBytes = 4 << 20
	MaxPolicyFindings     = 2048
)

const (
	DispositionAllowed          = "allowed"
	DispositionApprovalRequired = "approval_required"
	DispositionRejected         = "rejected"
)

var supportedTopLevelFields = map[string]struct{}{
	"configs":  {},
	"include":  {},
	"name":     {},
	"networks": {},
	"secrets":  {},
	"services": {},
	"version":  {},
	"volumes":  {},
}

// supportedServiceFields contains fields that are either evaluated below or are
// known not to grant host capabilities. Unknown fields fail closed so a newer
// Compose feature cannot silently bypass this policy. Compose extension fields
// (x-*) are metadata and are the only schema-drift exception.
var supportedServiceFields = map[string]struct{}{
	"annotations": {}, "attach": {}, "blkio_config": {}, "build": {},
	"cap_add": {}, "cap_drop": {}, "cgroup": {}, "cgroup_parent": {},
	"command": {}, "configs": {}, "container_name": {}, "cpu_count": {},
	"cpu_percent": {}, "cpu_period": {}, "cpu_quota": {}, "cpu_rt_period": {},
	"cpu_rt_runtime": {}, "cpu_shares": {}, "cpus": {}, "cpuset": {},
	"credential_spec": {}, "depends_on": {}, "deploy": {}, "develop": {},
	"device_cgroup_rules": {}, "devices": {}, "dns": {}, "dns_opt": {},
	"dns_search": {}, "domainname": {}, "entrypoint": {}, "env_file": {},
	"environment": {}, "expose": {}, "extends": {}, "external_links": {},
	"extra_hosts": {}, "gpus": {}, "group_add": {}, "healthcheck": {},
	"hostname": {}, "image": {}, "init": {}, "ipc": {}, "isolation": {},
	"label_file": {}, "labels": {}, "links": {}, "logging": {},
	"mac_address": {}, "mem_limit": {}, "mem_reservation": {}, "mem_swappiness": {},
	"memswap_limit": {}, "models": {}, "network_mode": {}, "networks": {},
	"oom_kill_disable": {}, "oom_score_adj": {}, "pid": {}, "pids_limit": {},
	"platform": {}, "ports": {}, "post_start": {}, "pre_stop": {},
	"privileged": {}, "profiles": {}, "provider": {}, "pull_policy": {},
	"read_only": {}, "restart": {}, "runtime": {}, "scale": {}, "secrets": {},
	"security_opt": {}, "shm_size": {}, "stdin_open": {}, "stop_grace_period": {},
	"stop_signal": {}, "storage_opt": {}, "sysctls": {}, "tmpfs": {}, "tty": {},
	"ulimits": {}, "use_api_socket": {}, "user": {}, "userns_mode": {},
	"uts": {}, "volumes": {}, "volumes_from": {}, "working_dir": {},
}

var rejectedServiceFields = []string{
	"blkio_config", "credential_spec", "develop", "external_links", "extends",
	"include", "isolation", "label_file", "models", "pre_stop", "provider",
	"runtime", "storage_opt", "sysctls", "volumes_from", "cgroup_parent",
	"oom_kill_disable", "oom_score_adj",
}

var supportedBuildFields = map[string]struct{}{
	"additional_contexts": {}, "args": {}, "context": {}, "dockerfile": {},
	"entitlements": {}, "labels": {}, "network": {}, "no_cache": {},
	"platforms": {}, "privileged": {}, "pull": {}, "secrets": {}, "shm_size": {},
	"ssh": {}, "tags": {}, "target": {},
}

var allowedVolumeFields = map[string]struct{}{
	"bind": {}, "consistency": {}, "read_only": {}, "source": {}, "target": {},
	"tmpfs": {}, "type": {}, "volume": {},
}

var allowedPortFields = map[string]struct{}{
	"app_protocol": {}, "host_ip": {}, "mode": {}, "name": {}, "protocol": {},
	"published": {}, "target": {},
}

var allowedEnvFileFields = map[string]struct{}{
	"format": {}, "path": {}, "required": {},
}

type PolicyFinding struct {
	PolicyVersion string `json:"policyVersion"`
	Capability    string `json:"capability"`
	Scope         string `json:"scope"`
	Fingerprint   string `json:"fingerprint"`
	Disposition   string `json:"disposition"`
}

type PolicyError struct {
	Code string
}

func (e *PolicyError) Error() string { return "compose policy: " + e.Code }

type evaluator struct {
	workspace string
	findings  []PolicyFinding
	indices   map[string]int
	overflow  bool
}

func EvaluatePolicy(rendered []byte, workspace string) ([]PolicyFinding, error) {
	if len(rendered) == 0 || len(rendered) > MaxEffectiveJSONBytes {
		return nil, &PolicyError{Code: "model_too_large"}
	}
	if workspace == "" || pathsecurity.RejectWindowsNamespace(workspace) || !filepath.IsAbs(workspace) || filepath.Clean(workspace) != workspace {
		return nil, &PolicyError{Code: "invalid_workspace"}
	}
	root, err := canonicalPath(workspace)
	if err != nil {
		return nil, &PolicyError{Code: "invalid_workspace"}
	}
	var top map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(rendered))
	if err := dec.Decode(&top); err != nil || top == nil {
		return nil, &PolicyError{Code: "malformed_model"}
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return nil, &PolicyError{Code: "malformed_model"}
	}
	defer clearRawMap(top)
	servicesRaw, ok := top["services"]
	if !ok {
		return nil, &PolicyError{Code: "missing_services"}
	}
	var services map[string]json.RawMessage
	if err := json.Unmarshal(servicesRaw, &services); err != nil || services == nil {
		return nil, &PolicyError{Code: "malformed_services"}
	}
	defer clearRawMap(services)
	if len(services) == 0 || len(services) > 512 {
		return nil, &PolicyError{Code: "malformed_services"}
	}
	e := evaluator{workspace: root, indices: make(map[string]int)}
	for _, field := range sortedKeys(top) {
		if _, supported := supportedTopLevelFields[field]; !supported && !isExtensionField(field) {
			e.add("unsupported_top_level_field", map[string]any{"field": field}, DispositionRejected)
		}
	}
	for _, field := range []string{"include"} {
		if raw, exists := top[field]; exists && !isEmptyJSON(raw) {
			e.add("unsupported_"+field, map[string]any{"resource": field}, DispositionRejected)
		}
	}
	names := sortedKeys(services)
	for _, name := range names {
		var service map[string]json.RawMessage
		if err := json.Unmarshal(services[name], &service); err != nil || service == nil {
			return nil, &PolicyError{Code: "malformed_service"}
		}
		e.evaluateService(name, service)
		clearRawMap(service)
		if e.overflow {
			return nil, &PolicyError{Code: "too_many_findings"}
		}
	}
	e.externalResources(top)
	if e.overflow {
		return nil, &PolicyError{Code: "too_many_findings"}
	}
	sort.Slice(e.findings, func(i, j int) bool {
		a, b := e.findings[i], e.findings[j]
		if a.Capability != b.Capability {
			return a.Capability < b.Capability
		}
		if a.Scope != b.Scope {
			return a.Scope < b.Scope
		}
		if a.Disposition != b.Disposition {
			return a.Disposition < b.Disposition
		}
		return a.Fingerprint < b.Fingerprint
	})
	return e.findings, nil
}

func (e *evaluator) evaluateService(name string, service map[string]json.RawMessage) {
	for _, field := range sortedKeys(service) {
		if _, supported := supportedServiceFields[field]; !supported && !isExtensionField(field) {
			e.add("unsupported_service_field", map[string]any{"service": name, "field": field}, DispositionRejected)
		}
	}
	e.boolGate(name, service, "privileged", "privileged")
	e.boolGate(name, service, "use_api_socket", "docker_socket_api")
	for _, item := range []struct{ field, capability, value string }{
		{"pid", "host_pid_namespace", "host"}, {"ipc", "host_ipc_namespace", "host"},
		{"uts", "host_uts_namespace", "host"}, {"cgroup", "host_cgroup_namespace", "host"},
		{"network_mode", "host_network", "host"}, {"userns_mode", "host_userns_namespace", "host"},
	} {
		raw, exists := service[item.field]
		if !exists || isEmptyJSON(raw) {
			continue
		}
		v, ok := rawString(raw)
		if !ok {
			e.add("unsupported_"+item.field, map[string]any{"service": name}, DispositionRejected)
			continue
		}
		if strings.EqualFold(v, item.value) {
			e.add(item.capability, map[string]any{"service": name}, DispositionApprovalRequired)
		} else if !safeNamespaceValue(item.field, v) {
			e.add("unsupported_"+item.field, map[string]any{"service": name, "value": v}, DispositionRejected)
		}
	}
	e.stringListGate(name, service, "cap_add", "cap_add")
	e.stringListGate(name, service, "device_cgroup_rules", "device_cgroup_rule")
	e.securityOptions(name, service["security_opt"])
	e.devices(name, service["devices"])
	e.gpus(name, service["gpus"])
	e.build(name, service["build"])
	e.deploy(name, service["deploy"])
	e.postStart(name, service["post_start"])
	e.volumes(name, service["volumes"])
	e.ports(name, service["ports"])
	e.envFiles(name, service["env_file"])
	for _, field := range rejectedServiceFields {
		if raw, ok := service[field]; ok && !isEmptyJSON(raw) {
			e.add("unsupported_"+field, map[string]any{"service": name, "resource": field}, DispositionRejected)
		}
	}
}

func (e *evaluator) boolGate(service string, fields map[string]json.RawMessage, field, capability string) {
	var enabled bool
	raw, ok := fields[field]
	if !ok || isEmptyJSON(raw) {
		return
	}
	if json.Unmarshal(raw, &enabled) != nil {
		e.add("unsupported_"+field, map[string]any{"service": service}, DispositionRejected)
		return
	}
	if enabled {
		e.add(capability, map[string]any{"service": service}, DispositionApprovalRequired)
	}
}

func (e *evaluator) stringListGate(service string, fields map[string]json.RawMessage, field, capability string) {
	raw, ok := fields[field]
	if !ok || isEmptyJSON(raw) {
		return
	}
	var values []string
	if json.Unmarshal(raw, &values) != nil {
		e.add("unsupported_"+field, map[string]any{"service": service, "resource": field}, DispositionRejected)
		return
	}
	for _, value := range values {
		value = strings.ToUpper(strings.TrimSpace(value))
		valid := safeDeviceRule(value)
		if field == "cap_add" {
			valid = safeCapabilityValue(value)
		}
		if value == "" || !valid {
			e.add("unsupported_"+field, map[string]any{"service": service}, DispositionRejected)
			continue
		}
		e.add(capability, map[string]any{"service": service, "value": value}, DispositionApprovalRequired)
	}
}

func (e *evaluator) securityOptions(service string, raw json.RawMessage) {
	if len(raw) == 0 || isEmptyJSON(raw) {
		return
	}
	var values []string
	if json.Unmarshal(raw, &values) != nil {
		e.add("unsupported_security_opt", map[string]any{"service": service, "resource": "security_opt"}, DispositionRejected)
		return
	}
	for _, value := range values {
		lower := strings.ToLower(strings.TrimSpace(value))
		switch {
		case lower == "no-new-privileges:true" || lower == "no-new-privileges=true":
			// This tightens the container security boundary and needs no approval.
		case lower == "seccomp=unconfined" || lower == "seccomp:unconfined" ||
			lower == "apparmor=unconfined" || lower == "apparmor:unconfined" ||
			lower == "label=disable" || lower == "label:disable":
			e.add("unconfined_security_opt", map[string]any{"service": service, "option": lower}, DispositionApprovalRequired)
		default:
			e.add("unsupported_security_opt", map[string]any{"service": service}, DispositionRejected)
		}
	}
}

func (e *evaluator) devices(service string, raw json.RawMessage) {
	if len(raw) == 0 || isEmptyJSON(raw) {
		return
	}
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		e.add("unsupported_devices", map[string]any{"service": service, "resource": "devices"}, DispositionRejected)
		return
	}
	for i, item := range items {
		var value any
		if json.Unmarshal(item, &value) != nil {
			e.add("unsupported_devices", map[string]any{"service": service, "index": i}, DispositionRejected)
			continue
		}
		switch value.(type) {
		case string, map[string]any:
			e.add("device", map[string]any{"service": service, "resource": value}, DispositionApprovalRequired)
		default:
			e.add("unsupported_devices", map[string]any{"service": service, "index": i}, DispositionRejected)
		}
	}
}

func (e *evaluator) gpus(service string, raw json.RawMessage) {
	if len(raw) == 0 || isEmptyJSON(raw) {
		return
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		e.add("unsupported_gpus", map[string]any{"service": service, "resource": "gpus"}, DispositionRejected)
		return
	}
	switch typed := value.(type) {
	case string:
		if !strings.EqualFold(typed, "all") {
			e.add("unsupported_gpus", map[string]any{"service": service, "resource": "gpus"}, DispositionRejected)
			return
		}
		e.add("gpu", map[string]any{"service": service, "resource": value}, DispositionApprovalRequired)
	case []any:
		for _, item := range typed {
			if _, ok := item.(map[string]any); !ok {
				e.add("unsupported_gpus", map[string]any{"service": service, "resource": "gpus"}, DispositionRejected)
				return
			}
		}
		e.add("gpu", map[string]any{"service": service, "resource": value}, DispositionApprovalRequired)
	default:
		e.add("unsupported_gpus", map[string]any{"service": service, "resource": "gpus"}, DispositionRejected)
	}
}

func (e *evaluator) build(service string, raw json.RawMessage) {
	if len(raw) == 0 || isEmptyJSON(raw) {
		return
	}
	var context string
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &context) != nil {
		if json.Unmarshal(raw, &obj) != nil || obj == nil {
			e.add("unsupported_build", map[string]any{"service": service}, DispositionRejected)
			return
		}
		for _, field := range sortedKeys(obj) {
			if _, supported := supportedBuildFields[field]; !supported && !isExtensionField(field) {
				e.add("unsupported_build_field", map[string]any{"service": service, "field": field}, DispositionRejected)
			}
		}
		e.validateBuildFieldTypes(service, obj)
		e.boolGate(service, obj, "privileged", "build_privileged")
		if network, exists := obj["network"]; exists && !isEmptyJSON(network) {
			value, ok := rawString(network)
			switch {
			case !ok:
				e.add("unsupported_build_network", map[string]any{"service": service}, DispositionRejected)
			case strings.EqualFold(value, "host"):
				e.add("build_host_network", map[string]any{"service": service}, DispositionApprovalRequired)
			case value != "" && !strings.EqualFold(value, "default") && !strings.EqualFold(value, "none"):
				e.add("unsupported_build_network", map[string]any{"service": service}, DispositionRejected)
			}
		}
		if entitlements, exists := obj["entitlements"]; exists && !isEmptyJSON(entitlements) {
			e.buildEntitlements(service, entitlements)
		}
		for _, field := range []string{"ssh", "secrets"} {
			if value, exists := obj[field]; exists && !isEmptyJSON(value) {
				e.add("unsupported_build_"+field, map[string]any{"service": service, "resource": field}, DispositionRejected)
			}
		}
		if contextRaw, exists := obj["context"]; exists && !isEmptyJSON(contextRaw) {
			var ok bool
			context, ok = rawString(contextRaw)
			if !ok {
				e.add("unsupported_build", map[string]any{"service": service}, DispositionRejected)
				return
			}
		}
		if extra, ok := obj["additional_contexts"]; ok && !isEmptyJSON(extra) {
			e.additionalContexts(service, extra)
		}
		if dockerfile, exists := obj["dockerfile"]; exists && !isEmptyJSON(dockerfile) {
			value, ok := rawString(dockerfile)
			if !ok {
				e.add("unsupported_dockerfile", map[string]any{"service": service}, DispositionRejected)
			} else {
				e.dockerfile(service, context, value)
			}
		}
	}
	if context == "" {
		context = "."
	}
	e.pathResource(service, "build_context", context, "", "")
}

func (e *evaluator) validateBuildFieldTypes(service string, fields map[string]json.RawMessage) {
	for _, field := range []string{"args", "labels"} {
		raw, exists := fields[field]
		if exists && !isEmptyJSON(raw) && !validStringMapOrList(raw) {
			e.add("unsupported_build_"+field, map[string]any{"service": service}, DispositionRejected)
		}
	}
	for _, field := range []string{"no_cache", "pull"} {
		raw, exists := fields[field]
		if exists && !isEmptyJSON(raw) && !validBool(raw) {
			e.add("unsupported_build_"+field, map[string]any{"service": service}, DispositionRejected)
		}
	}
	for _, field := range []string{"platforms", "tags"} {
		raw, exists := fields[field]
		if exists && !isEmptyJSON(raw) && !validStringList(raw) {
			e.add("unsupported_build_"+field, map[string]any{"service": service}, DispositionRejected)
		}
	}
	for _, field := range []string{"target", "shm_size"} {
		raw, exists := fields[field]
		if exists && !isEmptyJSON(raw) && !validStringOrNumber(raw) {
			e.add("unsupported_build_"+field, map[string]any{"service": service}, DispositionRejected)
		}
	}
}

func (e *evaluator) buildEntitlements(service string, raw json.RawMessage) {
	var values []string
	if json.Unmarshal(raw, &values) != nil {
		e.add("unsupported_build_entitlements", map[string]any{"service": service}, DispositionRejected)
		return
	}
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		switch value {
		case "network.host", "security.insecure":
			e.add("build_entitlement", map[string]any{"service": service, "entitlement": value}, DispositionApprovalRequired)
		default:
			e.add("unsupported_build_entitlement", map[string]any{"service": service}, DispositionRejected)
		}
	}
}

func (e *evaluator) postStart(service string, raw json.RawMessage) {
	if len(raw) == 0 || isEmptyJSON(raw) {
		return
	}
	var hooks []map[string]json.RawMessage
	if json.Unmarshal(raw, &hooks) != nil {
		e.add("unsupported_post_start", map[string]any{"service": service}, DispositionRejected)
		return
	}
	allowed := map[string]struct{}{"command": {}, "environment": {}, "privileged": {}, "user": {}, "working_dir": {}}
	for index, hook := range hooks {
		for _, field := range sortedKeys(hook) {
			if _, ok := allowed[field]; !ok && !isExtensionField(field) {
				e.add("unsupported_post_start_field", map[string]any{"service": service, "index": index, "field": field}, DispositionRejected)
			}
		}
		if command, exists := hook["command"]; exists && !isEmptyJSON(command) && !validStringOrStringList(command) {
			e.add("unsupported_post_start_command", map[string]any{"service": service, "index": index}, DispositionRejected)
		}
		if environment, exists := hook["environment"]; exists && !isEmptyJSON(environment) && !validStringMapOrList(environment) {
			e.add("unsupported_post_start_environment", map[string]any{"service": service, "index": index}, DispositionRejected)
		}
		for _, field := range []string{"user", "working_dir"} {
			if value, exists := hook[field]; exists && !isEmptyJSON(value) {
				if _, ok := rawString(value); !ok {
					e.add("unsupported_post_start_"+field, map[string]any{"service": service, "index": index}, DispositionRejected)
				}
			}
		}
		if privileged, exists := hook["privileged"]; exists && !isEmptyJSON(privileged) {
			var enabled bool
			if json.Unmarshal(privileged, &enabled) != nil {
				e.add("unsupported_post_start_privileged", map[string]any{"service": service, "index": index}, DispositionRejected)
			} else if enabled {
				e.add("post_start_privileged", map[string]any{"service": service, "index": index}, DispositionApprovalRequired)
			}
		}
		clearRawMap(hook)
	}
}

func (e *evaluator) deploy(service string, raw json.RawMessage) {
	if len(raw) == 0 || isEmptyJSON(raw) {
		return
	}
	var deploy map[string]json.RawMessage
	if json.Unmarshal(raw, &deploy) != nil || deploy == nil {
		e.add("unsupported_deploy", map[string]any{"service": service}, DispositionRejected)
		return
	}
	defer clearRawMap(deploy)
	allowed := map[string]struct{}{
		"endpoint_mode": {}, "labels": {}, "mode": {}, "placement": {}, "replicas": {},
		"resources": {}, "restart_policy": {}, "rollback_config": {}, "update_config": {},
	}
	for _, field := range sortedKeys(deploy) {
		if _, ok := allowed[field]; !ok && !isExtensionField(field) {
			e.add("unsupported_deploy_field", map[string]any{"service": service, "field": field}, DispositionRejected)
		}
	}
	for _, field := range []string{"placement", "restart_policy", "rollback_config", "update_config"} {
		if value, exists := deploy[field]; exists && !isEmptyJSON(value) {
			e.add("unsupported_deploy_"+field, map[string]any{"service": service}, DispositionRejected)
		}
	}
	resourcesRaw, exists := deploy["resources"]
	if !exists || isEmptyJSON(resourcesRaw) {
		return
	}
	var resources map[string]json.RawMessage
	if json.Unmarshal(resourcesRaw, &resources) != nil || resources == nil {
		e.add("unsupported_deploy_resources", map[string]any{"service": service}, DispositionRejected)
		return
	}
	defer clearRawMap(resources)
	for _, field := range sortedKeys(resources) {
		if field != "limits" && field != "reservations" && !isExtensionField(field) {
			e.add("unsupported_deploy_resource_field", map[string]any{"service": service, "field": field}, DispositionRejected)
		}
	}
	if limitsRaw, exists := resources["limits"]; exists && !isEmptyJSON(limitsRaw) {
		e.deployResourceLimit(service, "limits", limitsRaw)
	}
	reservationsRaw, exists := resources["reservations"]
	if !exists || isEmptyJSON(reservationsRaw) {
		return
	}
	var reservations map[string]json.RawMessage
	if json.Unmarshal(reservationsRaw, &reservations) != nil || reservations == nil {
		e.add("unsupported_deploy_reservations", map[string]any{"service": service}, DispositionRejected)
		return
	}
	defer clearRawMap(reservations)
	for _, field := range sortedKeys(reservations) {
		if field != "cpus" && field != "memory" && field != "pids" && field != "generic_resources" && field != "devices" && !isExtensionField(field) {
			e.add("unsupported_deploy_reservation_field", map[string]any{"service": service, "field": field}, DispositionRejected)
		}
	}
	for _, field := range []string{"cpus", "memory", "pids"} {
		if value, exists := reservations[field]; exists && !isEmptyJSON(value) && !validStringOrNumber(value) {
			e.add("unsupported_deploy_reservation_"+field, map[string]any{"service": service}, DispositionRejected)
		}
	}
	if generic, exists := reservations["generic_resources"]; exists && !isEmptyJSON(generic) {
		e.add("unsupported_deploy_generic_resources", map[string]any{"service": service}, DispositionRejected)
	}
	devicesRaw, exists := reservations["devices"]
	if !exists || isEmptyJSON(devicesRaw) {
		return
	}
	var devices []json.RawMessage
	if json.Unmarshal(devicesRaw, &devices) != nil {
		e.add("unsupported_deploy_reservation_devices", map[string]any{"service": service}, DispositionRejected)
		return
	}
	for index, device := range devices {
		var object map[string]json.RawMessage
		if json.Unmarshal(device, &object) != nil || object == nil {
			e.add("unsupported_deploy_reservation_device", map[string]any{"service": service, "index": index}, DispositionRejected)
			continue
		}
		valid := true
		for _, field := range sortedKeys(object) {
			switch field {
			case "capabilities", "count", "device_ids", "driver", "options":
			default:
				if !isExtensionField(field) {
					valid = false
					e.add("unsupported_deploy_reservation_device_field", map[string]any{"service": service, "index": index, "field": field}, DispositionRejected)
				}
			}
		}
		if valid && !validDeployDevice(object) {
			valid = false
			e.add("unsupported_deploy_reservation_device", map[string]any{"service": service, "index": index}, DispositionRejected)
		}
		if valid {
			var normalized any
			if json.Unmarshal(device, &normalized) != nil {
				e.add("unsupported_deploy_reservation_device", map[string]any{"service": service, "index": index}, DispositionRejected)
			} else {
				e.add("deploy_reservation_device", map[string]any{"service": service, "index": index, "device": normalized}, DispositionApprovalRequired)
			}
		}
		clearRawMap(object)
	}
}

func (e *evaluator) deployResourceLimit(service, kind string, raw json.RawMessage) {
	var values map[string]json.RawMessage
	if json.Unmarshal(raw, &values) != nil || values == nil {
		e.add("unsupported_deploy_"+kind, map[string]any{"service": service}, DispositionRejected)
		return
	}
	defer clearRawMap(values)
	for _, field := range sortedKeys(values) {
		if field != "cpus" && field != "memory" && field != "pids" && !isExtensionField(field) {
			e.add("unsupported_deploy_"+kind+"_field", map[string]any{"service": service, "field": field}, DispositionRejected)
			continue
		}
		if !isExtensionField(field) && !validStringOrNumber(values[field]) {
			e.add("unsupported_deploy_"+kind+"_"+field, map[string]any{"service": service}, DispositionRejected)
		}
	}
}

func (e *evaluator) dockerfile(service, contextValue, value string) {
	if contextValue == "" {
		contextValue = "."
	}
	if value == "" || filepath.IsAbs(value) || isRemoteContext(value) {
		e.add("invalid_dockerfile", map[string]any{"service": service, "path": value}, DispositionRejected)
		return
	}
	contextPath := contextValue
	if !filepath.IsAbs(contextPath) {
		contextPath = filepath.Join(e.workspace, contextPath)
	}
	contextPath, err := canonicalPath(contextPath)
	if err != nil || !pathWithin(e.workspace, contextPath) {
		e.add("invalid_dockerfile", map[string]any{"service": service, "path": value}, DispositionRejected)
		return
	}
	candidate, err := canonicalPath(filepath.Join(contextPath, value))
	if err != nil || !pathWithin(e.workspace, candidate) {
		e.add("invalid_dockerfile", map[string]any{"service": service, "path": value}, DispositionRejected)
		return
	}
	e.add("workspace_dockerfile", map[string]any{"service": service, "path": candidate}, DispositionAllowed)
}

func (e *evaluator) additionalContexts(service string, raw json.RawMessage) {
	var contexts map[string]json.RawMessage
	if json.Unmarshal(raw, &contexts) != nil || contexts == nil {
		e.add("unsupported_additional_context", map[string]any{"service": service}, DispositionRejected)
		return
	}
	for _, name := range sortedKeys(contexts) {
		value, ok := rawString(contexts[name])
		if !ok {
			e.add("unsupported_additional_context", map[string]any{"service": service, "name": name}, DispositionRejected)
			continue
		}
		if isRemoteContext(value) {
			e.add("remote_additional_context", map[string]any{"service": service, "name": name}, DispositionRejected)
			continue
		}
		e.pathResource(service, "additional_context", value, name, "")
	}
}

func (e *evaluator) volumes(service string, raw json.RawMessage) {
	if len(raw) == 0 || isEmptyJSON(raw) {
		return
	}
	var volumes []json.RawMessage
	if json.Unmarshal(raw, &volumes) != nil {
		e.add("unsupported_volume", map[string]any{"service": service}, DispositionRejected)
		return
	}
	for i, rawVolume := range volumes {
		var volume map[string]json.RawMessage
		if json.Unmarshal(rawVolume, &volume) != nil || volume == nil {
			e.add("unsupported_volume", map[string]any{"service": service, "index": i}, DispositionRejected)
			continue
		}
		validSyntax := true
		for _, field := range sortedKeys(volume) {
			if _, allowed := allowedVolumeFields[field]; !allowed && !isExtensionField(field) {
				e.add("unsupported_volume_field", map[string]any{"service": service, "index": i, "field": field}, DispositionRejected)
				validSyntax = false
			}
		}
		for _, field := range []string{"bind", "tmpfs", "volume"} {
			if nested, exists := volume[field]; exists && !isEmptyJSON(nested) {
				e.add("unsupported_volume_"+field+"_options", map[string]any{"service": service, "index": i}, DispositionRejected)
				validSyntax = false
			}
		}
		if consistency, exists := volume["consistency"]; exists && !isEmptyJSON(consistency) {
			if _, ok := rawString(consistency); !ok {
				e.add("unsupported_volume_consistency", map[string]any{"service": service, "index": i}, DispositionRejected)
				validSyntax = false
			}
		}
		if !validSyntax {
			clearRawMap(volume)
			continue
		}
		typeName, _ := rawString(volume["type"])
		source, sourceOK := rawString(volume["source"])
		target, targetOK := rawString(volume["target"])
		readOnly := false
		if readOnlyRaw, exists := volume["read_only"]; exists && !isEmptyJSON(readOnlyRaw) {
			if json.Unmarshal(readOnlyRaw, &readOnly) != nil {
				e.add("unsupported_volume", map[string]any{"service": service, "index": i}, DispositionRejected)
				continue
			}
		}
		mode := "rw"
		if readOnly {
			mode = "ro"
		}
		switch typeName {
		case "volume":
			if !sourceOK || !targetOK {
				e.add("unsupported_volume", map[string]any{"service": service, "index": i}, DispositionRejected)
				continue
			}
			e.add("named_volume", map[string]any{"service": service, "source": source, "target": target, "mode": mode}, DispositionAllowed)
		case "bind":
			if !sourceOK || !targetOK {
				e.add("unsupported_volume", map[string]any{"service": service, "index": i}, DispositionRejected)
				continue
			}
			e.pathResource(service, "bind_mount", source, target, mode)
		case "tmpfs":
			if !targetOK {
				e.add("unsupported_volume", map[string]any{"service": service, "index": i}, DispositionRejected)
				continue
			}
			e.add("tmpfs", map[string]any{"service": service, "target": target}, DispositionAllowed)
		default:
			e.add("unsupported_volume", map[string]any{"service": service, "index": i}, DispositionRejected)
		}
	}
}

func (e *evaluator) pathResource(service, kind, source, target, mode string) {
	if isRemoteContext(source) {
		e.add("remote_"+kind, map[string]any{"service": service}, DispositionRejected)
		return
	}
	abs := source
	relative := !filepath.IsAbs(source)
	if relative {
		abs = filepath.Join(e.workspace, source)
	}
	if kind == "bind_mount" && isDockerEndpoint(source, target) {
		scope := map[string]any{"service": service, "source": normalizePath(source), "target": target, "mode": mode}
		e.add("docker_socket_mount", scope, DispositionApprovalRequired)
		return
	}
	canonical, err := canonicalPath(abs)
	if err != nil {
		capability := "invalid_" + kind
		if relative {
			capability = "workspace_escape"
		}
		e.add(capability, map[string]any{"service": service, "source": filepath.Clean(abs)}, DispositionRejected)
		return
	}
	inside := pathWithin(e.workspace, canonical)
	scope := map[string]any{"service": service, "source": canonical}
	if target != "" {
		scope["target"] = target
		scope["mode"] = mode
	}
	if !inside && relative {
		e.add("workspace_escape", scope, DispositionRejected)
		return
	}
	if kind == "bind_mount" && isDockerEndpoint(canonical, target) {
		e.add("docker_socket_mount", scope, DispositionApprovalRequired)
		return
	}
	if inside {
		if kind == "bind_mount" && mode != "ro" {
			e.add("writable_managed_bind", scope, DispositionRejected)
			return
		}
		e.add("workspace_"+kind, scope, DispositionAllowed)
		return
	}
	if kind != "bind_mount" {
		e.add("external_"+kind, scope, DispositionRejected)
		return
	}
	e.add("external_bind", scope, DispositionApprovalRequired)
}

func (e *evaluator) externalResources(top map[string]json.RawMessage) {
	for _, group := range []struct{ field, capability string }{{"volumes", "external_volume"}, {"configs", "external_config"}, {"secrets", "external_secret"}, {"networks", "external_network"}} {
		raw, exists := top[group.field]
		if !exists || isEmptyJSON(raw) {
			continue
		}
		var resources map[string]json.RawMessage
		if json.Unmarshal(raw, &resources) != nil || resources == nil {
			e.add("unsupported_"+group.field, map[string]any{"resource": group.field}, DispositionRejected)
			continue
		}
		for _, name := range sortedKeys(resources) {
			definition := resources[name]
			if isEmptyJSON(definition) {
				continue
			}
			var object map[string]json.RawMessage
			if json.Unmarshal(definition, &object) != nil || object == nil {
				e.add("unsupported_"+group.field, map[string]any{"resource": name}, DispositionRejected)
				continue
			}
			external, valid := externalResourceFlag(object["external"])
			if !valid {
				e.add("unsupported_"+group.field, map[string]any{"resource": name}, DispositionRejected)
				continue
			}
			if external {
				e.add(group.capability, map[string]any{"resource": name}, DispositionRejected)
			}
			if (group.field == "configs" || group.field == "secrets") && !external {
				if file, exists := object["file"]; exists && !isEmptyJSON(file) {
					value, ok := rawString(file)
					if !ok {
						e.add("unsupported_"+group.field, map[string]any{"resource": name}, DispositionRejected)
					} else {
						e.workspaceFile("resource:"+name, group.field+"_file", value)
					}
				}
			}
			if (group.field == "volumes" || group.field == "networks") && !external {
				if driver, exists := object["driver"]; exists && !isEmptyJSON(driver) {
					value, ok := rawString(driver)
					allowed := group.field == "volumes" && value == "local" || group.field == "networks" && value == "bridge"
					if !ok || !allowed {
						e.add("remote_"+strings.TrimSuffix(group.field, "s")+"_driver", map[string]any{"resource": name}, DispositionRejected)
					}
				}
				if options, exists := object["driver_opts"]; exists && !isEmptyJSON(options) {
					e.add("remote_"+strings.TrimSuffix(group.field, "s")+"_options", map[string]any{"resource": name}, DispositionRejected)
				}
			}
			clearRawMap(object)
		}
		clearRawMap(resources)
	}
}

func externalResourceFlag(raw json.RawMessage) (bool, bool) {
	if len(raw) == 0 || isEmptyJSON(raw) {
		return false, true
	}
	var value bool
	if json.Unmarshal(raw, &value) == nil {
		return value, true
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) == nil && object != nil {
		clearRawMap(object)
		return true, true
	}
	return false, false
}

func (e *evaluator) ports(service string, raw json.RawMessage) {
	if len(raw) == 0 || isEmptyJSON(raw) {
		return
	}
	var ports []json.RawMessage
	if json.Unmarshal(raw, &ports) != nil {
		e.add("unsupported_port", map[string]any{"service": service}, DispositionRejected)
		return
	}
	for i, rawPort := range ports {
		var port map[string]json.RawMessage
		if json.Unmarshal(rawPort, &port) != nil || port == nil {
			e.add("unsupported_port", map[string]any{"service": service, "index": i}, DispositionRejected)
			continue
		}
		validSyntax := true
		for _, field := range sortedKeys(port) {
			if _, allowed := allowedPortFields[field]; !allowed && !isExtensionField(field) {
				e.add("unsupported_port_field", map[string]any{"service": service, "index": i, "field": field}, DispositionRejected)
				validSyntax = false
			}
		}
		for _, field := range []string{"app_protocol", "host_ip", "mode", "name", "protocol"} {
			if value, exists := port[field]; exists && !isEmptyJSON(value) {
				if _, ok := rawString(value); !ok {
					e.add("unsupported_port_"+field, map[string]any{"service": service, "index": i}, DispositionRejected)
					validSyntax = false
				}
			}
		}
		if !validSyntax {
			clearRawMap(port)
			continue
		}
		target, okTarget := numberString(port["target"])
		published, okPublished := numberString(port["published"])
		if !okTarget {
			e.add("unsupported_port", map[string]any{"service": service, "index": i}, DispositionRejected)
			continue
		}
		if !okPublished {
			if _, exists := port["published"]; !exists || isEmptyJSON(port["published"]) {
				continue
			}
			e.add("unsupported_port", map[string]any{"service": service, "index": i}, DispositionRejected)
			continue
		}
		hostIP, hostOK := rawString(port["host_ip"])
		if raw, exists := port["host_ip"]; exists && !isEmptyJSON(raw) && !hostOK {
			e.add("unsupported_port", map[string]any{"service": service, "index": i}, DispositionRejected)
			continue
		}
		protocol, protocolOK := rawString(port["protocol"])
		if raw, exists := port["protocol"]; exists && !isEmptyJSON(raw) && !protocolOK {
			e.add("unsupported_port", map[string]any{"service": service, "index": i}, DispositionRejected)
			continue
		}
		if protocol == "" {
			protocol = "tcp"
		}
		scope := map[string]any{"service": service, "host_ip": canonicalHostIP(hostIP), "published": published, "target": target, "protocol": strings.ToLower(protocol)}
		if isLoopback(hostIP) {
			e.add("loopback_published_port", scope, DispositionAllowed)
		} else {
			e.add("published_port", scope, DispositionApprovalRequired)
		}
	}
}

func (e *evaluator) envFiles(service string, raw json.RawMessage) {
	if len(raw) == 0 || isEmptyJSON(raw) {
		return
	}
	var values []json.RawMessage
	if bytes.HasPrefix(bytes.TrimSpace(raw), []byte("[")) {
		if json.Unmarshal(raw, &values) != nil {
			e.add("unsupported_env_file", map[string]any{"service": service}, DispositionRejected)
			return
		}
	} else {
		values = []json.RawMessage{raw}
	}
	for index, item := range values {
		path, ok := rawString(item)
		if !ok {
			var object map[string]json.RawMessage
			if json.Unmarshal(item, &object) != nil || object == nil {
				e.add("unsupported_env_file", map[string]any{"service": service, "index": index}, DispositionRejected)
				continue
			}
			validSyntax := true
			for _, field := range sortedKeys(object) {
				if _, allowed := allowedEnvFileFields[field]; !allowed && !isExtensionField(field) {
					e.add("unsupported_env_file_field", map[string]any{"service": service, "index": index, "field": field}, DispositionRejected)
					validSyntax = false
				}
			}
			if required, exists := object["required"]; exists && !isEmptyJSON(required) && !validBool(required) {
				e.add("unsupported_env_file_required", map[string]any{"service": service, "index": index}, DispositionRejected)
				validSyntax = false
			}
			if format, exists := object["format"]; exists && !isEmptyJSON(format) {
				if _, valid := rawString(format); !valid {
					e.add("unsupported_env_file_format", map[string]any{"service": service, "index": index}, DispositionRejected)
					validSyntax = false
				}
			}
			path, ok = rawString(object["path"])
			clearRawMap(object)
			if !validSyntax {
				continue
			}
		}
		if !ok {
			e.add("unsupported_env_file", map[string]any{"service": service, "index": index}, DispositionRejected)
			continue
		}
		e.workspaceFile(service, "env_file", path)
	}
}

func (e *evaluator) workspaceFile(service, capability, value string) {
	if value == "" || isRemoteContext(value) {
		e.add("invalid_"+capability, map[string]any{"service": service, "path": value}, DispositionRejected)
		return
	}
	candidate := value
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(e.workspace, candidate)
	}
	canonical, err := canonicalPath(candidate)
	if err != nil || !pathWithin(e.workspace, canonical) {
		e.add("invalid_"+capability, map[string]any{"service": service, "path": value}, DispositionRejected)
		return
	}
	e.add("workspace_"+capability, map[string]any{"service": service, "path": canonical}, DispositionAllowed)
}

func (e *evaluator) add(capability string, scope any, disposition string) {
	encoded, err := json.Marshal(scope)
	if err != nil {
		panic(err)
	}
	fingerprint := PolicyFingerprint(PolicyVersion, capability, string(encoded))
	finding := PolicyFinding{PolicyVersion: PolicyVersion, Capability: capability, Scope: string(encoded), Fingerprint: fingerprint, Disposition: disposition}
	if index, exists := e.indices[finding.Fingerprint]; exists {
		if dispositionRank(disposition) > dispositionRank(e.findings[index].Disposition) {
			e.findings[index] = finding
		}
		return
	}
	if len(e.findings) >= MaxPolicyFindings {
		e.overflow = true
		return
	}
	e.indices[finding.Fingerprint] = len(e.findings)
	e.findings = append(e.findings, finding)
}

func PolicyFingerprint(policyVersion, capability, scope string) string {
	fingerprintInput, err := json.Marshal(struct {
		PolicyVersion string `json:"policyVersion"`
		Capability    string `json:"capability"`
		Scope         string `json:"scope"`
	}{policyVersion, capability, scope})
	if err != nil {
		panic(err)
	}
	sum := sha256.Sum256(fingerprintInput)
	return hex.EncodeToString(sum[:])
}

func canonicalPath(path string) (string, error) {
	if pathsecurity.RejectWindowsNamespace(path) {
		return "", errors.New("unsafe path namespace")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)
	for probe := abs; ; probe = filepath.Dir(probe) {
		info, statErr := os.Lstat(probe)
		if statErr != nil || info.Mode()&os.ModeSymlink != 0 || policyPathIsReparsePoint(probe) {
			return "", errors.New("unsafe path ancestor")
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			break
		}
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	return normalizePath(filepath.Clean(resolved)), nil
}

func normalizePath(path string) string {
	path = filepath.Clean(path)
	if filepath.Separator == '\\' {
		path = strings.ToLower(path)
	}
	return path
}

func pathWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func isRemoteContext(value string) bool {
	lower := strings.ToLower(strings.TrimSpace(value))
	return strings.Contains(lower, "://") || strings.HasPrefix(lower, "git@") || strings.HasPrefix(lower, "docker-image://") || strings.HasPrefix(lower, "service:")
}

func isExtensionField(field string) bool {
	return strings.HasPrefix(strings.ToLower(field), "x-")
}

func isDockerEndpoint(source, target string) bool {
	for _, value := range []string{source, target} {
		v := strings.ToLower(strings.ReplaceAll(value, "\\", "/"))
		if strings.Contains(v, "docker_engine") || strings.HasSuffix(v, "/docker.sock") || strings.HasSuffix(v, "/docker.socket") {
			return true
		}
	}
	return false
}

func canonicalHostIP(value string) string {
	if strings.TrimSpace(value) == "" {
		return "unspecified"
	}
	ip := net.ParseIP(strings.Trim(value, "[]"))
	if ip == nil {
		return strings.ToLower(value)
	}
	return ip.String()
}

func isLoopback(value string) bool {
	ip := net.ParseIP(strings.Trim(value, "[]"))
	return ip != nil && ip.IsLoopback()
}

func rawString(raw json.RawMessage) (string, bool) {
	var value string
	err := json.Unmarshal(raw, &value)
	return value, err == nil
}

func rawBool(raw json.RawMessage) bool {
	var value bool
	return json.Unmarshal(raw, &value) == nil && value
}

func validBool(raw json.RawMessage) bool {
	var value bool
	return json.Unmarshal(raw, &value) == nil
}

func validStringList(raw json.RawMessage) bool {
	var values []string
	return json.Unmarshal(raw, &values) == nil
}

func validStringOrStringList(raw json.RawMessage) bool {
	if _, ok := rawString(raw); ok {
		return true
	}
	return validStringList(raw)
}

func validStringOrNumber(raw json.RawMessage) bool {
	if _, ok := rawString(raw); ok {
		return true
	}
	var value json.Number
	return json.Unmarshal(raw, &value) == nil
}

func validStringMapOrList(raw json.RawMessage) bool {
	if validStringList(raw) {
		return true
	}
	var values map[string]json.RawMessage
	if json.Unmarshal(raw, &values) != nil || values == nil {
		return false
	}
	defer clearRawMap(values)
	for _, value := range values {
		if isEmptyJSON(value) {
			continue
		}
		if _, ok := rawString(value); !ok {
			return false
		}
	}
	return true
}

func validDeployDevice(fields map[string]json.RawMessage) bool {
	capabilities, exists := fields["capabilities"]
	if !exists || !validNonemptyStringList(capabilities) {
		return false
	}
	if count, exists := fields["count"]; exists && !isEmptyJSON(count) {
		var text string
		if json.Unmarshal(count, &text) == nil {
			if !strings.EqualFold(text, "all") {
				if _, err := strconv.ParseUint(text, 10, 32); err != nil {
					return false
				}
			}
		} else {
			var number json.Number
			if json.Unmarshal(count, &number) != nil {
				return false
			}
			if _, err := strconv.ParseUint(number.String(), 10, 32); err != nil {
				return false
			}
		}
	}
	if ids, exists := fields["device_ids"]; exists && !isEmptyJSON(ids) {
		if !validNonemptyStringList(ids) {
			return false
		}
		if count, hasCount := fields["count"]; hasCount && !isEmptyJSON(count) {
			return false
		}
	}
	if driver, exists := fields["driver"]; exists && !isEmptyJSON(driver) {
		value, ok := rawString(driver)
		if !ok || strings.TrimSpace(value) == "" {
			return false
		}
	}
	if options, exists := fields["options"]; exists && !isEmptyJSON(options) {
		if !validStringMapOrList(options) {
			return false
		}
	}
	return true
}

func validNonemptyStringList(raw json.RawMessage) bool {
	var values []string
	if json.Unmarshal(raw, &values) != nil || len(values) == 0 {
		return false
	}
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return false
		}
	}
	return true
}

func isEmptyJSON(raw json.RawMessage) bool {
	s := strings.TrimSpace(string(raw))
	return s == "" || s == "null" || s == "[]"
}

func sortedKeys[V any](items map[string]V) []string {
	keys := make([]string, 0, len(items))
	for key := range items {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func numberString(raw json.RawMessage) (string, bool) {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if _, err := strconv.ParseUint(s, 10, 16); err == nil {
			return s, true
		}
		return "", false
	}
	var n json.Number
	if json.Unmarshal(raw, &n) == nil {
		if _, err := strconv.ParseUint(n.String(), 10, 16); err == nil {
			return n.String(), true
		}
	}
	return "", false
}

func dispositionRank(value string) int {
	if value == DispositionRejected {
		return 2
	}
	if value == DispositionApprovalRequired {
		return 1
	}
	return 0
}

func safeNamespaceValue(field, value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	switch field {
	case "cgroup", "userns_mode":
		return value == "private"
	case "network_mode":
		return value == "bridge" || value == "default" || value == "none"
	case "ipc":
		return value == "private" || value == "shareable"
	case "pid", "uts":
		return value == "private"
	default:
		return false
	}
}

func safeCapabilityValue(value string) bool {
	for _, character := range value {
		if (character < 'A' || character > 'Z') && (character < '0' || character > '9') && character != '_' {
			return false
		}
	}
	return value != ""
}

func safeDeviceRule(value string) bool {
	if value == "" || len(value) > 256 {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character > 0x7e {
			return false
		}
	}
	return true
}

func clearRawMap(values map[string]json.RawMessage) {
	for key, value := range values {
		clear(value)
		delete(values, key)
	}
}
