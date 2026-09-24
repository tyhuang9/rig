# M2 Next.js runtime cache qualification

**Branch:** `feature/hosting-m2-next-cache`, stacked on unmerged
[draft PR #73](https://github.com/tyhuang9/rig/pull/73). This is an M2
implementation slice; it is not M2 acceptance. The branch is local and
unpublished while hosted Docker evidence is pending.

## Contract

An accepted versioned Next.js server carries its immutable technology through
candidate creation and restart reconstruction. A Next.js candidate receives
the existing bounded `/tmp` mount and a separate 32 MiB tmpfs at its exact
`<component root>/.next/cache` path. The cache mount is writable only to the
container's non-root `node` user; the root filesystem stays read-only, and no
host path is mounted. Created, started, and recovered candidates must match
this exact Docker configuration. Other technologies retain the one-mount
policy. Docker option delimiters and unknown technologies are rejected before
mutation.

The generated image recipe checks the selected root, `.next`, and cache path
after the application's install/build commands, then checks them again in the
runtime stage before switching to `USER node`. Any symlink or missing parent
fails the image build. The recipe creates only an absent final cache leaf.

The pinned `examples/hosting-nextjs` fixture uses a real Next.js 16.3.6 npm
lockfile, a public build marker, a runtime-only secret-presence endpoint,
dynamic health endpoint, and local image optimization. The hosted gate builds
it with the generated recipe, then separately checks a real Next.js server
through the production runtime engine and Caddy for hardening, image-cache
write/reuse, restart ownership, blue/green replacement, and cleanup. The
server/Caddy gate uses its own labeled fixture image; it does not claim to
exercise controller persistence or the generated compiler. Those boundaries
have separate hosted gates.

## Verification evidence

| Check | Result |
| --- | --- |
| Generated image, plan, executor, and runtime unit tests | Focused `go test -p 1 -count=1 ./internal/generatedimage ./internal/deploymentplans ./internal/generatedexecutor ./internal/generatedruntime` passed with a writable temp Go cache. This includes missing or altered tmpfs, invalid technology/root, build-output symlinks, and candidate technology propagation. |
| Tagged hosted-test compilation | `go test -p 1 -tags live_docker -run '^$' ./cmd/hostd ./internal/generatedimage` passed. |
| Real local fixture build and responses | `npm ci` and `NEXT_PUBLIC_BUILD_MARKER=next-public-A npm run build` passed, including a rebuild after adding the blue/green runtime marker. Local `next start` served the page, dynamic health, runtime-secret presence without exposing its value, and an optimized PNG with MISS then HIT. This did not use a read-only Docker rootfs. |
| Full repository Go suite | `go test -p 1 -count=1 -timeout=15m ./...` passed with normal Windows workspace access. An initial sandboxed run failed existing Compose tests on file-access denials; the same Compose package and full suite passed outside that sandbox. |
| Static and web checks | `go vet ./...`, `go run ./cmd/openapi-gen -check`, and `pnpm --dir web test` (400/400) passed. The docs VitePress build, accessibility check, and Pages workflow check passed with lockfile-installed dependencies. |
| Hosted generated-recipe image and real runtime/Caddy gate | Pending publication and hosted Docker CI. The local Windows host has no Docker CLI. CI requires explicit pass events for both named live tests and detects leftover fixture resources. |

The cache is temporary per container and can fill. Durable ISR output,
shared cache coordination across slots, and persistent local uploads are not
part of this recipe. Applications needing them must configure their own
external storage or cache. Rig does not provision a database or cache.

M2 also still requires the remaining Node/Vite/pnpm/Yarn/workspace/manual
recipe matrix and a separate live GitHub authorization/archive walkthrough.
No merge or deployment is authorized by this record.
