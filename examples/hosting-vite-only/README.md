# Vite-only Yarn hosting fixture

This single-package Yarn 4 fixture exercises Rig's generated static recipe
without an API or workspace. It pins Vite and its real public dependencies in
`yarn.lock`. The checked-in `.yarnrc.yml` selects `node-modules`; the generated
`.yarn/install-state.gz` is local install state and is excluded from Rig source
identity and image staging. Other checked-in `.yarn` content remains eligible.

Install with `corepack yarn install --immutable` and build with
`VITE_BUILD_MARKER=vite-public-A corepack yarn build`. The output in `dist` is
served by Rig's static server on port 8080. The marker is public build input;
runtime secrets must never be added to Vite build values or emitted assets.
