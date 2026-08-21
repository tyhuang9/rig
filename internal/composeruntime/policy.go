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
	e.volumes(name, service["volumes"])
	e.ports(name, service["ports"])
	e.envFiles(name, service["env_file"])
	for _, field := range []string{"extends", "include", "volumes_from", "provider"} {
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
		if strings.Contains(lower, "unconfined") || lower == "label=disable" || lower == "label:disable" {
			e.add("unconfined_security_opt", map[string]any{"service": service, "option": lower}, DispositionApprovalRequired)
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
			path, ok = rawString(object["path"])
			clearRawMap(object)
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
