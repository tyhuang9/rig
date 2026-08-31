package generatedimage

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/hostd/hostd/internal/deploymentplans"
)

const CompilerVersion = "generated-node-v2"

var nodeImages = map[string]string{
	"20": "node:20-bookworm-slim@sha256:2cf067cfed83d5ea958367df9f966191a942351a2df77d6f0193e162b5febfc0",
	"22": "node:22-bookworm-slim@sha256:83f487e0a63425e5b4d146fb5e5be574bcbe1b7b843d3ebafdd95eaf7767a7e5",
	"24": "node:24-bookworm-slim@sha256:ba849c60be29959425b8734d57b8b4b7d56f98edd9504c9af091d5281095a71e",
}

type componentDefinition struct {
	name            string
	role            string
	rootDirectory   string
	packageManager  string
	installBehavior string
	buildCommand    string
	runCommand      string
	nodeVersion     string
	internalPort    uint16
	healthProbe     string
	baseImage       string
}

type digestDefinition struct {
	CompilerVersion string `json:"compilerVersion"`
	PlanDigest      string `json:"planDigest"`
	Component       string `json:"component"`
	Role            string `json:"role"`
	RootDirectory   string `json:"rootDirectory"`
	PackageManager  string `json:"packageManager"`
	InstallBehavior string `json:"installBehavior"`
	BuildCommand    string `json:"buildCommand"`
	RunCommand      string `json:"runCommand"`
	NodeVersion     string `json:"nodeVersion"`
	InternalPort    uint16 `json:"internalPort"`
	HealthProbe     string `json:"healthProbe"`
	BaseImage       string `json:"baseImage"`
	RecipeDigest    string `json:"recipeDigest"`
}

func definitionFor(revision deploymentplans.DeploymentPlanRevision, componentName string) (componentDefinition, string, error) {
	if revision.Plan.Strategy != deploymentplans.StrategyGeneratedNode || revision.CanonicalDigest == "" {
		return componentDefinition{}, "", errors.New("generated plan required")
	}
	var component *deploymentplans.Component
	for index := range revision.Plan.Components {
		if revision.Plan.Components[index].Name == componentName {
			component = &revision.Plan.Components[index]
			break
		}
	}
	if component == nil {
		return componentDefinition{}, "", errors.New("component unavailable")
	}
	major := strings.SplitN(strings.TrimPrefix(component.NodeVersion, "v"), ".", 2)[0]
	base, supported := nodeImages[major]
	if !supported {
		return componentDefinition{}, "", errors.New("unsupported node version")
	}
	definition := componentDefinition{
		name: component.Name, role: component.Role, rootDirectory: component.RootDirectory,
		packageManager: component.PackageManager, installBehavior: component.InstallBehavior,
		buildCommand: component.BuildCommand, runCommand: component.RunCommand,
		nodeVersion: component.NodeVersion, internalPort: component.InternalPort,
		healthProbe: component.HealthProbe, baseImage: base,
	}
	recipe := containerfile(definition.buildCommand != "", definition.packageManager != "npm", base)
	recipeSum := sha256.Sum256([]byte(recipe + entrypointScript + staticLauncherScript + staticServerScript))
	canonical, err := json.Marshal(digestDefinition{
		CompilerVersion: CompilerVersion, PlanDigest: revision.CanonicalDigest, Component: definition.name,
		Role: definition.role, RootDirectory: definition.rootDirectory, PackageManager: definition.packageManager,
		InstallBehavior: definition.installBehavior, BuildCommand: definition.buildCommand,
		RunCommand: definition.runCommand, NodeVersion: definition.nodeVersion, InternalPort: definition.internalPort,
		HealthProbe: definition.healthProbe, BaseImage: definition.baseImage, RecipeDigest: hex.EncodeToString(recipeSum[:]),
	})
	if err != nil {
		return componentDefinition{}, "", err
	}
	digest := sha256.Sum256(canonical)
	return definition, hex.EncodeToString(digest[:]), nil
}

