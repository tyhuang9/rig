package generatedingress

import (
	"archive/tar"
	"bytes"
	"testing"
)

func TestGatewayRebindExactFinalConfigVolumeArchivePinnedImageDirectory(t *testing.T) {
	stage, active := []byte(`{"stage":true}`), []byte(`{"active":true}`)
	root := tar.Header{Name: ".", Typeflag: tar.TypeDir}
	seed := tar.Header{Name: "caddy/", Typeflag: tar.TypeDir, Mode: 01777}
	stageHeader := tar.Header{Name: "stage.json", Typeflag: tar.TypeReg, Size: int64(len(stage))}
	activeHeader := tar.Header{Name: "active.json", Typeflag: tar.TypeReg, Size: int64(len(active))}
	archiveFor := func(t *testing.T, headers ...tar.Header) []byte {
		t.Helper()
		var body []byte
		for _, header := range headers {
			switch header.Name {
			case "stage.json":
				body = append(body, stage...)
			case "active.json":
				body = append(body, active...)
			}
		}
		return gatewayRebindStageConfigCopyHeadersTar(t, append([]tar.Header{root}, headers...), body)
	}
	for _, name := range []string{"caddy/", "./caddy/"} {
		seed.Name = name
		for _, headers := range [][]tar.Header{
			{seed, stageHeader}, {stageHeader, seed},
			{seed, stageHeader, activeHeader}, {stageHeader, seed, activeHeader}, {stageHeader, activeHeader, seed},
			{seed, activeHeader, stageHeader}, {activeHeader, seed, stageHeader}, {activeHeader, stageHeader, seed},
		} {
			want := gatewayRebindFinalConfigInventoryStageOnly
			if len(headers) == 3 {
				want = gatewayRebindFinalConfigInventoryExactPair
			}
			got, err := gatewayRebindExactFinalConfigVolumeArchive(archiveFor(t, headers...), stage, active)
			if err != nil || got != want {
				t.Errorf("seed %q headers %v: inventory=%d want=%d error=%v", name, headers, got, want, err)
			}
		}
	}
	seed.Name = "caddy/"
	for name, mutate := range map[string]func(*tar.Header){
		"wrong mode":   func(h *tar.Header) { h.Mode = 0755 },
		"wrong owner":  func(h *tar.Header) { h.Uid = 1000 },
		"wrong group":  func(h *tar.Header) { h.Gid = 1000 },
		"regular file": func(h *tar.Header) { h.Typeflag, h.Name = tar.TypeReg, "caddy" },
		"symlink":      func(h *tar.Header) { h.Typeflag, h.Linkname = tar.TypeSymlink, "stage.json" },
		"hardlink":     func(h *tar.Header) { h.Typeflag, h.Linkname = tar.TypeLink, "stage.json" },
		"path alias":   func(h *tar.Header) { h.Name = "././caddy/" },
		"PAX metadata": func(h *tar.Header) { h.PAXRecords = map[string]string{"comment": "unexpected"} },
	} {
		t.Run(name, func(t *testing.T) {
			changed := seed
			mutate(&changed)
			if got, err := gatewayRebindExactFinalConfigVolumeArchive(archiveFor(t, stageHeader, changed, activeHeader), stage, active); err == nil || got != 0 {
				t.Fatal("unexpected image directory representation accepted")
			}
		})
	}
	for name, headers := range map[string][]tar.Header{
		"duplicate seed":  {stageHeader, seed, activeHeader, seed},
		"child file":      {seed, stageHeader, activeHeader, {Name: "caddy/extra", Typeflag: tar.TypeReg}},
		"child directory": {seed, stageHeader, activeHeader, {Name: "caddy/nested/", Typeflag: tar.TypeDir}},
		"missing stage":   {seed, activeHeader},
	} {
		t.Run(name, func(t *testing.T) {
			if got, err := gatewayRebindExactFinalConfigVolumeArchive(archiveFor(t, headers...), stage, active); err == nil || got != 0 {
				t.Fatal("ambiguous seeded inventory accepted")
			}
		})
	}
	valid := archiveFor(t, seed, stageHeader, activeHeader)
	for name, data := range map[string][]byte{
		"missing terminator": valid[:len(valid)-512],
		"trailing data":      append(append([]byte(nil), valid...), bytes.Repeat([]byte{1}, 512)...),
	} {
		t.Run(name, func(t *testing.T) {
			if got, err := gatewayRebindExactFinalConfigVolumeArchive(data, stage, active); err == nil || got != 0 {
				t.Fatal("incomplete or extended seeded archive accepted")
			}
		})
	}
}
