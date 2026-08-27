package composeruntime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hostd/hostd/internal/appconfig"
	"github.com/hostd/hostd/internal/database"
	"github.com/hostd/hostd/internal/deployments"
)

type matrixPaths struct {
	workspace  string
	outside    string
	env        string
	config     string
	secret     string
	dockerfile string
}

type matrixCase struct {
	name    string
	model   func(matrixPaths) any
	classes map[string]string
	count   int
}

func TestPolicyAllowGateRejectMatrix(t *testing.T) {
	p := newMatrixPaths(t)
	cases := []matrixCase{
		{
			name: "allow named volume",
			model: func(matrixPaths) any {
				return serviceModel(map[string]any{
					"volumes": []any{map[string]any{"type": "volume", "source": "data", "target": "/data"}},
				})
			},
			classes: map[string]string{"named_volume": DispositionAllowed},
			count:   1,
		},
		{
			name: "allow read-only managed workspace bind",
			model: func(p matrixPaths) any {
				return serviceModel(map[string]any{
					"volumes": []any{map[string]any{"type": "bind", "source": p.workspace, "target": "/app", "read_only": true}},
				})
			},
			classes: map[string]string{"workspace_bind_mount": DispositionAllowed},
			count:   1,
		},
		{
			name: "reject writable managed workspace bind",
			model: func(p matrixPaths) any {
				return serviceModel(map[string]any{
					"volumes": []any{map[string]any{"type": "bind", "source": p.workspace, "target": "/app"}},
				})
			},
			classes: map[string]string{"writable_managed_bind": DispositionRejected},
			count:   1,
		},
		{
			name: "allow tmpfs",
			model: func(matrixPaths) any {
				return serviceModel(map[string]any{
					"volumes": []any{map[string]any{"type": "tmpfs", "target": "/run/cache"}},
				})
			},
			classes: map[string]string{"tmpfs": DispositionAllowed},
			count:   1,
		},
		{
			name: "allow build and dockerfile",
			model: func(p matrixPaths) any {
				return serviceModel(map[string]any{
					"build": map[string]any{"context": p.workspace, "dockerfile": "Dockerfile"},
				})
			},
			classes: map[string]string{
				"workspace_build_context": DispositionAllowed,
				"workspace_dockerfile":    DispositionAllowed,
			},
			count: 2,
		},
		{
			name: "allow workspace additional context",
			model: func(p matrixPaths) any {
				return serviceModel(map[string]any{
					"build": map[string]any{
						"context":             p.workspace,
						"additional_contexts": map[string]any{"assets": p.workspace},
					},
				})
			},
			classes: map[string]string{
				"workspace_build_context":      DispositionAllowed,
				"workspace_additional_context": DispositionAllowed,
			},
			count: 2,
		},
		{
			name: "allow loopback and internal ports",
			model: func(matrixPaths) any {
				return serviceModel(map[string]any{
					"ports": []any{
						map[string]any{"target": 80},
						map[string]any{"target": 80, "published": "8080", "host_ip": "127.0.0.1"},
						map[string]any{"target": 80, "published": 8081, "host_ip": "::1"},
					},
				})
			},
			classes: map[string]string{"loopback_published_port": DispositionAllowed},
			count:   2,
		},
		{
			name: "allow env config secret files",
			model: func(p matrixPaths) any {
				return map[string]any{
					"services": map[string]any{
						"web": map[string]any{"env_file": []any{p.env, map[string]any{"path": p.env, "required": true}}},
					},
					"configs": map[string]any{"cfg": map[string]any{"file": p.config}},
					"secrets": map[string]any{"sec": map[string]any{"file": p.secret}},
				}
			},
			classes: map[string]string{
				"workspace_env_file":     DispositionAllowed,
				"workspace_configs_file": DispositionAllowed,
				"workspace_secrets_file": DispositionAllowed,
			},
			count: 3,
		},
		{
			name: "allow version tolerant unknown fields",
			model: func(matrixPaths) any {
				return map[string]any{
					"x-future": true,
					"services": map[string]any{
						"web": map[string]any{
							"image":       "private.invalid/token",
							"environment": map[string]any{"TOKEN": "must-not-leak"},
							"x-future":    map[string]any{"v": 1},
						},
					},
				}
			},
			classes: map[string]string{},
		},
		{
			name: "reject unknown non-extension fields",
			model: func(matrixPaths) any {
				return map[string]any{
					"future_top_level": true,
					"services": map[string]any{
						"web": map[string]any{"future_host_access": true},
					},
				}
			},
			classes: map[string]string{
				"unsupported_top_level_field": DispositionRejected,
				"unsupported_service_field":   DispositionRejected,
			},
			count: 2,
		},
		{
			name: "gate privileged namespaces socket API",
			model: func(matrixPaths) any {
				return serviceModel(map[string]any{
					"privileged":     true,
					"pid":            "host",
					"ipc":            "host",
					"uts":            "host",
					"cgroup":         "host",
					"network_mode":   "host",
					"userns_mode":    "host",
					"use_api_socket": true,
				})
			},
			classes: map[string]string{
				"privileged":            DispositionApprovalRequired,
				"host_pid_namespace":    DispositionApprovalRequired,
				"host_ipc_namespace":    DispositionApprovalRequired,
				"host_uts_namespace":    DispositionApprovalRequired,
				"host_cgroup_namespace": DispositionApprovalRequired,
				"host_network":          DispositionApprovalRequired,
				"host_userns_namespace": DispositionApprovalRequired,
				"docker_socket_api":     DispositionApprovalRequired,
			},
			count: 8,
		},
		{
			name: "gate devices GPUs caps rules security",
			model: func(matrixPaths) any {
				return serviceModel(map[string]any{
					"devices": []any{
						"/dev/null:/dev/null",
						map[string]any{"source": "/dev/dri", "target": "/dev/dri"},
					},
					"gpus":                []any{map[string]any{"driver": "nvidia", "count": 1}},
					"cap_add":             []any{"NET_ADMIN", "sys_time"},
					"device_cgroup_rules": []any{"c 1:3 rwm"},
					"security_opt":        []any{"seccomp=unconfined", "label=disable"},
				})
			},
			classes: map[string]string{
				"device":                  DispositionApprovalRequired,
				"gpu":                     DispositionApprovalRequired,
				"cap_add":                 DispositionApprovalRequired,
				"device_cgroup_rule":      DispositionApprovalRequired,
				"unconfined_security_opt": DispositionApprovalRequired,
			},
			count: 8,
		},
		{
			name: "gate Docker socket",
			model: func(matrixPaths) any {
				return serviceModel(map[string]any{
					"volumes": []any{map[string]any{"type": "bind", "source": "/var/run/docker.sock", "target": "/var/run/docker.sock"}},
				})
			},
			classes: map[string]string{"docker_socket_mount": DispositionApprovalRequired},
			count:   1,
		},
		{
			name: "gate exact external bind",
			model: func(p matrixPaths) any {
				return serviceModel(map[string]any{
					"volumes": []any{map[string]any{"type": "bind", "source": p.outside, "target": "/outside", "read_only": true}},
				})
			},
			classes: map[string]string{"external_bind": DispositionApprovalRequired},
			count:   1,
		},
		{
			name: "gate nonloopback ports",
			model: func(matrixPaths) any {
				return serviceModel(map[string]any{
					"ports": []any{
						map[string]any{"target": 80, "published": 8080},
						map[string]any{"target": 81, "published": 8081, "host_ip": "0.0.0.0"},
						map[string]any{"target": 82, "published": 8082, "host_ip": "192.0.2.4"},
					},
				})
			},
			classes: map[string]string{"published_port": DispositionApprovalRequired},
			count:   3,
		},
		{
			name: "reject remote builds",
			model: func(matrixPaths) any {
				return serviceModel(map[string]any{
					"build": map[string]any{
						"context":             "https://example.invalid/repo.git",
						"additional_contexts": map[string]any{"image": "docker-image://busybox:latest"},
					},
				})
			},
			classes: map[string]string{
				"remote_build_context":      DispositionRejected,
				"remote_additional_context": DispositionRejected,
			},
			count: 2,
		},
		{
			name: "reject absolute dockerfile",
			model: func(p matrixPaths) any {
				return serviceModel(map[string]any{
					"build": map[string]any{"context": p.workspace, "dockerfile": p.dockerfile},
				})
			},
			classes: map[string]string{
				"workspace_build_context": DispositionAllowed,
				"invalid_dockerfile":      DispositionRejected,
			},
			count: 2,
		},
		{
			name: "reject escaped files",
			model: func(p matrixPaths) any {
				return map[string]any{
					"services": map[string]any{"web": map[string]any{"env_file": p.outside}},
					"configs":  map[string]any{"cfg": map[string]any{"file": p.outside}},
					"secrets":  map[string]any{"sec": map[string]any{"file": "https://example.invalid/secret"}},
				}
			},
			classes: map[string]string{
				"invalid_env_file":     DispositionRejected,
				"invalid_configs_file": DispositionRejected,
				"invalid_secrets_file": DispositionRejected,
			},
			count: 3,
		},
		{
			name: "reject relative traversal",
			model: func(p matrixPaths) any {
				relativeOutside, err := filepath.Rel(p.workspace, p.outside)
				if err != nil {
					panic(err)
				}
				return serviceModel(map[string]any{
					"volumes": []any{map[string]any{"type": "bind", "source": relativeOutside, "target": "/outside"}},
				})
			},
			classes: map[string]string{"workspace_escape": DispositionRejected},
			count:   1,
		},
		{
			name: "reject external and remote resources",
			model: func(matrixPaths) any {
				return map[string]any{
					"services": map[string]any{"web": map[string]any{}},
					"volumes": map[string]any{
						"ext": map[string]any{"external": true},
						"nfs": map[string]any{"driver": "local", "driver_opts": map[string]any{"type": "nfs"}},
					},
					"configs":  map[string]any{"cfg": map[string]any{"external": true}},
					"secrets":  map[string]any{"sec": map[string]any{"external": map[string]any{"name": "host"}}},
					"networks": map[string]any{"net": map[string]any{"driver": "overlay"}},
				}
			},
			classes: map[string]string{
				"external_volume":       DispositionRejected,
				"remote_volume_options": DispositionRejected,
				"external_config":       DispositionRejected,
				"external_secret":       DispositionRejected,
				"remote_network_driver": DispositionRejected,
			},
			count: 5,
		},
		{
			name: "reject malformed critical fields",
			model: func(matrixPaths) any {
				return serviceModel(map[string]any{
					"privileged":   "yes",
					"pid":          7,
					"ipc":          "container:other",
					"network_mode": "container:other",
					"cap_add":      []any{"", "SYS ADMIN"},
					"gpus":         "some",
					"devices":      map[string]any{"bad": true},
					"volumes":      map[string]any{"bad": true},
					"ports":        map[string]any{"bad": true},
					"env_file":     []any{7},
					"volumes_from": []any{"other"},
					"provider":     map[string]any{"type": "remote"},
				})
			},
			classes: map[string]string{
				"unsupported_privileged":   DispositionRejected,
				"unsupported_pid":          DispositionRejected,
				"unsupported_ipc":          DispositionRejected,
				"unsupported_network_mode": DispositionRejected,
				"unsupported_cap_add":      DispositionRejected,
				"unsupported_gpus":         DispositionRejected,
				"unsupported_devices":      DispositionRejected,
				"unsupported_volume":       DispositionRejected,
				"unsupported_port":         DispositionRejected,
				"unsupported_env_file":     DispositionRejected,
				"unsupported_volumes_from": DispositionRejected,
				"unsupported_provider":     DispositionRejected,
			},
			count: 12,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, _ := json.Marshal(tc.model(p))
			findings, err := EvaluatePolicy(body, p.workspace)
			if err != nil {
				t.Fatal(err)
			}
			assertMatrix(t, findings, tc.classes, tc.count)
			for _, f := range findings {
				if strings.Contains(f.Scope, "must-not-leak") || strings.Contains(f.Scope, "private.invalid") {
					t.Fatalf("secret leaked: %#v", f)
				}
			}
		})
	}
}

