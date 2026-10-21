package generatedcontainer

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"text/template"
	"time"
)

func inspectionFixture() (Inspection, Identity, Limits) {
	expected := Identity{
		AppID: "app", ReleaseID: "release", DeploymentID: "deployment", ArtifactID: "artifact", DeploymentPlanRevisionID: "plan",
		Component: "web", Role: "server", Technology: "node", Slot: "blue", ReceiptID: "receipt",
		ContainerID: strings.Repeat("a", 64), ContainerName: "candidate", NetworkName: "network", NetworkAlias: "web-blue",
		ImageContentID: "sha256:" + strings.Repeat("b", 64), WorkingDirectory: "/workspace", RunCommandDigest: digest("node server.js"),
	}
	limits := Limits{MemoryBytes: 512 << 20, MilliCPUs: 1000, PIDs: 256, TmpfsBytes: 64 << 20, LogSize: "10m", LogFiles: 3}
	actual := Inspection{
		ID: expected.ContainerID, Name: "/" + expected.ContainerName, Image: expected.ImageContentID, ConfigImage: expected.ImageContentID,
		User: User, WorkingDirectory: expected.WorkingDirectory, Entrypoint: []string{Entrypoint}, Command: []string{"/bin/sh", "-lc", "node server.js"},
		Labels: map[string]string{"io.rig.managed": "generated-runtime", "io.rig.application": expected.AppID, "io.rig.release": expected.ReleaseID,
			"io.rig.deployment": expected.DeploymentID, "io.rig.artifact": expected.ArtifactID, "io.rig.plan": expected.DeploymentPlanRevisionID,
			"io.rig.component": expected.Component, "io.rig.role": expected.Role, "io.rig.slot": expected.Slot, ReceiptLabel: expected.ReceiptID},
		HealthTest: []string{"CMD-SHELL", HealthCommand}, HealthInterval: int64(2 * time.Second), HealthTimeout: int64(2 * time.Second), HealthStartPeriod: int64(5 * time.Second), HealthRetries: 3,
		Memory: limits.MemoryBytes, MemorySwap: limits.MemoryBytes, NanoCPUs: limits.MilliCPUs * 1_000_000, PIDs: limits.PIDs,
		Ulimits: []Ulimit{{Name: "nofile", Soft: 1024, Hard: 1024}}, Init: true, NetworkMode: expected.NetworkName, ReadonlyRootfs: true,
		CapDrop: []string{"ALL"}, SecurityOptions: []string{"no-new-privileges=true"}, Tmpfs: map[string]string{"/tmp": "rw,noexec,nosuid,nodev,size=67108864"},
		LogType: "local", LogConfig: map[string]string{"max-size": limits.LogSize, "max-file": "3"}, Restart: "no",
		Status: "created", StartedAt: "0001-01-01T00:00:00Z",
	}
	return actual, expected, limits
}

