package generatedingress

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestGatewayRebindFinalConfigAutosavePoliciesRequireTheExactPermittedSnapshot(t *testing.T) {
	stage, active := []byte(`{"z":true,"a":1}`), []byte(`{"z":false,"a":2}`)
	stageSave, activeSave := []byte(`{"a":1,"z":true}`), []byte(`{"a":2,"z":false}`)
	for _, test := range []struct {
		name                        string
		mode                        gatewayRebindFinalConfigAutosaveMode
		stageAllowed, activeAllowed bool
	}{
		{"stage only", gatewayRebindFinalConfigStageAutosave, true, false},
		{"active only", gatewayRebindFinalConfigActiveAutosave, false, true},
		{"uncertain cutover", gatewayRebindFinalConfigEitherAutosave, true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, save := range []struct {
				name    string
				body    []byte
				allowed bool
			}{
				{"stage", stageSave, test.stageAllowed}, {"active", activeSave, test.activeAllowed},
				{"foreign", []byte(`{"a":3,"z":false}`), false},
				{"noncanonical stage", stage, false}, {"noncanonical active", active, false},
				{"trailing newline", append(append([]byte(nil), stageSave...), '\n'), false},
			} {
				t.Run(save.name, func(t *testing.T) {
					archive := finalConfigAutosaveArchive(t, stage, active, save.body, "stage.json", "active.json", "caddy/", "caddy/autosave.json")
					got, err := gatewayRebindExactFinalConfigVolumeArchiveWithAutosave(archive, stage, active, test.mode)
					if save.allowed {
						if err != nil || got != gatewayRebindFinalConfigInventoryExactPair {
							t.Fatalf("permitted canonical snapshot rejected: inventory=%d error=%v", got, err)
						}
					} else if err == nil || got != 0 {
						t.Fatal("unapproved or noncanonical autosave accepted")
					}
				})
			}
			without := finalConfigAutosaveArchive(t, stage, active, nil, "stage.json", "active.json", "caddy/")
			if got, err := gatewayRebindExactFinalConfigVolumeArchiveWithAutosave(without, stage, active, test.mode); err != nil || got != gatewayRebindFinalConfigInventoryExactPair {
				t.Fatal("optional autosave absence rejected")
			}
		})
	}
	for _, mode := range []gatewayRebindFinalConfigAutosaveMode{0, 255} {
		archive := finalConfigAutosaveArchive(t, stage, active, stageSave, "stage.json", "active.json", "caddy/", "caddy/autosave.json")
		if got, err := gatewayRebindExactFinalConfigVolumeArchiveWithAutosave(archive, stage, active, mode); err == nil || got != 0 {
			t.Fatal("invalid autosave policy accepted")
		}
	}
	stageOnly := finalConfigAutosaveArchive(t, stage, active, stageSave, "stage.json", "caddy/", "caddy/autosave.json")
	if got, err := gatewayRebindExactFinalConfigVolumeArchiveWithAutosave(stageOnly, stage, active, gatewayRebindFinalConfigStageAutosave); err != nil || got != gatewayRebindFinalConfigInventoryStageOnly {
		t.Fatal("stage-only autosave inventory rejected")
	}
	for _, mode := range []gatewayRebindFinalConfigAutosaveMode{gatewayRebindFinalConfigActiveAutosave, gatewayRebindFinalConfigEitherAutosave} {
		if got, err := gatewayRebindExactFinalConfigVolumeArchiveWithAutosave(stageOnly, stage, active, mode); err == nil || got != 0 {
			t.Fatal("final-container policy accepted inventory without active.json")
		}
	}
	if _, err := gatewayRebindExactStageConfigVolumeArchive(stageOnly, stage); err == nil {
		t.Fatal("legacy pre-start parser accepted autosave")
	}
	activeSnapshot := finalConfigAutosaveArchive(t, stage, active, activeSave, "stage.json", "active.json", "caddy/", "caddy/autosave.json")
	if got, err := gatewayRebindExactFinalConfigVolumeArchive(activeSnapshot, stage, active); err == nil || got != 0 {
		t.Fatal("sequence-eleven/twelve default inventory accepted an already-active snapshot")
	}
}

