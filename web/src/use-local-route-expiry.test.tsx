import { act, cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { useLocalRouteExpiry } from "./use-local-route-expiry";

afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

it("re-renders once when a route observation expires", () => {
  vi.useFakeTimers();
  vi.setSystemTime(new Date("2026-09-24T19:00:00Z"));
  let renders = 0;
  function Probe() {
    useLocalRouteExpiry("2026-09-24T19:00:00Z");
    renders++;
    return <span>Renders {renders}</span>;
  }
  render(<Probe/>);
  expect(screen.getByText("Renders 1")).toBeTruthy();
  act(() => vi.advanceTimersByTime(60_001));
  expect(screen.getByText("Renders 2")).toBeTruthy();
});