func TestPolicyMalformedBoundsDedupeAndDeterminism(t *testing.T) {
	workspace := t.TempDir()
	bad := []struct {
		name string
		body []byte
		code string
	}{
		{"empty", nil, "model_too_large"},
		{"malformed", []byte(`{"services":`), "malformed_model"},
		{"trailing", []byte(`{"services":{"web":{}}} garbage`), "malformed_model"},
		{"missing", []byte(`{}`), "missing_services"},
		{"empty services", []byte(`{"services":{}}`), "malformed_services"},
		{"oversize", make([]byte, MaxEffectiveJSONBytes+1), "model_too_large"},
	}
	tooManyServices := make(map[string]any, 513)
	for i := 0; i < 513; i++ {
		tooManyServices["service-"+strconv.Itoa(i)] = map[string]any{}
	}
	tooManyServicesBody, err := json.Marshal(map[string]any{"services": tooManyServices})
	if err != nil {
		t.Fatal(err)
	}
	bad = append(bad, struct {
		name string
		body []byte
		code string
	}{"service count", tooManyServicesBody, "malformed_services"})
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			_, err := EvaluatePolicy(tc.body, workspace)
			var pe *PolicyError
			if !errors.As(err, &pe) || pe.Code != tc.code {
				t.Fatalf("error=%v want=%s", err, tc.code)
			}
		})
	}
	findings, err := EvaluatePolicy([]byte(`{"services":{"web":{"cap_add":["NET_ADMIN","net_admin"]}}}`), workspace)
	if err != nil || len(findings) != 1 {
		t.Fatalf("dedupe=%#v err=%v", findings, err)
	}
	values := make([]string, MaxPolicyFindings+1)
	for i := range values {
		values[i] = "CAP_" + strings.Repeat("A", i/26) + string(rune('A'+i%26))
	}
	body, _ := json.Marshal(serviceModel(map[string]any{"cap_add": values}))
	_, err = EvaluatePolicy(body, workspace)
	var pe *PolicyError
	if !errors.As(err, &pe) || pe.Code != "too_many_findings" {
		t.Fatalf("bound=%v", err)
	}
	a, ea := EvaluatePolicy([]byte(`{"services":{"b":{"privileged":true},"a":{"cap_add":["NET_ADMIN"]}}}`), workspace)
	b, eb := EvaluatePolicy([]byte(`{"services":{"a":{"cap_add":["NET_ADMIN"]},"b":{"privileged":true}}}`), workspace)
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	if ea != nil || eb != nil || string(ja) != string(jb) {
		t.Fatalf("nondeterministic %s %s %v %v", ja, jb, ea, eb)
	}
}