func encodeInspection(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func inspectionDocument(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	fixture, _, _ := inspectionFixture()
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(encodeInspection(t, fixture), &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func TestDecodeCompleteCreatedAndRunning(t *testing.T) {
	fixture, expected, limits := inspectionFixture()
	for _, running := range []bool{false, true} {
		if running {
			fixture.Running, fixture.Status, fixture.StartedAt, fixture.Health = true, "running", "2026-10-20T00:00:00.123456789Z", "healthy"
			fixture.Networks = map[string]NetworkAttachment{expected.NetworkName: {Aliases: []string{expected.NetworkAlias, "docker-id"}}}
			fixture.Mounts = []Mount{{Type: "tmpfs", Destination: "/tmp"}}
		}
		actual, err := Decode(encodeInspection(t, fixture))
		if err != nil || !reflect.DeepEqual(actual, fixture) {
			t.Fatalf("roundtrip error=%v", err)
		}
		if !MatchesConfigured(actual, expected, limits) || NeverStarted(actual) == running || CurrentlyRunning(actual) != running {
			t.Fatal("physical state or policy changed")
		}
		if MatchesRunning(actual, expected, limits) != running {
			t.Fatal("network realization was not distinguished")
		}
	}
}

func TestDecodeRequiresEveryProjectedField(t *testing.T) {
	for _, name := range inspectionFields {
		t.Run(name, func(t *testing.T) {
			doc := inspectionDocument(t)
			delete(doc, name)
			if _, err := Decode(encodeInspection(t, doc)); !errors.Is(err, ErrIncompleteInspection) {
				t.Fatal("missing field accepted")
			}
		})
	}
}

func TestDecodeRejectsNullScalars(t *testing.T) {
	for _, name := range scalarFields {
		t.Run(name, func(t *testing.T) {
			doc := inspectionDocument(t)
			doc[name] = json.RawMessage("null")
			if _, err := Decode(encodeInspection(t, doc)); !errors.Is(err, ErrIncompleteInspection) {
				t.Fatal("null scalar accepted")
			}
		})
	}
}

func TestDecodeRejectsMalformedAndAmbiguousEvidence(t *testing.T) {
	valid := encodeInspection(t, inspectionDocument(t))
	cases := map[string][]byte{
		"invalid utf8": append(append([]byte(nil), valid[:len(valid)-1]...), 0xff, '}'),
		"truncated":    valid[:len(valid)-1], "trailing document": append(append([]byte(nil), valid...), []byte(" {}")...),
		"duplicate top":         append([]byte(`{"id":"duplicate",`), valid[1:]...),
		"duplicate escaped top": append([]byte(`{"\u0069d":"duplicate",`), valid[1:]...),
		"null document":         []byte("null"), "array document": []byte("[]"),
	}
	mutations := map[string]struct{ field, raw string }{
		"duplicate label":      {"labels", `{"io.rig.managed":"generated-runtime","io.rig.managed":"foreign"}`},
		"null label":           {"labels", `{"io.rig.managed":null}`},
		"null command element": {"cmd", `["/bin/sh",null,"command"]`},
		"wrong integer":        {"memory", `"536870912"`}, "fractional integer": {"memory", `1.5`}, "wrong boolean": {"running", `"false"`},
		"missing ulimit":            {"ulimits", `[{"Name":"nofile","Soft":1024}]`},
		"null ulimit":               {"ulimits", `[{"Name":"nofile","Soft":null,"Hard":1024}]`},
		"null ulimit object":        {"ulimits", `[null]`},
		"case shadow ulimit":        {"ulimits", `[{"Name":"foreign","name":"nofile","Soft":1024,"Hard":1024}]`},
		"null mount source":         {"mounts", `[{"Type":"tmpfs","Source":null,"Destination":"/tmp"}]`},
		"missing mount destination": {"mounts", `[{"Type":"tmpfs","Source":""}]`},
		"wrong mount scalar":        {"mounts", `[{"Type":1,"Source":"","Destination":"/tmp"}]`},
		"missing aliases":           {"networks", `{"network":{}}`}, "null network": {"networks", `{"network":null}`},
		"null alias element":  {"networks", `{"network":{"Aliases":[null]}}`},
		"duplicate network":   {"networks", `{"network":{"Aliases":[]},"network":{"Aliases":["web-blue"]}}`},
		"case shadow aliases": {"networks", `{"network":{"Aliases":["foreign"],"aliases":["web-blue"]}}`},
		"unexpected field":    {"env", `["SENTINEL_PRIVATE_VALUE"]`},
	}
	for name, mutation := range mutations {
		doc := inspectionDocument(t)
		doc[mutation.field] = json.RawMessage(mutation.raw)
		cases[name] = encodeInspection(t, doc)
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			value, err := Decode(data)
			if err != ErrIncompleteInspection || !reflect.DeepEqual(value, Inspection{}) {
				t.Fatal("ambiguous evidence accepted or exposed")
			}
		})
	}
}

func TestInspectionTemplateOmitsEnvironmentAndPreservesMissingScalars(t *testing.T) {
	jsonFunc := func(value any) string { data, _ := json.Marshal(value); return string(data) }
	projection, err := template.New("inspect").Funcs(template.FuncMap{"json": jsonFunc}).Parse(InspectFormat)
	if err != nil {
		t.Fatal(err)
	}
	input := map[string]any{
		"ID": "id", "Name": "name", "Image": "image", "Mounts": nil,
		"Config":     map[string]any{"Env": []string{"PRIVATE=SENTINEL_PRIVATE_VALUE"}},
		"HostConfig": map[string]any{"LogConfig": map[string]any{}, "RestartPolicy": map[string]any{}}, "State": map[string]any{},
	}
	var output bytes.Buffer
	if err := projection.Execute(&output, input); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "SENTINEL_PRIVATE_VALUE") || strings.Contains(InspectFormat, ".Config.Env") {
		t.Fatal("environment leaked into projection")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(output.Bytes(), &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != len(inspectionFields) || !jsonNull(fields["init"]) || !jsonNull(fields["pidsLimit"]) || !jsonNull(fields["healthInterval"]) {
		t.Fatal("projection invented absent scalar values")
	}
	if _, err := Decode(output.Bytes()); err != ErrIncompleteInspection {
		t.Fatal("missing physical evidence accepted")
	}
}

func TestHardeningRejectsIdentityConfigurationAndMountDrift(t *testing.T) {
	cases := map[string]func(*Inspection){
		"receipt": func(v *Inspection) { v.Labels[ReceiptLabel] = "foreign" }, "id": func(v *Inspection) { v.ID = "foreign" },
		"config image": func(v *Inspection) { v.ConfigImage = "mutable:tag" }, "entrypoint": func(v *Inspection) { v.Entrypoint = []string{"sh"} },
		"missing entrypoint": func(v *Inspection) { v.Entrypoint = nil },
		"extra entrypoint":   func(v *Inspection) { v.Entrypoint = []string{Entrypoint, "foreign"} },
		"image volume":       func(v *Inspection) { v.Volumes = map[string]json.RawMessage{"/data": json.RawMessage(`{}`)} },
		"host mount":         func(v *Inspection) { v.HostMounts = []json.RawMessage{json.RawMessage(`{"Type":"bind"}`)} },
		"runtime bind":       func(v *Inspection) { v.Mounts = []Mount{{Type: "bind", Source: "/private", Destination: "/tmp"}} },
		"unexpected tmpfs":   func(v *Inspection) { v.Mounts = []Mount{{Type: "tmpfs", Destination: "/other"}} },
		"duplicate mount": func(v *Inspection) {
			v.Mounts = []Mount{{Type: "tmpfs", Destination: "/tmp"}, {Type: "tmpfs", Destination: "/tmp"}}
		},
		"mount source": func(v *Inspection) { v.Mounts = []Mount{{Type: "tmpfs", Source: "foreign", Destination: "/tmp"}} },
		"privileged":   func(v *Inspection) { v.Privileged = true }, "writable root": func(v *Inspection) { v.ReadonlyRootfs = false },
		"capability": func(v *Inspection) { v.CapAdd = []string{"SYS_ADMIN"} }, "missing cap drop": func(v *Inspection) { v.CapDrop = nil },
		"security": func(v *Inspection) { v.SecurityOptions = []string{"no-new-privileges=false"} },
		"user":     func(v *Inspection) { v.User = "root" }, "command": func(v *Inspection) { v.Command[2] = "foreign" },
		"health": func(v *Inspection) { v.HealthTest[1] = "true" }, "health timeout": func(v *Inspection) { v.HealthTimeout++ },
		"memory": func(v *Inspection) { v.Memory++ }, "swap": func(v *Inspection) { v.MemorySwap = -1 }, "cpu": func(v *Inspection) { v.NanoCPUs++ },
		"pids": func(v *Inspection) { v.PIDs++ }, "ulimit": func(v *Inspection) { v.Ulimits[0].Hard++ }, "init": func(v *Inspection) { v.Init = false },
		"network": func(v *Inspection) { v.NetworkMode = "host" }, "bind": func(v *Inspection) { v.Binds = []string{"/host:/tmp"} },
		"port":    func(v *Inspection) { v.PortBindings = map[string]json.RawMessage{"80/tcp": json.RawMessage(`[]`)} },
		"restart": func(v *Inspection) { v.Restart = "always" }, "logs": func(v *Inspection) { v.LogConfig["max-file"] = "100" },
		"tmpfs duplicate option": func(v *Inspection) { v.Tmpfs["/tmp"] = "rw,noexec,nosuid,nodev,rw" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			actual, expected, limits := inspectionFixture()
			mutate(&actual)
			if MatchesConfigured(actual, expected, limits) {
				t.Fatal("drift accepted")
			}
		})
	}
}

func TestHardeningNextCacheAndRealizedNetwork(t *testing.T) {
	for _, test := range []struct {
		name                 string
		networks             map[string]NetworkAttachment
		configured, realized bool
	}{
		{"unrealized nil", nil, true, false},
		{"unrealized empty", map[string]NetworkAttachment{}, true, false},
		{"foreign only", map[string]NetworkAttachment{"foreign": {Aliases: []string{"web-blue"}}}, false, false},
		{"expected and foreign", map[string]NetworkAttachment{"network": {Aliases: []string{"web-blue"}}, "foreign": {}}, false, false},
		{"expected nil aliases", map[string]NetworkAttachment{"network": {}}, true, false},
		{"expected empty aliases", map[string]NetworkAttachment{"network": {Aliases: []string{}}}, true, false},
		{"foreign alias", map[string]NetworkAttachment{"network": {Aliases: []string{"foreign"}}}, false, false},
		{"expected and Docker aliases", map[string]NetworkAttachment{"network": {Aliases: []string{"web-blue", "docker-id"}}}, true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			actual, expected, limits := inspectionFixture()
			actual.Networks = test.networks
			if MatchesConfigured(actual, expected, limits) != test.configured || MatchesRunning(actual, expected, limits) != test.realized {
				t.Fatal("unexpected configured or realized network result")
			}
		})
	}
	for _, actualReceipt := range []string{"", "receipt"} {
		t.Run("missing expected receipt actual="+actualReceipt, func(t *testing.T) {
			actual, expected, limits := inspectionFixture()
			expected.ReceiptID = ""
			actual.Labels[ReceiptLabel] = actualReceipt
			actual.Networks = map[string]NetworkAttachment{"network": {Aliases: []string{"web-blue"}}}
			if MatchesConfigured(actual, expected, limits) || MatchesRunning(actual, expected, limits) {
				t.Fatal("candidate matcher omitted receipt authority")
			}
		})
	}
	actual, expected, limits := inspectionFixture()
	expected.Technology = "nextjs"
	if MatchesConfigured(actual, expected, limits) {
		t.Fatal("missing cache policy accepted")
	}
	actual.Tmpfs["/workspace/.next/cache"] = "rw,noexec,nosuid,nodev,uid=1000,gid=1000,mode=0700,size=33554432"
	if !MatchesConfigured(actual, expected, limits) {
		t.Fatal("configured cache policy rejected")
	}
	actual.Mounts = []Mount{{Type: "tmpfs", Destination: "/workspace/.next/cache"}}
	actual.Networks = map[string]NetworkAttachment{expected.NetworkName: {Aliases: []string{expected.NetworkAlias}}}
	if !MatchesRunning(actual, expected, limits) {
		t.Fatal("realized network/cache rejected")
	}
	actual.Networks["other"] = NetworkAttachment{Aliases: []string{expected.NetworkAlias}}
	if MatchesRunning(actual, expected, limits) {
		t.Fatal("extra network accepted")
	}
	delete(actual.Networks, "other")
	actual.Networks[expected.NetworkName] = NetworkAttachment{Aliases: []string{"foreign"}}
	if MatchesRunning(actual, expected, limits) {
		t.Fatal("foreign alias accepted")
	}
}

func TestPhysicalStateNeverInfersStartFromRunningRecord(t *testing.T) {
	for _, state := range []string{"", "running", "exited", "paused", "restarting", "dead", "removing"} {
		t.Run(state, func(t *testing.T) {
			actual, _, _ := inspectionFixture()
			actual.Status = state
			if NeverStarted(actual) || CurrentlyRunning(actual) {
				t.Fatal("incomplete state accepted")
			}
		})
	}
	for _, stamp := range []string{"", "invalid", "2026-10-20T00:00:00Z"} {
		actual, _, _ := inspectionFixture()
		actual.StartedAt = stamp
		if NeverStarted(actual) {
			t.Fatal("former or unknown start accepted")
		}
	}
	for _, mutate := range []func(*Inspection){func(v *Inspection) { v.Paused = true }, func(v *Inspection) { v.Restarting = true }, func(v *Inspection) { v.Dead = true }, func(v *Inspection) { v.ExitCode = 1 }} {
		actual, _, _ := inspectionFixture()
		actual.Status = "running"
		actual.Running = true
		actual.StartedAt = "2026-10-20T00:00:00Z"
		mutate(&actual)
		if CurrentlyRunning(actual) {
			t.Fatal("contradictory running state accepted")
		}
	}
}
