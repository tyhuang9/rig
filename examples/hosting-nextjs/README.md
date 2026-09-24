# Next.js generated-runtime fixture

This pinned public-package fixture exercises a production Next.js server. The
reviewed generated plan uses Node 24, npm with `npm ci`, `npm run build`, and
`npm run start` from the repository root. It is a server component on port
3000 with `/api/health` as its readiness path.

`NEXT_PUBLIC_BUILD_MARKER` is a public build input. The runtime-only
`RIG_FIXTURE_RUNTIME_SECRET` belongs in a scoped server secret; `/api/runtime`
reports only whether it is present. Never place that secret in a build input.

The page includes one local PNG served through Next.js image optimization so
the hosted gate can inspect actual runtime cache behavior. This fixture does
not use managed storage. Its cache is disposable per container; application
data, when needed, remains external and owned by the application.