func TestPolicyCanonicalScopeFingerprintAndRepositoryParity(t *testing.T) {
	p := newMatrixPaths(t)
	body, _ := json.Marshal(serviceModel(map[string]any{"volumes": []any{map[string]any{"type": "bind", "source": p.outside, "target": "/data", "read_only": true}}}))
	findings, err := EvaluatePolicy(body, p.workspace)
	if err != nil || len(findings) != 1 {
		t.Fatalf("findings=%#v err=%v", findings, err)
	}
	f := findings[0]
	canonical, _ := filepath.EvalSymlinks(p.outside)
	want, _ := json.Marshal(map[string]any{"service": "web", "source": normalizePath(canonical), "target": "/data", "mode": "ro"})
	if f.Scope != string(want) {
		t.Fatalf("scope=%s want=%s", f.Scope, want)
	}
	input, _ := json.Marshal(struct {
		PolicyVersion string `json:"policyVersion"`
		Capability    string `json:"capability"`
		Scope         string `json:"scope"`
	}{PolicyVersion, f.Capability, f.Scope})
	sum := sha256.Sum256(input)
	if f.Fingerprint != hex.EncodeToString(sum[:]) || f.Fingerprint != PolicyFingerprint(PolicyVersion, f.Capability, f.Scope) {
		t.Fatalf("fingerprint=%s", f.Fingerprint)
	}
	assertRepositoryParity(t, f)
}

