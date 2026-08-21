package composeruntime

import (
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
