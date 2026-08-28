import { afterEach, describe, expect, it, vi } from "vitest";
import { DASHBOARD_CAUGHT_ERROR_MESSAGE } from "./root-errors";

const rootHarness = vi.hoisted(() => ({
  createRoot: vi.fn(),
  render: vi.fn(),
}));

vi.mock("react-dom/client", () => ({ createRoot: rootHarness.createRoot }));

describe("dashboard root error handling", () => {
  afterEach(() => {
    document.body.replaceChildren();
    vi.restoreAllMocks();
  });

  it("wires a caught-error callback that logs only the fixed generic message", async () => {
    document.body.innerHTML = '<div id="root"></div>';
    rootHarness.createRoot.mockReturnValue({ render: rootHarness.render });
    const consoleError = vi.spyOn(console, "error").mockImplementation(() => {});

    await import("./main");

    expect(rootHarness.createRoot).toHaveBeenCalledTimes(1);
    expect(rootHarness.render).toHaveBeenCalledTimes(1);
    const options = rootHarness.createRoot.mock.calls[0][1] as { onCaughtError: (error: unknown, errorInfo: unknown) => void };
    const hostile = new Error("gho_rootToken https://cdn.attacker.example/relay-management.js");
    options.onCaughtError(hostile, { componentStack: hostile.stack });
    expect(consoleError).toHaveBeenCalledTimes(1);
    expect(consoleError.mock.calls[0]).toEqual([DASHBOARD_CAUGHT_ERROR_MESSAGE]);
    const logged = JSON.stringify(consoleError.mock.calls);
    expect(logged).not.toContain("gho_rootToken");
    expect(logged).not.toContain("cdn.attacker.example");
    expect(logged).not.toContain("Error:");
  });
});