func TestPolicyRejectsSymlinkAncestor(t *testing.T) {
	workspace, outside := t.TempDir(), t.TempDir()
	link := filepath.Join(workspace, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	body, _ := json.Marshal(serviceModel(map[string]any{"volumes": []any{map[string]any{"type": "bind", "source": link, "target": "/data"}}}))
	findings, err := EvaluatePolicy(body, workspace)
	if err != nil || len(findings) != 1 || findings[0].Disposition != DispositionRejected {
		t.Fatalf("findings=%#v err=%v", findings, err)
	}
}

func TestPolicyRejectsRelativeWorkspace(t *testing.T) {
	if _, err := EvaluatePolicy([]byte(`{"services":{"web":{}}}`), "relative"); err == nil {
		t.Fatal("relative workspace accepted")
	}
}

func TestPolicyRejectsWindowsNamespacesBeforeFilesystemAccess(t *testing.T) {
	t.Parallel()
	model := []byte(`{"services":{"web":{"volumes":[{"type":"bind","source":"\\\\?\\C:\\outside","target":"/data"}]}}}`)
	findings, err := EvaluatePolicy(model, t.TempDir())
	if err != nil || len(findings) != 1 || findings[0].Disposition != DispositionRejected {
		t.Fatalf("namespace finding=%#v err=%v", findings, err)
	}
	if _, err := EvaluatePolicy([]byte(`{"services":{"web":{}}}`), `\\server\share`); err == nil {
		t.Fatal("UNC workspace accepted")
	}
}

func TestPolicyRecursiveCompositeSchema(t *testing.T) {
	p := newMatrixPaths(t)
	cases := []struct {
		name        string
		service     map[string]any
		want        map[string]string
		wantScope   map[string]string
		wantNoClass []string
	}{
		{
			name: "lifecycle hook privileged is exact and unknown fields reject",
			service: map[string]any{
				"post_start": []any{
					map[string]any{"command": []any{"echo", "ok"}, "privileged": true},
					map[string]any{"command": "true", "future_host_access": true},
				},
			},
			want: map[string]string{
				"post_start_privileged":        DispositionApprovalRequired,
				"unsupported_post_start_field": DispositionRejected,
			},
			wantScope: map[string]string{"post_start_privileged": `{"index":0,"service":"web"}`},
		},
		{
			name:    "pre stop rejected",
			service: map[string]any{"pre_stop": []any{map[string]any{"command": "true"}}},
			want:    map[string]string{"unsupported_pre_stop": DispositionRejected},
		},
		{
			name: "build capability fields are recursively classified",
			service: map[string]any{"build": map[string]any{
				"context": p.workspace, "privileged": true, "network": "host",
				"entitlements": []any{"network.host", "security.insecure"},
				"ssh":          []any{"default"}, "secrets": []any{"build-secret"},
				"future_host_access": true,
			}},
			want: map[string]string{
				"workspace_build_context":   DispositionAllowed,
				"build_privileged":          DispositionApprovalRequired,
				"build_host_network":        DispositionApprovalRequired,
				"build_entitlement":         DispositionApprovalRequired,
				"unsupported_build_ssh":     DispositionRejected,
				"unsupported_build_secrets": DispositionRejected,
				"unsupported_build_field":   DispositionRejected,
			},
		},
		{
			name: "deploy device reservation scope binds the entire normalized request",
			service: map[string]any{"deploy": map[string]any{"resources": map[string]any{"reservations": map[string]any{
				"devices": []any{map[string]any{
					"capabilities": []any{"gpu"}, "count": 1, "driver": "nvidia",
					"options": map[string]any{"virtualization": "false"},
				}},
			}}}},
			want: map[string]string{"deploy_reservation_device": DispositionApprovalRequired},
			wantScope: map[string]string{
				"deploy_reservation_device": `{"device":{"capabilities":["gpu"],"count":1,"driver":"nvidia","options":{"virtualization":"false"}},"index":0,"service":"web"}`,
			},
		},
		{
			name: "uninspected deploy composites and generic resources reject",
			service: map[string]any{"deploy": map[string]any{
				"placement":      map[string]any{"constraints": []any{"node.role==manager"}},
				"restart_policy": map[string]any{"condition": "any"},
				"resources":      map[string]any{"reservations": map[string]any{"generic_resources": []any{"GPU=1"}}},
			}},
			want: map[string]string{
				"unsupported_deploy_placement":         DispositionRejected,
				"unsupported_deploy_restart_policy":    DispositionRejected,
				"unsupported_deploy_generic_resources": DispositionRejected,
			},
		},
		{
			name: "security options allow only hardened or approval gated values",
			service: map[string]any{"security_opt": []any{
				"no-new-privileges:true", "seccomp=unconfined", "apparmor=custom-profile",
			}},
			want: map[string]string{
				"unconfined_security_opt":  DispositionApprovalRequired,
				"unsupported_security_opt": DispositionRejected,
			},
		},
		{
			name: "recognized host capability fields reject",
			service: map[string]any{
				"credential_spec": map[string]any{"file": "host.json"}, "runtime": "runc",
				"isolation": "hyperv", "cgroup_parent": "host.slice", "oom_kill_disable": true,
			},
			want: map[string]string{
				"unsupported_credential_spec":  DispositionRejected,
				"unsupported_runtime":          DispositionRejected,
				"unsupported_isolation":        DispositionRejected,
				"unsupported_cgroup_parent":    DispositionRejected,
				"unsupported_oom_kill_disable": DispositionRejected,
			},
		},
		{
			name: "unknown long syntax keys fail closed",
			service: map[string]any{
				"volumes":  []any{map[string]any{"type": "volume", "source": "data", "target": "/data", "future": true}},
				"ports":    []any{map[string]any{"target": 80, "future": true}},
				"env_file": []any{map[string]any{"path": p.env, "future": true}},
			},
			want: map[string]string{
				"unsupported_volume_field":   DispositionRejected,
				"unsupported_port_field":     DispositionRejected,
				"unsupported_env_file_field": DispositionRejected,
			},
		},
		{
			name: "nested field type mismatch fails closed",
			service: map[string]any{
				"post_start": []any{map[string]any{"command": true, "environment": true, "user": true}},
				"build":      map[string]any{"context": p.workspace, "args": true},
			},
			want: map[string]string{
				"unsupported_post_start_command":     DispositionRejected,
				"unsupported_post_start_environment": DispositionRejected,
				"unsupported_post_start_user":        DispositionRejected,
				"unsupported_build_args":             DispositionRejected,
				"workspace_build_context":            DispositionAllowed,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, err := json.Marshal(serviceModel(tc.service))
			if err != nil {
				t.Fatal(err)
			}
			findings, err := EvaluatePolicy(body, p.workspace)
			if err != nil {
				t.Fatal(err)
			}
			byClass := make(map[string][]PolicyFinding)
			for _, finding := range findings {
				byClass[finding.Capability] = append(byClass[finding.Capability], finding)
			}
			for capability, disposition := range tc.want {
				matches := byClass[capability]
				if len(matches) == 0 {
					t.Fatalf("missing %s in %#v", capability, findings)
				}
				for _, finding := range matches {
					if finding.Disposition != disposition {
						t.Fatalf("%s disposition=%s want=%s", capability, finding.Disposition, disposition)
					}
				}
				if scope, ok := tc.wantScope[capability]; ok && len(matches) == 1 && matches[0].Scope != scope {
					t.Fatalf("%s scope=%s want=%s", capability, matches[0].Scope, scope)
				}
			}
		})
	}
}

func TestPolicySecretOriginsAreSanitizedAndRejected(t *testing.T) {
	p := newMatrixPaths(t)
	secretDirectory := filepath.Join(p.workspace, "SecretPath")
	if err := os.Mkdir(secretDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	const (
		revisionID          = "f3dfa55c-2e77-432c-8dec-a7a23c88f552"
		expectedPlaceholder = "secret-origin:570006fde010dc47210bd8fe3299874b861bb48080ba4bd94ee0e37eb98ac7c8"
	)
	origin := appconfig.SecretOrigin{RevisionID: revisionID, RevisionNumber: 7, Key: []byte("TOKEN"), Value: []byte("first-value")}
	if got := secretOriginPlaceholder(origin); got != expectedPlaceholder {
		t.Fatalf("placeholder=%q want=%q", got, expectedPlaceholder)
	}
	origin.Value = []byte("second-value")
	if got := secretOriginPlaceholder(origin); got != expectedPlaceholder {
		t.Fatalf("placeholder changed with secret value: got=%q want=%q", got, expectedPlaceholder)
	}
	if !strings.Contains(expectedPlaceholder, "8080") {
		t.Fatalf("collision fixture no longer contains published-port secret: %q", expectedPlaceholder)
	}
	expectedScopeBytes, err := json.Marshal(map[string]any{"secretOrigins": []string{expectedPlaceholder}})
	if err != nil {
		t.Fatal(err)
	}
	expectedScope := string(expectedScopeBytes)
	tests := []struct {
		name   string
		secret string
		model  any
	}{
		{
			name:   "capability uppercase normalization",
			secret: "net_admin",
			model:  serviceModel(map[string]any{"cap_add": []any{"NET_ADMIN"}}),
		},
		{
			name:   "canonical managed path",
			secret: "SecretPath",
			model: serviceModel(map[string]any{
				"volumes": []any{map[string]any{
					"type": "bind", "source": secretDirectory, "target": "/data", "read_only": true,
				}},
			}),
		},
		{
			name:   "published port scalar",
			secret: "8080",
			model: serviceModel(map[string]any{
				"ports": []any{map[string]any{"target": 80, "published": 8080}},
			}),
		},
		{
			name:   "deploy device option",
			secret: "secret-option",
			model: serviceModel(map[string]any{
				"deploy": map[string]any{
					"resources": map[string]any{
						"reservations": map[string]any{
							"devices": []any{map[string]any{
								"capabilities": []any{"gpu"},
								"options":      map[string]any{"profile": "secret-option"},
							}},
						},
					},
				},
			}),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body, err := json.Marshal(test.model)
			if err != nil {
				t.Fatal(err)
			}
			origin := appconfig.SecretOrigin{RevisionID: revisionID, RevisionNumber: 7, Key: []byte("TOKEN"), Value: []byte(test.secret)}
			findings, err := EvaluatePolicy(body, p.workspace, origin)
			if err != nil || len(findings) != 1 {
				t.Fatalf("findings=%#v err=%v", findings, err)
			}
			finding := findings[0]
			if finding.Scope != expectedScope {
				t.Fatalf("scope=%s want=%s", finding.Scope, expectedScope)
			}
			if finding.Disposition != DispositionRejected {
				t.Fatalf("tainted finding disposition=%s", finding.Disposition)
			}
			wantFingerprint := PolicyFingerprint(PolicyVersion, finding.Capability, expectedScope)
			if finding.Fingerprint != wantFingerprint {
				t.Fatalf("fingerprint=%s want=%s", finding.Fingerprint, wantFingerprint)
			}

			normalized := finding
			normalized.Scope = `{"secretOrigins":["<validated-opaque-placeholder>"]}`
			normalized.Fingerprint = "<validated-opaque-fingerprint>"
			encoded, err := json.Marshal(normalized)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(bytes.ToLower(encoded), bytes.ToLower([]byte(test.secret))) {
				t.Fatalf("secret persisted outside validated opaque fields: %s", encoded)
			}
		})
	}
}

func TestPolicySecretOriginRotationAndUntaintedScopes(t *testing.T) {
	workspace := t.TempDir()
	body := []byte(`{"services":{"web":{"privileged":true}}}`)
	baseline, err := EvaluatePolicy(body, workspace)
	if err != nil || len(baseline) != 1 {
		t.Fatalf("baseline=%#v err=%v", baseline, err)
	}
	unrelated := appconfig.SecretOrigin{RevisionID: uuid.NewString(), RevisionNumber: 1, Key: []byte("TOKEN"), Value: []byte("does-not-occur")}
	control, err := EvaluatePolicy(body, workspace, unrelated)
	if err != nil || len(control) != 1 || control[0] != baseline[0] {
		t.Fatalf("untainted scope changed: baseline=%#v control=%#v err=%v", baseline, control, err)
	}

	shortValue := []byte("web")
	first := appconfig.SecretOrigin{RevisionID: uuid.NewString(), RevisionNumber: 1, Key: []byte("TOKEN"), Value: shortValue}
	second := appconfig.SecretOrigin{RevisionID: uuid.NewString(), RevisionNumber: 2, Key: []byte("TOKEN"), Value: append([]byte(nil), shortValue...)}
	a, err := EvaluatePolicy(body, workspace, first)
	if err != nil || len(a) != 1 || a[0].Disposition != DispositionRejected {
		t.Fatalf("first taint=%#v err=%v", a, err)
	}
	b, err := EvaluatePolicy(body, workspace, second)
	if err != nil || len(b) != 1 || b[0].Disposition != DispositionRejected {
		t.Fatalf("second taint=%#v err=%v", b, err)
	}
	if a[0].Fingerprint == b[0].Fingerprint || a[0].Scope == b[0].Scope {
		t.Fatalf("revision rotation reused sanitized identity: %#v %#v", a, b)
	}
	// Intentionally conservative: short/common secret values that occur in an
	// otherwise non-sensitive exact scope fail closed instead of risking reuse.
}

func newMatrixPaths(t *testing.T) matrixPaths {
	t.Helper()
	w := t.TempDir()
	o := t.TempDir()
	p := matrixPaths{
		workspace:  w,
		outside:    o,
		env:        filepath.Join(w, ".env"),
		config:     filepath.Join(w, "config"),
		secret:     filepath.Join(w, "secret"),
		dockerfile: filepath.Join(w, "Dockerfile"),
	}
	for _, path := range []string{p.env, p.config, p.secret, p.dockerfile} {
		if err := os.WriteFile(path, []byte("safe"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

func serviceModel(service map[string]any) any {
	return map[string]any{"services": map[string]any{"web": service}}
}

func assertMatrix(t *testing.T, findings []PolicyFinding, want map[string]string, count int) {
	t.Helper()
	if len(findings) != count {
		t.Fatalf("count=%d want=%d findings=%#v", len(findings), count, findings)
	}
	got := map[string]string{}
	for _, f := range findings {
		got[f.Capability] = f.Disposition
	}
	if len(got) != len(want) {
		t.Fatalf("classes=%v want=%v", got, want)
	}
	for c, d := range want {
		if got[c] != d {
			t.Fatalf("%s=%s want=%s", c, got[c], d)
		}
	}
}

func assertRepositoryParity(t *testing.T, f PolicyFinding) {
	t.Helper()
	db, err := database.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	app, job, actor, machine := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	statements := []struct {
		q string
		a []any
	}{{`INSERT INTO users(id,username,passphrase_hash,created_at,updated_at) VALUES(?,'owner','hash',?,?)`, []any{actor, now, now}}, {`INSERT INTO machines(id,name,mode,status,os,architecture,hostname,agent_version,created_at,updated_at) VALUES(?,'local','local','ready','x','x','x','x',?,?)`, []any{machine, now, now}}, {`INSERT INTO applications(id,slug,name,active_machine_id,status,created_at,updated_at) VALUES(?,?,?,?,'draft',?,?)`, []any{app, app, app, machine, now, now}}, {`INSERT INTO jobs(id,type,resource_type,resource_id,status,phase,requested_by,created_at,updated_at) VALUES(?,'deploy','application',?,'queued','queued',?,?,?)`, []any{job, app, actor, now, now}}}
	for _, s := range statements {
		if _, err := db.Exec(s.q, s.a...); err != nil {
			t.Fatal(err)
		}
	}
	r := deployments.New(db)
	d, err := r.Create(context.Background(), app, job, "current")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.Initialize(context.Background(), app, d.ID, "", "", 0); err != nil {
		t.Fatal(err)
	}
	err = r.Gate(context.Background(), app, d.ID, []deployments.Finding{{PolicyVersion: f.PolicyVersion, Capability: f.Capability, Scope: f.Scope, Fingerprint: f.Fingerprint, Disposition: f.Disposition}})
	if !errors.Is(err, deployments.ErrApprovalRequired) {
		t.Fatalf("repository parity=%v", err)
	}
}