func TestGatewayRebindFinalConfigAutosaveRejectsAmbiguousHeadersAndArchives(t *testing.T) {
	stage, active, save := []byte(`{"stage":true}`), []byte(`{"active":true}`), []byte(`{"stage":true}`)
	root := tar.Header{Name: ".", Typeflag: tar.TypeDir}
	seed := tar.Header{Name: "caddy/", Typeflag: tar.TypeDir, Mode: 01777}
	stageHeader := tar.Header{Name: "stage.json", Typeflag: tar.TypeReg, Mode: 0600, Size: int64(len(stage))}
	activeHeader := tar.Header{Name: "active.json", Typeflag: tar.TypeReg, Mode: 0600, Size: int64(len(active))}
	good := tar.Header{Name: "caddy/autosave.json", Typeflag: tar.TypeReg, Mode: 0600, Uid: 1000, Gid: 1000, Size: int64(len(save))}
	archive := func(header tar.Header, body []byte) []byte {
		return gatewayRebindStageConfigCopyHeadersTar(t, []tar.Header{root, seed, stageHeader, activeHeader, header}, bytes.Join([][]byte{stage, active, body}, nil))
	}
	for _, test := range []struct {
		name   string
		change func(*tar.Header)
		body   []byte
	}{
		{"root owned", func(h *tar.Header) { h.Uid = 0 }, save},
		{"root group", func(h *tar.Header) { h.Gid = 0 }, save},
		{"world readable", func(h *tar.Header) { h.Mode = 0644 }, save},
		{"executable", func(h *tar.Header) { h.Mode = 0700 }, save},
		{"setuid", func(h *tar.Header) { h.Mode = 04600 }, save},
		{"symlink", func(h *tar.Header) { h.Typeflag = tar.TypeSymlink; h.Linkname = "../stage.json"; h.Size = 0 }, nil},
		{"hardlink", func(h *tar.Header) { h.Typeflag = tar.TypeLink; h.Linkname = "stage.json"; h.Size = 0 }, nil},
		{"PAX metadata", func(h *tar.Header) { h.PAXRecords = map[string]string{"comment": "unexpected"} }, save},
		{"nested file", func(h *tar.Header) { h.Name = "caddy/nested/autosave.json" }, save},
		{"parent alias", func(h *tar.Header) { h.Name = "caddy/../caddy/autosave.json" }, save},
		{"double dot slash", func(h *tar.Header) { h.Name = "././caddy/autosave.json" }, save},
		{"absolute path", func(h *tar.Header) { h.Name = "/caddy/autosave.json" }, save},
		{"empty snapshot", func(h *tar.Header) { h.Size = 0 }, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			header := good
			test.change(&header)
			if got, err := gatewayRebindExactFinalConfigVolumeArchiveWithAutosave(archive(header, test.body), stage, active, gatewayRebindFinalConfigStageAutosave); err == nil || got != 0 {
				t.Fatal("ambiguous autosave header accepted")
			}
		})
	}
	valid := archive(good, save)
	badPadding := append([]byte(nil), valid...)
	// Locate the actual payload; corrupt its padding without changing contents.
	dataAt := bytes.Index(badPadding, save)
	if dataAt < 0 {
		t.Fatal("fixture snapshot absent")
	}
	dataAt = bytes.LastIndex(badPadding, save)
	badPadding[dataAt+len(save)] = 1
	gnuBody := []byte("caddy/autosave.json\x00")
	gnu := gatewayRebindStageConfigCopyHeadersTar(t, []tar.Header{root, {Name: "././@LongLink", Typeflag: tar.TypeReg, Size: int64(len(gnuBody))}, seed, stageHeader, activeHeader, good}, bytes.Join([][]byte{gnuBody, stage, active, save}, nil))
	finalConfigDriverSetTarType(gnu[512:1024], tar.TypeGNULongName)
	for _, test := range []struct {
		name string
		body []byte
	}{
		{"missing seed directory", finalConfigAutosaveArchive(t, stage, active, save, "stage.json", "active.json", "caddy/autosave.json")},
		{"duplicate snapshot", finalConfigAutosaveArchive(t, stage, active, save, "stage.json", "active.json", "caddy/", "caddy/autosave.json", "caddy/autosave.json")},
		{"duplicate snapshot alias", finalConfigAutosaveArchive(t, stage, active, save, "stage.json", "active.json", "caddy/", "caddy/autosave.json", "./caddy/autosave.json")},
		{"duplicate seed", finalConfigAutosaveArchive(t, stage, active, save, "stage.json", "active.json", "caddy/", "./caddy/", "caddy/autosave.json")},
		{"missing stage", finalConfigAutosaveArchive(t, stage, active, save, "active.json", "caddy/", "caddy/autosave.json")},
		{"GNU metadata", gnu}, {"nonzero padding", badPadding},
		{"truncated payload", valid[:dataAt+len(save)-1]},
		{"missing terminator", valid[:len(valid)-512]},
		{"trailing data", append(append([]byte(nil), valid...), bytes.Repeat([]byte{1}, 512)...)},
		{"second archive", append(append([]byte(nil), valid...), valid...)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got, err := gatewayRebindExactFinalConfigVolumeArchiveWithAutosave(test.body, stage, active, gatewayRebindFinalConfigStageAutosave); err == nil || got != 0 {
				t.Fatal("ambiguous autosave archive accepted")
			}
		})
	}
}