func containerfile(hasBuild, enableCorepack bool, baseImage string) string {
	corepack := ""
	if enableCorepack {
		corepack = "RUN [\"corepack\", \"enable\"]\n"
	}
	build := ""
	if hasBuild {
		build = "RUN --mount=type=secret,id=rig-build-command,required=true [\"/bin/sh\", \"-c\", \"root=$(cat /run/rig/root.path); cd -- \\\"/workspace/$root\\\" && exec /bin/sh -lc \\\"$(cat /run/secrets/rig-build-command)\\\"\"]\n"
	}
	return fmt.Sprintf(`FROM %s AS builder
%sWORKDIR /workspace
COPY --chown=node:node source/ /workspace/
COPY --chown=node:node rig/root.path /run/rig/root.path
USER node
RUN --mount=type=secret,id=rig-install-command,required=true ["/bin/sh", "-c", "root=$(cat /run/rig/root.path); cd -- \"/workspace/$root\" && exec /bin/sh -lc \"$(cat /run/secrets/rig-install-command)\""]
%sFROM %s AS runtime
ENV NODE_ENV=production
WORKDIR /workspace
COPY --from=builder --chown=node:node /workspace/ /workspace/
COPY --chmod=0555 rig/rig-entrypoint /usr/local/bin/rig-entrypoint
COPY --chmod=0555 rig/rig-static /usr/local/bin/rig-static
COPY --chmod=0444 rig/rig-static.mjs /usr/local/lib/rig/static.mjs
USER node
ENTRYPOINT ["/usr/local/bin/rig-entrypoint"]
`, baseImage, corepack, build, baseImage)
}

func writeRecipe(layout buildLayout, definition componentDefinition) error {
	if err := writeBuildFile(filepath.Join(layout.contextDirectory, "rig", "root.path"), []byte(definition.rootDirectory), 0o600); err != nil {
		return err
	}
	return writeBuildFile(layout.containerfile, []byte(containerfile(definition.buildCommand != "", definition.packageManager != "npm", definition.baseImage)), 0o600)
}

const entrypointScript = `#!/bin/sh
set -eu
if [ "$#" -lt 1 ]; then
  echo "Rig runtime command is missing" >&2
  exit 64
fi
exec "$@"
`

const staticLauncherScript = `#!/bin/sh
set -eu
exec node /usr/local/lib/rig/static.mjs "$@"
`

const staticServerScript = `import { createReadStream } from "node:fs";
import { lstat, realpath } from "node:fs/promises";
import { createServer } from "node:http";
import { extname, resolve, sep } from "node:path";

const args = process.argv.slice(2);
const value = (flag, fallback) => {
  const index = args.indexOf(flag);
  return index >= 0 && args[index + 1] ? args[index + 1] : fallback;
};
const root = resolve(process.cwd(), value("--root", "dist"));
const port = Number(value("--port", "8080"));
if (!Number.isInteger(port) || port < 1 || port > 65535) process.exit(64);
const rootInfo = await lstat(root).catch(() => null);
if (!rootInfo?.isDirectory() || rootInfo.isSymbolicLink()) process.exit(66);
const realRoot = await realpath(root);
const types = { ".css": "text/css; charset=utf-8", ".html": "text/html; charset=utf-8", ".js": "text/javascript; charset=utf-8", ".json": "application/json; charset=utf-8", ".svg": "image/svg+xml", ".txt": "text/plain; charset=utf-8" };
const safeFile = async (candidate) => {
  const canonical = await realpath(candidate);
  if (canonical !== realRoot && !canonical.startsWith(realRoot + sep)) throw new Error("escape");
  const info = await lstat(canonical);
  if (!info.isFile() || info.isSymbolicLink()) throw new Error("not-file");
  return canonical;
};
createServer(async (request, response) => {
  try {
    const pathname = decodeURIComponent(new URL(request.url ?? "/", "http://rig.local").pathname);
    let file = resolve(root, ` + "`" + `.${pathname}` + "`" + `);
    if (file !== root && !file.startsWith(root + sep)) throw new Error("escape");
    const info = await lstat(file).catch(() => null);
    if (info?.isDirectory()) file = resolve(file, "index.html");
    file = await safeFile(file).catch(() => safeFile(resolve(root, "index.html")));
    response.writeHead(200, { "content-type": types[extname(file)] ?? "application/octet-stream", "x-content-type-options": "nosniff" });
    createReadStream(file).pipe(response);
  } catch { response.writeHead(404, { "content-type": "text/plain; charset=utf-8" }); response.end("Not found"); }
}).listen(port, "0.0.0.0");
`
