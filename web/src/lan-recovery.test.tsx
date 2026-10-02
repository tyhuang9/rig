import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { StrictMode } from "react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  APIError,
  api,
  type LANAppDisableRead,
  type LANAppGrantRead,
  type LANRecoveryHead,
  type SystemStatus,
} from "./api";
import { App } from "./dashboard";
import {
  AuthenticatedOperatorGate,
  LANRecoveryScreen,
  NORMAL_STATUS_PROBE_TIMEOUT_MS,
  NORMAL_STATUS_RECHECK_MS,
} from "./lan-recovery";

const appId = "11111111-1111-4111-8111-111111111111";
const operationId = "22222222-2222-4222-8222-222222222222";
const ownerOperationId = "33333333-3333-4333-8333-333333333333";
const accessRevisionId = "44444444-4444-4444-8444-444444444444";
const allocationId = "55555555-5555-4555-8555-555555555555";
const approvalDigest = "a".repeat(64);

const grantHead: LANRecoveryHead = {
  kind: "lan_grant",
  appId,
  operationId,
  batch: true,
  batchPosition: 1,
  batchCount: 2,
  claimState: "uncertain",
  accessRevisionId,
  accessRevisionNumber: 7,
  allocationId,
  ownerOperationId,
  port: 8104,
  approvalDigest,
};

const grantRead: LANAppGrantRead = {
  claim: {
    attemptId: operationId,
    appId,
    accessRevisionId,
    accessRevisionNumber: 7,
    allocationId,
    ownerOperationId,
    port: 8104,
    state: "uncertain",
    updatedAt: "2026-10-01T10:00:00Z",
  },
  observed: { availability: "unknown" },
};

const disableHead: LANRecoveryHead = {
  ...grantHead,
  kind: "lan_disable",
  claimState: "withdrawing",
  batch: false,
  batchPosition: 1,
  batchCount: 1,
  approvalDigest: "b".repeat(64),
};

const disableRead: LANAppDisableRead = {
  claim: {
    operationId,
    appId,
    accessRevisionId,
    accessRevisionNumber: 7,
    allocationId,
    approvalDigest: disableHead.approvalDigest,
    port: 8104,
    state: "withdrawing",
    updatedAt: "2026-10-01T10:00:00Z",
  },
  observed: { availability: "withdrawn_pending_resolution" },
};

const normalStatus = {
  daemon: "ready",
  capabilities: {},
  diagnostics: {},
} as SystemStatus;

function recoveryRequired() {
  return new APIError({ status: 503, code: "gateway_reconciliation_required", detail: "Recovery required" });
}

function renderRecovery() {
  const onSignOut = vi.fn();
  const onCheckSystemStatus = vi.fn().mockResolvedValue(undefined);
  render(<LANRecoveryScreen onSignOut={onSignOut} onCheckSystemStatus={onCheckSystemStatus}/>);
  return { onSignOut, onCheckSystemStatus };
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<T>((done, fail) => { resolve = done; reject = fail; });
  return { promise, resolve, reject };
}

async function confirmAndSubmit(name: string) {
  const checkbox = await screen.findByRole("checkbox", { name: /I reviewed every recovery field/i });
  checkbox.focus();
  expect(document.activeElement).toBe(checkbox);
  expect(checkbox.tagName).toBe("INPUT");
  fireEvent.click(checkbox);
  const button = screen.getByRole("button", { name });
  expect(button.tagName).toBe("BUTTON");
  button.focus();
  expect(document.activeElement).toBe(button);
  fireEvent.click(button);
}

