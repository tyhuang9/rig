package generatedingress

import (
	"archive/tar"
	"testing"
)

func TestGatewayRebindExactFinalConfigVolumeArchiveStageAutosave(t *testing.T) {
	stage, active := []byte(`{"z":true,"a":1}`), []byte(`{"z":false,"a":2}`)
	autosave := []byte(`{"a":1,"z":true}`)
	for _, files := range [][]string{
		{"caddy/", "stage.json", "caddy/autosave.json"},
		{"caddy/", "stage.json", "active.json", "caddy/autosave.json"},
		{"active.json", "caddy/autosave.json", "caddy/", "stage.json"},
		{"./caddy/", "stage.json", "./caddy/autosave.json", "active.json"},
	} {
		archive := finalConfigAutosaveArchive(t, stage, active, autosave, files...)
		want := gatewayRebindFinalConfigInventoryStageOnly
		if len(files) == 4 {
			want = gatewayRebindFinalConfigInventoryExactPair
		}
		if got, err := gatewayRebindExactFinalConfigVolumeArchive(archive, stage, active); err != nil || got != want {
			t.Errorf("legitimate stage autosave rejected: inventory=%d want=%d error=%v", got, want, err)
		}
		if _, err := gatewayRebindExactStageConfigVolumeArchive(archive, stage); err == nil {
			t.Fatal("pre-start inventory accepted autosave")
		}
	}
}

func finalConfigAutosaveArchive(t *testing.T, stage, active, autosave []byte, names ...string) []byte {
	t.Helper()
	headers := []tar.Header{{Name: ".", Typeflag: tar.TypeDir}}
	var body []byte
	for _, name := range names {
		header := tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0600}
		var data []byte
		switch name {
		case "caddy/", "./caddy/":
			header.Typeflag, header.Mode = tar.TypeDir, 01777
		case "stage.json":
			data = stage
		case "active.json":
			data = active
		case "caddy/autosave.json", "./caddy/autosave.json":
			header.Uid, header.Gid = 1000, 1000
			data = autosave
		}
		header.Size = int64(len(data))
		headers = append(headers, header)
		body = append(body, data...)
	}
	return gatewayRebindStageConfigCopyHeadersTar(t, headers, body)
}
