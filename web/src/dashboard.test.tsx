import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, render, screen } from "@testing-library/react";
import { readFileSync } from "node:fs";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "./api";
import { MachinesPage } from "./dashboard";
import { DASHBOARD_CAUGHT_ERROR_MESSAGE, handleDashboardCaughtError } from "./root-errors";

const unavailableRelayStatus = {
  availability: "unavailable",
  state: "offline",
  paused: false,
  outcome: "unavailable",
  diagnosticsUnavailable: false,
  pendingCommands: 0,
  activeLeases: 0,
  expiredLeases: 0,
  oldestPendingAgeSeconds: 0,
  observerDropped: 0,
  readModelAvailable: false,
  removableBindings: [],
  keyRotation: { inProgress: false },
};

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<T>((done, fail) => { resolve = done; reject = fail; });
  return { promise, resolve, reject };
}

type RelayPanelLoader = NonNullable<Parameters<typeof MachinesPage>[0]["relayPanelLoader"]>;

function renderPage(role: string, relayPanelLoader?: RelayPanelLoader) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(<QueryClientProvider client={client}><MachinesPage role={role} relayPanelLoader={relayPanelLoader}/></QueryClientProvider>, { onCaughtError: handleDashboardCaughtError });
}

describe("MachinesPage relay integration", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
    vi.spyOn(api, "relayStatus").mockResolvedValue(unavailableRelayStatus as never);
  });
  afterEach(cleanup);

  it("renders viewer-safe relay management independently while machines are loading", async () => {
    const machines = deferred<unknown>();
    vi.spyOn(api, "machines").mockReturnValue(machines.promise as never);
    renderPage("viewer");
    expect(screen.getByText("Loading content")).not.toBeNull();
    const relayLoading = screen.getByText("Loading relay management");
    expect(relayLoading.closest('[role="status"]')?.getAttribute("aria-busy")).toBe("true");
    expect(await screen.findByText("GitHub deployment relay")).not.toBeNull();
    expect(screen.queryByText("Loading relay management")).toBeNull();
    expect(screen.queryByRole("button", { name: "Rotate controller key" })).toBeNull();
    await act(async () => machines.resolve({ items: [] }));
  });

  it("keeps relay management available when the machine request fails", async () => {
    vi.spyOn(api, "machines").mockRejectedValue(new Error("machine inventory offline"));
    renderPage("administrator");
    expect((await screen.findByRole("alert")).textContent).toContain("machine inventory offline");
    expect(screen.getByText("GitHub deployment relay")).not.toBeNull();
    expect(screen.getByRole("button", { name: "Rotate controller key" })).not.toBeNull();
  });

  it("renders the controller-global relay panel after an empty machine result", async () => {
    vi.spyOn(api, "machines").mockResolvedValue({ items: [] } as never);
    const view = renderPage("viewer");
    expect(await screen.findByText("GitHub deployment relay")).not.toBeNull();
    expect(view.container.querySelectorAll("article.machine")).toHaveLength(0);
  });

  it("keeps admin machine content intact while relay status loads and enables idle rotation after resolution", async () => {
    const relayStatus = deferred<unknown>();
    vi.mocked(api.relayStatus).mockReturnValue(relayStatus.promise as never);
    vi.spyOn(api, "machines").mockResolvedValue({ items: [{ id: "machine-1", name: "Controller host", hostname: "rig.local", os: "linux", architecture: "amd64", status: "ready", composeVersion: "2", dockerVersion: "28", resources: {} }] } as never);
    renderPage("administrator");

    expect(await screen.findByRole("heading", { name: "Controller host" })).not.toBeNull();
    expect((await screen.findByText("Loading relay status")).closest('[role="status"]')?.getAttribute("aria-busy")).toBe("true");
    expect(screen.queryByText("Relay management controls could not be loaded. Machine information remains available.")).toBeNull();
    expect((screen.getByRole("button", { name: "Rotate controller key" }) as HTMLButtonElement).disabled).toBe(true);

    await act(async () => relayStatus.resolve({
      availability: "available",
      state: "ready",
      paused: false,
      outcome: "ready",
      diagnosticsUnavailable: false,
      pendingCommands: 0,
      activeLeases: 0,
      expiredLeases: 0,
      oldestPendingAgeSeconds: 0,
      observerDropped: 0,
      readModelAvailable: true,
      removableBindings: [],
      keyRotation: { inProgress: false },
    }));

    expect(await screen.findByText("No removable relay bindings.")).not.toBeNull();
    expect((screen.getByRole("button", { name: "Rotate controller key" }) as HTMLButtonElement).disabled).toBe(false);
    expect(screen.getByRole("heading", { name: "Controller host" })).not.toBeNull();
    expect(screen.queryByText("Loading relay status")).toBeNull();
    expect(screen.queryByText("Relay management controls could not be loaded. Machine information remains available.")).toBeNull();
  });

  it("keeps machine results intact and retries a rejected relay chunk without leaking its error", async () => {
    const firstAttempt = deferred<{ default: React.ComponentType<{ role: string }> }>();
    const retryAttempt = deferred<{ default: React.ComponentType<{ role: string }> }>();
    const loader = vi.fn<RelayPanelLoader>()
      .mockReturnValueOnce(firstAttempt.promise)
      .mockReturnValueOnce(retryAttempt.promise);
    vi.spyOn(api, "machines").mockResolvedValue({ items: [{ id: "machine-1", name: "Controller host", hostname: "rig.local", os: "linux", architecture: "amd64", status: "ready", composeVersion: "2", dockerVersion: "28", resources: {} }] } as never);
    const consoleError = vi.spyOn(console, "error").mockImplementation(() => {});
    renderPage("administrator", loader);
    expect(await screen.findByRole("heading", { name: "Controller host" })).not.toBeNull();
    expect(screen.getByText("Loading relay management").closest('[role="status"]')?.getAttribute("aria-busy")).toBe("true");

    await act(async () => firstAttempt.reject(new Error("gho_chunkToken https://cdn.attacker.example/relay-management.js")));
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("Relay management controls could not be loaded. Machine information remains available.");
    expect(alert.textContent).not.toContain("gho_chunkToken");
    expect(alert.textContent).not.toContain("cdn.attacker.example");
    expect(screen.getByRole("heading", { name: "Controller host" })).not.toBeNull();
    expect(screen.getAllByRole("alert")).toHaveLength(1);
    expect(consoleError).toHaveBeenCalledTimes(1);
    expect(consoleError).toHaveBeenCalledWith(DASHBOARD_CAUGHT_ERROR_MESSAGE);
    expect(consoleError.mock.calls[0]).toEqual([DASHBOARD_CAUGHT_ERROR_MESSAGE]);
    expect(JSON.stringify(consoleError.mock.calls)).not.toContain("gho_chunkToken");
    expect(JSON.stringify(consoleError.mock.calls)).not.toContain("cdn.attacker.example");

    const retry = screen.getByRole("button", { name: "Retry relay management" });
    const css = readFileSync("src/styles.css", "utf8");
    expect(css).toContain(".relay-chunk-error .button { min-height: 44px; justify-self: start; }");
    expect(css).toContain(".button:focus-visible");
    retry.focus();
    await act(async () => { retry.click(); retry.click(); });
    expect(loader).toHaveBeenCalledTimes(2);
    const retryLoading = screen.getByText("Loading relay management");
    expect(retryLoading.closest('[role="status"]')?.getAttribute("aria-busy")).toBe("true");
    expect(screen.queryByRole("alert")).toBeNull();
    expect(screen.getAllByText("Loading relay management")).toHaveLength(1);

    await act(async () => retryAttempt.resolve({ default: ({ role }) => <section aria-label="Loaded relay management">Relay controls loaded for {role}</section> }));
    expect(await screen.findByText("Relay controls loaded for administrator")).not.toBeNull();
    expect(screen.queryByText("Loading relay management")).toBeNull();
    expect(loader).toHaveBeenCalledTimes(2);
    expect(consoleError).toHaveBeenCalledTimes(1);
    expect(readFileSync("src/main.tsx", "utf8")).toContain("onCaughtError: handleDashboardCaughtError");
    consoleError.mockRestore();
  });
});