describe("authenticated operator mode gate", () => {
  beforeEach(() => vi.restoreAllMocks());
  afterEach(() => {
    cleanup();
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it("mounts normal content only after a fresh successful status probe", async () => {
    vi.spyOn(api, "status").mockResolvedValue(normalStatus);
    const recovery = vi.spyOn(api, "lanRecoveryHead");
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });

    render(<QueryClientProvider client={client}><AuthenticatedOperatorGate onSignOut={() => undefined}><div>Normal operator routes</div></AuthenticatedOperatorGate></QueryClientProvider>);

    expect(await screen.findByText("Normal operator routes")).not.toBeNull();
    expect(client.getQueryData(["system-status"])).toBe(normalStatus);
    expect(recovery).not.toHaveBeenCalled();
  });

  it("selects only the recovery screen for the exact recovery-gate error", async () => {
    vi.spyOn(api, "status").mockRejectedValue(recoveryRequired());
    vi.spyOn(api, "lanRecoveryHead").mockResolvedValue(grantHead);
    vi.spyOn(api, "lanGrant").mockResolvedValue(grantRead);
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });

    render(<QueryClientProvider client={client}><AuthenticatedOperatorGate onSignOut={() => undefined}><div>Normal operator routes</div></AuthenticatedOperatorGate></QueryClientProvider>);

    expect(await screen.findByRole("heading", { name: "LAN reconciliation required" })).not.toBeNull();
    expect(screen.queryByText("Normal operator routes")).toBeNull();
    expect(screen.queryByRole("navigation")).toBeNull();
  });

  it("keeps other status failures indeterminate until an explicit retry succeeds", async () => {
    const status = vi.spyOn(api, "status")
      .mockRejectedValueOnce(new APIError({ status: 503, code: "service_unavailable", detail: "Unavailable" }))
      .mockResolvedValueOnce(normalStatus);
    const onSignOut = vi.fn();
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });

    render(<QueryClientProvider client={client}><AuthenticatedOperatorGate onSignOut={onSignOut}><div>Normal operator routes</div></AuthenticatedOperatorGate></QueryClientProvider>);

    const heading = await screen.findByRole("heading", { name: "System status unavailable" });
    expect(document.activeElement).toBe(heading);
    expect(screen.queryByText("Normal operator routes")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    expect(await screen.findByText("Normal operator routes")).not.toBeNull();
    expect(status).toHaveBeenCalledTimes(2);
  });

  it.each([null, {}, { daemon: "ready", capabilities: null, diagnostics: {} }])(
    "keeps malformed successful system status indeterminate: %s", async (body) => {
      vi.spyOn(api, "status").mockResolvedValue(body as unknown as SystemStatus);
      const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
      render(<QueryClientProvider client={client}><AuthenticatedOperatorGate onSignOut={() => undefined}><div>Normal operator routes</div></AuthenticatedOperatorGate></QueryClientProvider>);

      expect(await screen.findByRole("heading", { name: "System status unavailable" })).not.toBeNull();
      expect(screen.queryByText("Normal operator routes")).toBeNull();
      expect(client.getQueryData(["system-status"])).toBeUndefined();
    },
  );

  it("hides an open normal console while a focus-return status proof is pending", async () => {
    const focusStatus = deferred<SystemStatus>();
    const status = vi.spyOn(api, "status")
      .mockResolvedValueOnce(normalStatus)
      .mockReturnValueOnce(focusStatus.promise);
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(<QueryClientProvider client={client}><AuthenticatedOperatorGate onSignOut={() => undefined}><main id="main" tabIndex={-1}>Normal operator routes</main></AuthenticatedOperatorGate></QueryClientProvider>);
    await screen.findByText("Normal operator routes");

    window.dispatchEvent(new Event("focus"));

    expect(await screen.findByText("Checking Rig system status…")).not.toBeNull();
    expect(screen.queryByText("Normal operator routes")).toBeNull();
    expect(screen.getByRole("button", { name: "Sign out" })).not.toBeNull();
    await act(async () => focusStatus.resolve(normalStatus));
    expect(await screen.findByText("Normal operator routes")).not.toBeNull();
    const restoredFocus = screen.getByRole("main");
    await waitFor(() => expect(document.activeElement).toBe(restoredFocus));
    expect(status).toHaveBeenCalledTimes(2);
  });

  it("moves an open normal console to indeterminate mode when a focus proof fails normally", async () => {
    vi.spyOn(api, "status")
      .mockResolvedValueOnce(normalStatus)
      .mockRejectedValueOnce(new APIError({ status: 503, code: "service_unavailable", detail: "Unavailable" }));
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    client.setQueryData(["lan-app-access", appId], { url: "http://192.168.50.4:8104/" });
    render(<QueryClientProvider client={client}><AuthenticatedOperatorGate onSignOut={() => undefined}><div>Normal operator routes</div></AuthenticatedOperatorGate></QueryClientProvider>);
    await screen.findByText("Normal operator routes");

    window.dispatchEvent(new Event("focus"));

    expect(await screen.findByRole("heading", { name: "System status unavailable" })).not.toBeNull();
    expect(screen.queryByText("Normal operator routes")).toBeNull();
    expect(client.getQueryData(["lan-app-access", appId])).toBeUndefined();
  });

  it("isolates an already-open normal tab on an exact recovery response from any API call", async () => {
    vi.spyOn(api, "status").mockResolvedValue(normalStatus);
    vi.spyOn(api, "lanRecoveryHead").mockResolvedValue(grantHead);
    vi.spyOn(api, "lanGrant").mockResolvedValue(grantRead);
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    client.setQueryData(["lan-app-access", appId], { url: "http://192.168.50.4:8104/" });
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({ code: "gateway_reconciliation_required", detail: "Recovery required" }), { status: 503 })));
    render(<QueryClientProvider client={client}><AuthenticatedOperatorGate onSignOut={() => undefined}><a href="http://192.168.50.4:8104/">Cached LAN URL</a></AuthenticatedOperatorGate></QueryClientProvider>);
    await screen.findByText("Cached LAN URL");

    await act(async () => {
      await api.apps().catch(() => undefined);
      window.dispatchEvent(new Event("focus"));
    });

    expect(await screen.findByRole("heading", { name: "LAN reconciliation required" })).not.toBeNull();
    expect(screen.queryByText("Cached LAN URL")).toBeNull();
    expect(document.body.textContent).not.toContain("http://192.168.50.4:8104/");
    expect(client.getQueryData(["lan-app-access", appId])).toBeUndefined();
    act(() => client.setQueryData(["app-configuration", appId], { secret: "late mutation result" }));
    expect(client.getQueryData(["app-configuration", appId])).toBeUndefined();
  });

  it("does not let an older 200 override a newer visibility recovery proof", async () => {
    const olderSuccess = deferred<SystemStatus>();
    const newerRecovery = deferred<SystemStatus>();
    const status = vi.spyOn(api, "status")
      .mockResolvedValueOnce(normalStatus)
      .mockReturnValueOnce(olderSuccess.promise)
      .mockReturnValueOnce(newerRecovery.promise);
    vi.spyOn(api, "lanRecoveryHead").mockResolvedValue(grantHead);
    vi.spyOn(api, "lanGrant").mockResolvedValue(grantRead);
    vi.spyOn(document, "visibilityState", "get").mockReturnValue("visible");
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(<QueryClientProvider client={client}><AuthenticatedOperatorGate onSignOut={() => undefined}><div>Normal operator routes</div></AuthenticatedOperatorGate></QueryClientProvider>);
    await screen.findByText("Normal operator routes");

    act(() => {
      window.dispatchEvent(new Event("focus"));
      document.dispatchEvent(new Event("visibilitychange"));
    });
    await waitFor(() => expect(status).toHaveBeenCalledTimes(3));
    await act(async () => newerRecovery.reject(recoveryRequired()));
    expect(await screen.findByRole("heading", { name: "LAN reconciliation required" })).not.toBeNull();

    await act(async () => olderSuccess.resolve(normalStatus));
    expect(screen.getByRole("heading", { name: "LAN reconciliation required" })).not.toBeNull();
    expect(screen.queryByText("Normal operator routes")).toBeNull();
  });

  it("fences the repeated StrictMode probe so its older 200 cannot defeat recovery", async () => {
    const firstProbe = deferred<SystemStatus>();
    const secondProbe = deferred<SystemStatus>();
    const status = vi.spyOn(api, "status")
      .mockReturnValueOnce(firstProbe.promise)
      .mockReturnValueOnce(secondProbe.promise);
    vi.spyOn(api, "lanRecoveryHead").mockResolvedValue(grantHead);
    vi.spyOn(api, "lanGrant").mockResolvedValue(grantRead);
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(<StrictMode><QueryClientProvider client={client}><AuthenticatedOperatorGate onSignOut={() => undefined}><div>Normal operator routes</div></AuthenticatedOperatorGate></QueryClientProvider></StrictMode>);
    await waitFor(() => expect(status).toHaveBeenCalledTimes(2));

    await act(async () => secondProbe.reject(recoveryRequired()));
    expect(await screen.findByRole("heading", { name: "LAN reconciliation required" })).not.toBeNull();
    await act(async () => firstProbe.resolve(normalStatus));

    expect(screen.getByRole("heading", { name: "LAN reconciliation required" })).not.toBeNull();
    expect(screen.queryByText("Normal operator routes")).toBeNull();
  });

  it("bounds visible active-tab staleness with the recurring normal-mode proof", async () => {
    vi.useFakeTimers();
    const status = vi.spyOn(api, "status")
      .mockResolvedValueOnce(normalStatus)
      .mockRejectedValueOnce(recoveryRequired());
    vi.spyOn(api, "lanRecoveryHead").mockResolvedValue(grantHead);
    vi.spyOn(api, "lanGrant").mockResolvedValue(grantRead);
    vi.spyOn(document, "visibilityState", "get").mockReturnValue("visible");
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(<QueryClientProvider client={client}><AuthenticatedOperatorGate onSignOut={() => undefined}><div>Normal operator routes</div></AuthenticatedOperatorGate></QueryClientProvider>);
    await act(async () => Promise.resolve());
    expect(screen.getByText("Normal operator routes")).not.toBeNull();

    await act(async () => { await vi.advanceTimersByTimeAsync(NORMAL_STATUS_RECHECK_MS); });

    expect(status).toHaveBeenCalledTimes(2);
    expect(screen.getByRole("heading", { name: "LAN reconciliation required" })).not.toBeNull();
    expect(screen.queryByText("Normal operator routes")).toBeNull();
  });

  it("fails closed when a recurring normal-mode status proof does not settle", async () => {
    vi.useFakeTimers();
    const status = vi.spyOn(api, "status")
      .mockResolvedValueOnce(normalStatus)
      .mockReturnValueOnce(new Promise<SystemStatus>(() => undefined));
    vi.spyOn(document, "visibilityState", "get").mockReturnValue("visible");
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    client.setQueryData(["lan-app-access", appId], { url: "http://192.168.50.4:8104/" });
    render(<QueryClientProvider client={client}><AuthenticatedOperatorGate onSignOut={() => undefined}><div>Normal operator routes</div></AuthenticatedOperatorGate></QueryClientProvider>);
    await act(async () => Promise.resolve());
    expect(screen.getByText("Normal operator routes")).not.toBeNull();

    await act(async () => { await vi.advanceTimersByTimeAsync(NORMAL_STATUS_RECHECK_MS); });
    expect(status).toHaveBeenCalledTimes(2);
    expect(screen.getByText("Normal operator routes")).not.toBeNull();

    await act(async () => { await vi.advanceTimersByTimeAsync(NORMAL_STATUS_PROBE_TIMEOUT_MS); });
    expect(screen.getByRole("heading", { name: "System status unavailable" })).not.toBeNull();
    expect(screen.queryByText("Normal operator routes")).toBeNull();
    expect(client.getQueryData(["lan-app-access", appId])).toBeUndefined();
  });
});

