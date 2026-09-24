export const dynamic = "force-dynamic";

export function GET() {
  return Response.json({ status: "ok", version: "next-fixture-1" }, {
    headers: { "Cache-Control": "no-store" },
  });
}
