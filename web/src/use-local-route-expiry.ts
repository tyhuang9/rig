import { useEffect, useState } from "react";
import { LOCAL_ROUTE_MAX_AGE_MS } from "./api";

// Verification is a snapshot. Re-render once when it ages out so a page left
// open cannot keep offering a route from an old Caddy or deployment state.
export function useLocalRouteExpiry(observedAt: string | undefined) {
  const [, refresh] = useState(0);
  useEffect(() => {
    const observedMs = Date.parse(observedAt ?? "");
    if (!Number.isFinite(observedMs)) return;
    const remainingMs = Math.max(0, observedMs + LOCAL_ROUTE_MAX_AGE_MS - Date.now());
    const timer = window.setTimeout(() => refresh((value) => value + 1), remainingMs + 1);
    return () => window.clearTimeout(timer);
  }, [observedAt]);
}