describe("LAN recovery review", () => {
  beforeEach(() => vi.restoreAllMocks());
  afterEach(cleanup);

  it("reviews and submits the exact grant without rendering a URL", async () => {
    vi.spyOn(api, "lanRecoveryHead").mockResolvedValue(grantHead);
    vi.spyOn(api, "lanGrant")
      .mockResolvedValueOnce({ ...grantRead, url: "http://192.168.50.4:8104/" } as LANAppGrantRead)
      .mockResolvedValueOnce({ ...grantRead, observed: { ...grantRead.observed, observedAt: "2026-10-01T10:00:05Z" } });
    const grant = vi.spyOn(api, "grantLANAccess").mockResolvedValue({
      created: false,
      claim: { ...grantRead.claim, state: "committed" },
    });
    renderRecovery();

    const heading = screen.getByRole("heading", { name: "LAN reconciliation required" });
    await screen.findByText(appId);
    await waitFor(() => expect(document.activeElement).toBe(heading));
    expect(screen.queryByRole("link")).toBeNull();
    expect(document.body.textContent).not.toContain("http://192.168.50.4:8104/");
    expect(screen.getByText("unknown")).not.toBeNull();

    await confirmAndSubmit("Reconcile exact grant");

    await waitFor(() => expect(grant).toHaveBeenCalledWith(appId, {
      attemptId: operationId,
      accessRevisionId,
      accessRevisionNumber: 7,
      approvalDigest,
    }));
    expect(api.lanRecoveryHead).toHaveBeenCalledTimes(3);
    expect(api.lanGrant).toHaveBeenCalledTimes(2);
    expect(await screen.findByRole("button", { name: "Check system status after restart" })).not.toBeNull();
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole("status")));
  });

  it("reviews and submits the exact disable", async () => {
    vi.spyOn(api, "lanRecoveryHead").mockResolvedValue(disableHead);
    vi.spyOn(api, "lanDisable").mockResolvedValue(disableRead);
    const disable = vi.spyOn(api, "disableLANAccess").mockResolvedValue({
      created: false,
      claim: { ...disableRead.claim, state: "committed" },
    });
    renderRecovery();

    await screen.findByText("withdrawn_pending_resolution");
    await confirmAndSubmit("Reconcile exact disable");

    await waitFor(() => expect(disable).toHaveBeenCalledWith(appId, {
      operationId,
      accessRevisionId,
      accessRevisionNumber: 7,
      allocationId,
      approvalDigest: disableHead.approvalDigest,
    }));
    expect(api.lanRecoveryHead).toHaveBeenCalledTimes(3);
    expect(api.lanDisable).toHaveBeenCalledTimes(2);
  });

  it("fails closed when the batch head changes during the final bracket read", async () => {
    vi.spyOn(api, "lanRecoveryHead")
      .mockResolvedValueOnce(grantHead)
      .mockResolvedValueOnce(grantHead)
      .mockResolvedValueOnce({ ...grantHead, batchPosition: 2 });
    vi.spyOn(api, "lanGrant").mockResolvedValue(grantRead);
    const grant = vi.spyOn(api, "grantLANAccess");
    renderRecovery();

    await screen.findByText(appId);
    await confirmAndSubmit("Reconcile exact grant");

    expect((await screen.findByRole("alert")).textContent).toContain("changed during review");
    expect(grant).not.toHaveBeenCalled();
    expect(screen.queryByRole("button", { name: /Reconcile exact/ })).toBeNull();
    expect(screen.queryByRole("button", { name: "Load a fresh recovery review" })).toBeNull();
    expect(document.activeElement).toBe(screen.getByRole("button", { name: "Reconciliation blocked" }));
  });

  it("fails closed when the freshly fetched observation does not match the head", async () => {
    vi.spyOn(api, "lanRecoveryHead").mockResolvedValue(grantHead);
    vi.spyOn(api, "lanGrant").mockResolvedValue({ ...grantRead, claim: { ...grantRead.claim, allocationId: "66666666-6666-4666-8666-666666666666" } });
    renderRecovery();

    expect((await screen.findByRole("alert")).textContent).toContain("complete, exact recovery review");
    expect(screen.queryByRole("button", { name: /Reconcile exact/ })).toBeNull();
  });

  it("fails closed when the observed disposition changes after deliberate review", async () => {
    vi.spyOn(api, "lanRecoveryHead").mockResolvedValue(grantHead);
    vi.spyOn(api, "lanGrant")
      .mockResolvedValueOnce(grantRead)
      .mockResolvedValueOnce({ ...grantRead, observed: { availability: "committed", observedAt: "2026-10-01T10:00:05Z" } });
    const grant = vi.spyOn(api, "grantLANAccess");
    renderRecovery();

    await screen.findByText(appId);
    await confirmAndSubmit("Reconcile exact grant");

    expect((await screen.findByRole("alert")).textContent).toContain("exact observation changed during review");
    expect(grant).not.toHaveBeenCalled();
    expect(screen.queryByRole("button", { name: /Reconcile exact/ })).toBeNull();
  });

  it.each([
    [403, "lan_access_forbidden", "Administrator access is required"],
    [503, "lan_access_unavailable", "could not prove one exact recovery operation"],
  ])("removes the recovery action when the head read returns %s", async (status, code, message) => {
    vi.spyOn(api, "lanRecoveryHead").mockRejectedValue(new APIError({ status, code, detail: "Hidden detail" }));
    renderRecovery();

    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain(message);
    await waitFor(() => expect(document.activeElement).toBe(alert));
    expect(screen.queryByRole("button", { name: /Reconcile exact/ })).toBeNull();
    expect(screen.queryByRole("checkbox")).toBeNull();
  });

  it.each([
    [{ ...grantHead, approvalDigest: undefined }, "complete, exact recovery review"],
    [{ ...grantHead, kind: "lan_gateway_upgrade" }, "recovery kind is not supported"],
  ])("fails closed on an incomplete or unsupported recovery head", async (head, message) => {
    vi.spyOn(api, "lanRecoveryHead").mockResolvedValue(head as unknown as LANRecoveryHead);
    const observation = vi.spyOn(api, "lanGrant");
    renderRecovery();

    expect((await screen.findByRole("alert")).textContent).toContain(message);
    expect(observation).not.toHaveBeenCalled();
    expect(screen.queryByRole("button", { name: /Reconcile exact/ })).toBeNull();
  });

  it.each([
    [403, "lan_access_forbidden", "Administrator access is required"],
    [503, "lan_access_unavailable", "can no longer prove this recovery operation"],
  ])("removes every retry action when the reconciliation POST returns %s", async (status, code, message) => {
    vi.spyOn(api, "lanRecoveryHead").mockResolvedValue(grantHead);
    vi.spyOn(api, "lanGrant").mockResolvedValue(grantRead);
    const grant = vi.spyOn(api, "grantLANAccess").mockRejectedValue(new APIError({ status, code, detail: "Hidden detail" }));
    renderRecovery();

    await screen.findByText(appId);
    await confirmAndSubmit("Reconcile exact grant");

    expect((await screen.findByRole("alert")).textContent).toContain(message);
    expect(grant).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole("button", { name: /Reconcile exact/ })).toBeNull();
    expect(screen.queryByRole("button", { name: "Load a fresh recovery review" })).toBeNull();
  });

  it("does not repeat an ambiguous POST and requires a fresh explicit review", async () => {
    vi.spyOn(api, "lanRecoveryHead").mockResolvedValue(grantHead);
    vi.spyOn(api, "lanGrant").mockResolvedValue(grantRead);
    const grant = vi.spyOn(api, "grantLANAccess").mockRejectedValue(new TypeError("network response lost"));
    renderRecovery();

    await screen.findByText(appId);
    await confirmAndSubmit("Reconcile exact grant");

    expect((await screen.findByRole("alert")).textContent).toContain("result is unknown");
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole("alert")));
    expect(grant).toHaveBeenCalledTimes(1);
    await act(async () => Promise.resolve());
    expect(grant).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole("button", { name: "Reconcile exact grant" })).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: "Load a fresh recovery review" }));
    await screen.findByRole("button", { name: "Reconcile exact grant" });
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole("heading", { name: "LAN reconciliation required" })));
    expect(grant).toHaveBeenCalledTimes(1);
  });

  it("delegates leaving recovery mode to the explicit status coordinator", async () => {
    vi.spyOn(api, "lanRecoveryHead").mockResolvedValue(disableHead);
    vi.spyOn(api, "lanDisable").mockResolvedValue(disableRead);
    vi.spyOn(api, "disableLANAccess").mockResolvedValue({ created: false, claim: { ...disableRead.claim, state: "committed" } });
    const { onCheckSystemStatus } = renderRecovery();

    await screen.findByText("withdrawn_pending_resolution");
    await confirmAndSubmit("Reconcile exact disable");
    await screen.findByRole("button", { name: "Check system status after restart" });
    expect(onCheckSystemStatus).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: "Check system status after restart" }));
    await waitFor(() => expect(onCheckSystemStatus).toHaveBeenCalledTimes(1));
  });
});