func TestGatewayRebindFinalConfigAutosaveBoundsThreeConfigurationsAndArchive(t *testing.T) {
	config := func(value string) []byte {
		return []byte(`{"pad":"` + strings.Repeat(value, gatewayV2MaxConfigBytes-len(`{"pad":""}`)) + `"}`)
	}
	stage, active := config("s"), config("a")
	if len(stage) != 60<<10 || len(active) != 60<<10 || !json.Valid(stage) || !json.Valid(active) {
		t.Fatal("invalid maximum JSON fixture")
	}
	for _, mode := range []gatewayRebindFinalConfigAutosaveMode{gatewayRebindFinalConfigStageAutosave, gatewayRebindFinalConfigActiveAutosave, gatewayRebindFinalConfigEitherAutosave} {
		save := stage
		if mode == gatewayRebindFinalConfigActiveAutosave {
			save = active
		}
		archive := finalConfigAutosaveArchive(t, stage, active, save, "stage.json", "active.json", "caddy/", "caddy/autosave.json")
		if len(archive) <= 128<<10 || len(archive) > 192<<10 || gatewayRebindFinalConfigArchiveLimit != 192<<10 {
			t.Fatalf("unexpected bounded triple archive: %d", len(archive))
		}
		for _, value := range [][]byte{archive, append(append([]byte(nil), archive...), make([]byte, gatewayRebindFinalConfigArchiveLimit-len(archive))...)} {
			if got, err := gatewayRebindExactFinalConfigVolumeArchiveWithAutosave(value, stage, active, mode); err != nil || got != gatewayRebindFinalConfigInventoryExactPair {
				t.Fatalf("bounded maximum configuration rejected: %v", err)
			}
		}
		overflow := append(append([]byte(nil), archive...), make([]byte, gatewayRebindFinalConfigArchiveLimit-len(archive)+512)...)
		if got, err := gatewayRebindExactFinalConfigVolumeArchiveWithAutosave(overflow, stage, active, mode); err == nil || got != 0 {
			t.Fatal("archive overflow accepted")
		}
	}
	oversized := append(append([]byte(nil), stage...), ' ')
	archive := finalConfigAutosaveArchive(t, stage, active, oversized, "stage.json", "active.json", "caddy/", "caddy/autosave.json")
	if got, err := gatewayRebindExactFinalConfigVolumeArchiveWithAutosave(archive, stage, active, gatewayRebindFinalConfigStageAutosave); err == nil || got != 0 {
		t.Fatal("individual autosave overflow accepted")
	}
}
