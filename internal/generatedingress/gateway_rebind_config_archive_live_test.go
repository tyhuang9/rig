package generatedingress

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"testing"
	"time"

	runtimeprocess "github.com/hostd/hostd/internal/runtime/process"
)

// A failed hosted archive check must expose its representation without logging
// config contents, arbitrary paths, Docker identifiers or command stderr.
func logLiveGatewayRebindInitialConfigArchive(t *testing.T, fixture *liveGatewayV2Fixture,
	intent gatewayRebindProtectedIntent, stage gatewayRebindStageIntent,
) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	observed, err := (managerGatewayRebindStageContainerDriver{manager: fixture.ingress}).inspect(ctx, intent)
	if err != nil || stage.StageContainer == nil ||
		!validGatewayRebindStageContainerObservation(intent, stage, observed, stage.StageContainer) {
		t.Log("initial config archive: exact stopped-container ownership unavailable; diagnostic read withheld")
		return
	}
	result, err := fixture.ingress.runner.Run(ctx, runtimeprocess.CommandRequest{
		Executable: fixture.ingress.options.DockerExecutable,
		Args:       []string{"container", "cp", stage.StageContainer.ID + ":/config/.", "-"},
		Directory:  fixture.ingress.options.WorkingDirectory, Env: append([]string(nil), fixture.ingress.dockerEnv...),
		Timeout: fixture.ingress.options.CommandTimeout, OutputLimit: defaultOutputLimit,
	})
	defer clearResult(&result)
	t.Logf("initial config archive: read_ok=%t bytes=%d stdout_truncated=%t stderr_truncated=%t stderr_present=%t",
		err == nil, len(result.Stdout), result.StdoutTruncated, result.StderrTruncated, len(result.Stderr) != 0)
	if err != nil || result.StdoutTruncated || result.StderrTruncated || len(result.Stderr) != 0 {
		return
	}
	source := bytes.NewReader(result.Stdout)
	reader := tar.NewReader(source)
	for index := 0; index < 8; index++ {
		header, err := reader.Next()
		if err == io.EOF {
			t.Logf("initial config archive: end=true entries=%d consumed=%d", index, len(result.Stdout)-source.Len())
			return
		}
		if err != nil || header == nil {
			t.Logf("initial config archive: parse_ok=false entry=%d consumed=%d", index, len(result.Stdout)-source.Len())
			return
		}
		category := "other"
		switch header.Name {
		case ".", "./":
			category = "root"
		case "caddy/", "./caddy/":
			category = "caddy-directory"
		case "stage.json", "./stage.json":
			category = "stage-config"
		case "active.json", "./active.json":
			category = "active-config"
		}
		t.Logf("initial config archive: entry=%d category=%s type=%d size=%d uid=%d gid=%d mode=%o link_present=%t pax_fields=%d consumed=%d",
			index, category, header.Typeflag, header.Size, header.Uid, header.Gid, header.Mode, header.Linkname != "", len(header.PAXRecords), len(result.Stdout)-source.Len())
	}
	t.Log("initial config archive: diagnostic entry limit reached")
}
