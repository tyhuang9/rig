# Standalone Node API hosting fixture

This single-package npm fixture exercises Rig's generated Node server recipe
without a workspace or frontend. It uses a real locked Express dependency.
`/health` reports readiness and `/version` reports the immutable fixture
version, a harmless runtime slot marker, and only the presence of a synthetic
runtime secret. The listener binds to all container interfaces on `PORT`
(default 3000), so a Docker-published port can reach it.

Install with `npm ci` and start with `npm start`. `rig-setup.json` records the
reviewed generated setup. Do not put real credentials in this fixture; a hosted
test passes a synthetic runtime-only value and verifies that its value never
appears in the HTTP response or generated image metadata.