describe("recovery sign out", () => {
  beforeEach(() => vi.restoreAllMocks());
  afterEach(() => {
    cleanup();
    window.sessionStorage.clear();
  });

  it("clears session-scoped data and the query cache from the isolated screen", async () => {
    vi.spyOn(api, "bootstrapStatus").mockResolvedValue({ bootstrapRequired: false });
    vi.spyOn(api, "me").mockResolvedValue({ user: { id: "admin-id", username: "admin", role: "admin" } });
    vi.spyOn(api, "csrf").mockResolvedValue("csrf-token");
    vi.spyOn(api, "status").mockRejectedValue(recoveryRequired());
    vi.spyOn(api, "lanRecoveryHead").mockResolvedValue(grantHead);
    vi.spyOn(api, "lanGrant").mockResolvedValue(grantRead);
    const logout = vi.spyOn(api, "logout").mockResolvedValue(undefined);
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    client.setQueryData(["sensitive"], { value: "cached" });
    window.sessionStorage.setItem("hostd-csrf", "csrf-token");
    window.sessionStorage.setItem("rig-lan-access-grant:app", "pending");

    render(<QueryClientProvider client={client}><MemoryRouter><App/></MemoryRouter></QueryClientProvider>);
    await screen.findByRole("heading", { name: "LAN reconciliation required" });
    fireEvent.click(screen.getByRole("button", { name: "Sign out" }));

    expect(await screen.findByRole("heading", { name: "Welcome back" })).not.toBeNull();
    expect(logout).toHaveBeenCalledTimes(1);
    expect(window.sessionStorage.getItem("hostd-csrf")).toBeNull();
    expect(window.sessionStorage.getItem("rig-lan-access-grant:app")).toBeNull();
    expect(client.getQueryData(["sensitive"])).toBeUndefined();
  });
});
