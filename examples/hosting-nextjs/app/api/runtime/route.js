export const dynamic = "force-dynamic";

export function GET() {
  const configured = Boolean(process.env.RIG_FIXTURE_RUNTIME_SECRET);
  return Response.json({ runtimeConfigured: configured, runtimeMarker: process.env.RIG_FIXTURE_SLOT_MARKER ?? null, version: "next-fixture-1" }, {
    status: configured ? 200 : 503,
    headers: { "Cache-Control": "no-store" },
  });
}
