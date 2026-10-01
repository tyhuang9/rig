import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { APIError, api, type LANAppAccessRead, type SystemStatus } from "./api";
import { LAN_ADDRESS_ACTION_DEADLINE_MS, LANApplicationAccessPanel } from "./lan-application-access";

const appId = "11111111-1111-4111-8111-111111111111";
const userId = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa";
const profile = { id: "22222222-2222-4222-8222-222222222222", revisionNumber: 2, spec: { interfaceId: "nic-1", selectedIpv4: "192.168.50.4", portStart: 8100, portEnd: 8119 } };
const revision = {
  id: "33333333-3333-4333-8333-333333333333", appId, revisionNumber: 1, operationId: "44444444-4444-4444-8444-444444444444", specDigest: "b".repeat(64), approvedBy: "admin", approvedAt: "2026-10-01T00:00:00Z",
  allocation: { id: "55555555-5555-4555-8555-555555555555", appId, port: 8100, ownerOperationId: "44444444-4444-4444-8444-444444444444", gatewayProfileRevisionId: profile.id, gatewayProfileRevisionNumber: 2, state: "active" },
};
const reservationOperation = "66666666-6666-4666-8666-666666666666";
const grantAttempt = "77777777-7777-4777-8777-777777777777";
const disableOperation = "88888888-8888-4888-8888-888888888888";
const disableDigest = "d".repeat(64);
const lanURL = "http://192.168.50.4:8100/";
const normalStatus = {
  daemon: "running",
  capabilities: { composeRuntime: false, fakeRuntime: false, generatedRuntime: true, githubConnections: false },
  diagnostics: {},
} as SystemStatus;

function verifiedAccess(overrides: Partial<LANAppAccessRead> = {}): LANAppAccessRead {
  return {
    expectedRevisionNumber: 1,
    availability: "verified",
    desiredAccess: revision,
    url: lanURL,
    observedAt: new Date().toISOString(),
    ...overrides,
  };
}

function popupWindow() {
  const replace = vi.fn();
  const initialDocument = document.implementation.createHTMLDocument();
  const popupRecord: { opener: unknown; closed: boolean; document: Document; location: { href: string; origin: string; replace: typeof replace }; close: () => void } = {
    opener: { source: "opener" }, closed: false, document: initialDocument,
    location: { href: "about:blank", origin: window.location.origin, replace }, close: () => undefined,
  };
  const close = vi.fn(() => { popupRecord.closed = true; });
  popupRecord.close = close;
  const popup = popupRecord as unknown as Window;
  return { popup, popupRecord, replace, close };
}

function grantClaim(state = "committed") {
  return { attemptId: grantAttempt, appId, allocationId: revision.allocation.id, ownerOperationId: revision.operationId, accessRevisionId: revision.id, accessRevisionNumber: revision.revisionNumber, port: revision.allocation.port, state, updatedAt: "2026-10-01T00:00:00Z" };
}

function disableReview(overrides = {}) {
  return { appId, accessRevisionId: revision.id, accessRevisionNumber: revision.revisionNumber, allocationId: revision.allocation.id, ownerOperationId: revision.operationId, port: revision.allocation.port, gatewayProfileRevisionId: profile.id, gatewayProfileRevisionNumber: profile.revisionNumber, approvalDigest: disableDigest, ...overrides };
}

function disableClaim(state = "committed") {
  return { appId, operationId: disableOperation, accessRevisionId: revision.id, accessRevisionNumber: revision.revisionNumber, allocationId: revision.allocation.id, port: revision.allocation.port, approvalDigest: disableDigest, state, updatedAt: "2026-10-01T00:01:00Z", releasedAt: state === "committed" ? "2026-10-01T00:01:00Z" : undefined };
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((next) => { resolve = next; });
  return { promise, resolve };
}

function renderPanel(role = "administrator", operatorId = userId, enabled = true) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return { client, ...render(<QueryClientProvider client={client}><LANApplicationAccessPanel appId={appId} role={role} userId={operatorId} enabled={enabled}/></QueryClientProvider>) };
}

function mockProfile() {
  vi.spyOn(api, "lanGatewayProfile").mockResolvedValue({ expectedRevisionNumber: 2, candidates: [], desiredProfile: profile } as never);
}

describe("LANApplicationAccessPanel", () => {
  afterEach(() => {
    cleanup();
    window.sessionStorage.clear();
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
    vi.useRealTimers();
  });

  it("rehydrates a pending reservation from the controller snapshot and approves its exact fields", async () => {
    const accessValue = { expectedRevisionNumber: 0, availability: "local_only", pendingReservation: { expectedRevisionNumber: 0, approvalDigest: "a".repeat(64), allocation: { ...revision.allocation, state: "reserved", ownerOperationId: reservationOperation } } };
    vi.spyOn(api, "lanAccess").mockImplementation(async () => accessValue as never);
    mockProfile();
    const approve = vi.spyOn(api, "approveLANAccess").mockResolvedValue({ created: true, revision } as never);

    renderPanel();
    fireEvent.click(await screen.findByRole("button", { name: "Review reserved port" }));
    const review = await screen.findByRole("dialog", { name: "Review LAN application access" });
    expect(review.textContent).toContain("a".repeat(64));
    fireEvent.click(screen.getByRole("button", { name: "Approve LAN access" }));
    await waitFor(() => expect(approve).toHaveBeenCalledWith(appId, { operationId: reservationOperation, allocationId: revision.allocation.id, expectedRevisionNumber: 0, approvalDigest: "a".repeat(64) }));
  });

  it("focuses an approval error inside the review dialog", async () => {
    vi.spyOn(api, "lanAccess").mockResolvedValue({ expectedRevisionNumber: 0, availability: "local_only", pendingReservation: { expectedRevisionNumber: 0, approvalDigest: "a".repeat(64), allocation: { ...revision.allocation, state: "reserved", ownerOperationId: reservationOperation } } } as never);
    mockProfile();
    vi.spyOn(api, "approveLANAccess").mockRejectedValue(new Error("approval changed"));

    renderPanel();
    fireEvent.click(await screen.findByRole("button", { name: "Review reserved port" }));
    const dialog = await screen.findByRole("dialog", { name: "Review LAN application access" });
    expect(document.activeElement).toBe(screen.getByRole("heading", { name: "Review LAN application access" }));
    fireEvent.click(screen.getByRole("button", { name: "Approve LAN access" }));
    const error = await screen.findByRole("alert");
    await waitFor(() => expect(document.activeElement).toBe(error));
    expect(dialog.contains(error)).toBe(true);
  });

  it("focuses reservation recovery when its result is uncertain", async () => {
    vi.spyOn(api, "lanAccess").mockResolvedValue({ expectedRevisionNumber: 0, availability: "local_only" } as never);
    mockProfile();
    vi.spyOn(crypto, "randomUUID").mockReturnValue(reservationOperation);
    const reserve = vi.spyOn(api, "reserveLANAccess").mockRejectedValue(new Error("connection interrupted"));

    renderPanel();
    fireEvent.click(await screen.findByRole("button", { name: "Reserve LAN port for review" }));
    const recovery = await screen.findByRole("heading", { name: "Reservation result needs reconciliation" });
    await waitFor(() => expect(document.activeElement).toBe(recovery));
    expect(reserve).toHaveBeenCalledWith(appId, expect.objectContaining({ operationId: reservationOperation, expectedRevisionNumber: 0 }));
    expect(screen.queryByRole("button", { name: "Reserve LAN port for review" })).toBeNull();
  });

  it("rehydrates and observes the exact current activation claim after reload", async () => {
    vi.spyOn(api, "lanAccess").mockResolvedValue({ expectedRevisionNumber: 1, availability: "unverified", desiredAccess: revision, grantClaim: grantClaim() } as never);
    mockProfile();
    const observe = vi.spyOn(api, "lanGrant").mockResolvedValue({ claim: grantClaim(), observed: { availability: "committed", observedAt: "2026-10-01T00:00:01Z" } } as never);

    renderPanel();
    await screen.findByRole("heading", { name: "Activation claim" });
    expect(screen.getByText(grantAttempt)).not.toBeNull();
    await waitFor(() => expect(observe).toHaveBeenCalledWith(appId, grantAttempt));
    expect(screen.getByText("Gateway observation").parentElement?.textContent).toContain("committed");
  });

  it("retains and replays the exact activation request after an uncertain response", async () => {
    vi.spyOn(api, "lanAccess").mockResolvedValue({ expectedRevisionNumber: 1, availability: "unverified", desiredAccess: revision } as never);
    mockProfile();
    vi.spyOn(crypto, "randomUUID").mockReturnValue(grantAttempt);
    const grant = vi.spyOn(api, "grantLANAccess").mockRejectedValue(new Error("connection interrupted"));

    renderPanel();
    fireEvent.click(await screen.findByRole("button", { name: "Review LAN activation" }));
    const review = await screen.findByRole("dialog", { name: "Review LAN activation" });
    expect(review.textContent).toContain(revision.specDigest);
    fireEvent.click(screen.getByRole("button", { name: "Grant and attest LAN route" }));
    const recovery = await screen.findByRole("heading", { name: "Activation result needs reconciliation" });
    await waitFor(() => expect(document.activeElement).toBe(recovery));
    expect(grant).toHaveBeenCalledWith(appId, {
      attemptId: grantAttempt,
      accessRevisionId: revision.id,
      accessRevisionNumber: revision.revisionNumber,
      approvalDigest: revision.specDigest,
    });

    cleanup();
    renderPanel();
    fireEvent.click(await screen.findByRole("button", { name: "Replay exact activation" }));
    await waitFor(() => expect(grant).toHaveBeenCalledTimes(2));
    expect(grant.mock.calls[1]).toEqual(grant.mock.calls[0]);
    expect(crypto.randomUUID).toHaveBeenCalledTimes(1);
  });

  it("posts the exact disable review with a fresh operation ID and shows release completion without desired access", async () => {
    let accessValue: Record<string, unknown> = { expectedRevisionNumber: 1, availability: "unverified", desiredAccess: revision, grantClaim: grantClaim(), disableReview: disableReview() };
    vi.spyOn(crypto, "randomUUID").mockReturnValue(disableOperation);
    vi.spyOn(api, "lanAccess").mockImplementation(async () => accessValue as never);
    mockProfile();
    vi.spyOn(api, "lanGrant").mockResolvedValue({ claim: grantClaim(), observed: { availability: "committed" } } as never);
    const remove = vi.spyOn(api, "disableLANAccess").mockImplementation(async (_id, request) => {
      accessValue = { expectedRevisionNumber: 2, availability: "local_only", disableClaim: disableClaim() };
      return { created: true, claim: { ...disableClaim(), operationId: request.operationId } } as never;
    });
    vi.spyOn(api, "lanDisable").mockResolvedValue({ claim: disableClaim(), observed: { availability: "disabled" } } as never);

    renderPanel();
    fireEvent.click(await screen.findByRole("button", { name: "Review LAN sharing removal" }));
    const review = await screen.findByRole("dialog", { name: "Review LAN sharing removal" });
    for (const exactValue of [appId, revision.id, revision.operationId, revision.allocation.id, String(revision.allocation.port), profile.id, String(profile.revisionNumber), disableDigest]) expect(review.textContent).toContain(exactValue);
    fireEvent.click(screen.getByRole("button", { name: "Remove LAN sharing" }));
    await waitFor(() => expect(remove).toHaveBeenCalledWith(appId, { operationId: disableOperation, accessRevisionId: revision.id, accessRevisionNumber: revision.revisionNumber, allocationId: revision.allocation.id, approvalDigest: disableDigest }));
    const heading = await screen.findByRole("heading", { name: "LAN sharing removal claim" });
    await waitFor(() => expect(document.activeElement).toBe(heading));
    expect(screen.getByText(/Port 8100 was released/)).not.toBeNull();
    expect(screen.queryByRole("button", { name: "Open LAN address" })).toBeNull();
  });

  it("retains and replays the exact uncertain disable request after reload", async () => {
    const initialAccess = { expectedRevisionNumber: 1, availability: "verified", desiredAccess: revision, grantClaim: grantClaim(), disableReview: disableReview(), url: "http://192.168.50.4:8100/", observedAt: new Date().toISOString() };
    vi.spyOn(crypto, "randomUUID").mockReturnValue(disableOperation);
    vi.spyOn(api, "lanAccess").mockResolvedValue(initialAccess as never);
    mockProfile();
    vi.spyOn(api, "lanGrant").mockResolvedValue({ claim: grantClaim(), observed: { availability: "committed" } } as never);
    const remove = vi.spyOn(api, "disableLANAccess").mockRejectedValueOnce(new Error("connection interrupted")).mockRejectedValueOnce(new Error("connection interrupted"));

    renderPanel();
    expect(await screen.findByRole("button", { name: "Open LAN address" })).not.toBeNull();
    fireEvent.click(await screen.findByRole("button", { name: "Review LAN sharing removal" }));
    fireEvent.click(screen.getByRole("button", { name: "Remove LAN sharing" }));
    const recovery = await screen.findByRole("heading", { name: "LAN sharing removal needs reconciliation" });
    await waitFor(() => expect(document.activeElement).toBe(recovery));
    expect(screen.queryByRole("button", { name: "Open LAN address" })).toBeNull();
    expect(JSON.parse(window.sessionStorage.getItem("rig-lan-access-disable:" + appId) || "{}")).toMatchObject({ version: 1, kind: "disable", appId, userId, request: { operationId: disableOperation, accessRevisionId: revision.id, approvalDigest: disableDigest } });

    cleanup();
    renderPanel();
    await screen.findByRole("heading", { name: "LAN sharing removal needs reconciliation" });
    expect(screen.queryByRole("button", { name: "Open LAN address" })).toBeNull();
    fireEvent.click(await screen.findByRole("button", { name: "Replay exact removal" }));
    await waitFor(() => expect(remove).toHaveBeenCalledTimes(2));
    expect(remove.mock.calls[1]).toEqual(remove.mock.calls[0]);
    expect(crypto.randomUUID).toHaveBeenCalledTimes(1);
  });

  it("does not offer another administrator a retained removal replay", async () => {
    window.sessionStorage.setItem("rig-lan-access-disable:" + appId, JSON.stringify({
      version: 1,
      kind: "disable",
      appId,
      userId,
      request: { operationId: disableOperation, accessRevisionId: revision.id, accessRevisionNumber: revision.revisionNumber, allocationId: revision.allocation.id, approvalDigest: disableDigest },
    }));
    vi.spyOn(api, "lanAccess").mockResolvedValue({ expectedRevisionNumber: 1, availability: "unverified", desiredAccess: revision, grantClaim: grantClaim(), disableReview: disableReview() } as never);
    mockProfile();
    vi.spyOn(api, "lanGrant").mockResolvedValue({ claim: grantClaim(), observed: { availability: "committed" } } as never);
    const remove = vi.spyOn(api, "disableLANAccess");

    renderPanel("administrator", "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb");
    expect(await screen.findByRole("button", { name: "Review LAN sharing removal" })).not.toBeNull();
    expect(screen.queryByRole("button", { name: "Replay exact removal" })).toBeNull();
    expect(window.sessionStorage.getItem("rig-lan-access-disable:" + appId)).toBeNull();
    expect(remove).not.toHaveBeenCalled();
  });

  it("disables a stale removal review when the current snapshot changes its digest", async () => {
    const first = { expectedRevisionNumber: 1, availability: "unverified", desiredAccess: revision, grantClaim: grantClaim(), disableReview: disableReview() };
    vi.spyOn(api, "lanAccess").mockResolvedValue(first as never);
    mockProfile();
    vi.spyOn(api, "lanGrant").mockResolvedValue({ claim: grantClaim(), observed: { availability: "committed" } } as never);
    const remove = vi.spyOn(api, "disableLANAccess");

    const { client } = renderPanel();
    fireEvent.click(await screen.findByRole("button", { name: "Review LAN sharing removal" }));
    await screen.findByRole("dialog", { name: "Review LAN sharing removal" });
    await act(async () => { client.setQueryData(["lan-app-access", appId], { ...first, disableReview: disableReview({ approvalDigest: "e".repeat(64) }) }); });
    await waitFor(() => expect((screen.getByRole("button", { name: "Remove LAN sharing" }) as HTMLButtonElement).disabled).toBe(true));
    expect(remove).not.toHaveBeenCalled();
  });

  it("renders a committed prior disable and a successor reservation from one snapshot", async () => {
    vi.spyOn(api, "lanAccess").mockResolvedValue({ expectedRevisionNumber: 2, availability: "local_only", disableClaim: disableClaim(), pendingReservation: { expectedRevisionNumber: 1, approvalDigest: "a".repeat(64), allocation: { ...revision.allocation, id: "99999999-9999-4999-8999-999999999999", state: "reserved", ownerOperationId: reservationOperation } } } as never);
    mockProfile();
    vi.spyOn(api, "lanDisable").mockResolvedValue({ claim: disableClaim(), observed: { availability: "disabled" } } as never);

    renderPanel();
    expect(await screen.findByRole("heading", { name: "LAN sharing removal claim" })).not.toBeNull();
    expect(screen.getByText("LAN sharing removed")).not.toBeNull();
    expect(await screen.findByRole("button", { name: "Review reserved port" })).not.toBeNull();
    expect(screen.queryByRole("button", { name: "Open LAN address" })).toBeNull();
  });

  it("hides the removal action while a grant is unresolved", async () => {
    vi.spyOn(api, "lanAccess").mockResolvedValue({ expectedRevisionNumber: 1, availability: "unverified", desiredAccess: revision, grantClaim: grantClaim("applying"), disableReview: disableReview() } as never);
    mockProfile();
    vi.spyOn(api, "lanGrant").mockResolvedValue({ claim: grantClaim("applying"), observed: { availability: "applying" } } as never);

    renderPanel();
    expect(await screen.findByText("LAN activation is still resolving.")).not.toBeNull();
    expect(screen.queryByRole("button", { name: "Review LAN sharing removal" })).toBeNull();
  });

  it("withholds a previously verified LAN address while it refreshes", async () => {
    const verified = { expectedRevisionNumber: 1, availability: "verified", desiredAccess: revision, url: "http://192.168.50.4:8100/", observedAt: new Date().toISOString() };
    const refreshed = deferred<typeof verified>();
    vi.spyOn(api, "lanAccess").mockResolvedValueOnce(verified as never).mockReturnValueOnce(refreshed.promise as never);
    mockProfile();

    renderPanel();
    await screen.findByRole("button", { name: "Open LAN address" });
    fireEvent.click(screen.getByRole("button", { name: "Check LAN access" }));
    expect(await screen.findByText("Checking LAN route")).not.toBeNull();
    expect(screen.queryByRole("button", { name: "Open LAN address" })).toBeNull();
    await act(async () => { refreshed.resolve(verified); });
    expect(await screen.findByRole("button", { name: "Open LAN address" })).not.toBeNull();
  });

  it("preopens one isolated window and navigates only after the ordered exact proof", async () => {
    const displayed = verifiedAccess();
    const firstStatus = deferred<SystemStatus>();
    const order: string[] = [];
    let accessReads = 0;
    const access = vi.spyOn(api, "lanAccess").mockImplementation(async () => {
      accessReads += 1;
      if (accessReads === 1) return displayed;
      order.push("fresh access");
      return { ...displayed, observedAt: new Date().toISOString() };
    });
    vi.spyOn(api, "status")
      .mockImplementationOnce(() => { order.push("status before"); return firstStatus.promise; })
      .mockImplementationOnce(async () => { order.push("status after"); return normalStatus; });
    mockProfile();
    const { popup, popupRecord, replace, close } = popupWindow();
    const open = vi.spyOn(window, "open").mockReturnValue(popup);

    renderPanel();
    const button = await screen.findByRole("button", { name: "Open LAN address" });
    expect(button.tagName).toBe("BUTTON");
    act(() => {
      button.dispatchEvent(new MouseEvent("click", { bubbles: true }));
      button.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });

    expect(open).toHaveBeenCalledTimes(1);
    expect(open).toHaveBeenCalledWith("about:blank", "_blank");
    expect(popup.opener).toBeNull();
    expect(popupRecord.document.title).toBe("Checking LAN address — Rig");
    expect(popupRecord.document.querySelector("h1")?.textContent).toBe("Checking LAN address");
    expect(popupRecord.document.querySelector("[role='status']")?.textContent).toContain("proving the current LAN address");
    expect(popupRecord.document.body.textContent).not.toContain(lanURL);
    const checking = (await screen.findByText("Checking LAN address")).closest("[role='status']");
    expect(checking).not.toBeNull();
    await waitFor(() => expect(document.activeElement).toBe(checking));
    expect(screen.queryByText(lanURL)).toBeNull();
    expect(replace).not.toHaveBeenCalled();

    await act(async () => firstStatus.resolve(normalStatus));
    await waitFor(() => expect(replace).toHaveBeenCalledWith(lanURL));
    expect(order).toEqual(["status before", "fresh access", "status after"]);
    expect(access).toHaveBeenCalledTimes(2);
    expect(close).not.toHaveBeenCalled();
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole("button", { name: "Open LAN address" })));
  });

  it("keeps the focused checking status visible if access becomes local-only mid-action", async () => {
    const displayed = verifiedAccess();
    const firstStatus = deferred<SystemStatus>();
    vi.spyOn(api, "lanAccess").mockResolvedValue(displayed);
    vi.spyOn(api, "status").mockReturnValueOnce(firstStatus.promise);
    mockProfile();
    const { popup } = popupWindow();
    vi.spyOn(window, "open").mockReturnValue(popup);

    const view = renderPanel();
    fireEvent.click(await screen.findByRole("button", { name: "Open LAN address" }));
    const checking = (await screen.findByText("Checking LAN address")).closest("[role='status']");
    await waitFor(() => expect(document.activeElement).toBe(checking));
    await act(async () => {
      view.client.setQueryData(["lan-app-access", appId], { expectedRevisionNumber: 2, availability: "local_only" });
      await Promise.resolve();
    });

    const currentChecking = screen.getByText("Checking LAN address").closest("[role='status']");
    expect(currentChecking?.textContent).toContain("Checking LAN address");
    expect(document.activeElement).toBe(currentChecking);
    expect(screen.queryByText("Local only")).toBeNull();
    view.unmount();
    await act(async () => firstStatus.resolve(normalStatus));
  });

  it("keeps focused failure guidance visible if access becomes local-only", async () => {
    const displayed = verifiedAccess();
    vi.spyOn(api, "lanAccess").mockResolvedValue(displayed);
    vi.spyOn(api, "status").mockResolvedValue({ ...normalStatus, daemon: "recovery" });
    mockProfile();
    const { popup } = popupWindow();
    vi.spyOn(window, "open").mockReturnValue(popup);

    const view = renderPanel();
    fireEvent.click(await screen.findByRole("button", { name: "Open LAN address" }));
    const initialAlert = await screen.findByRole("alert");
    await waitFor(() => expect(document.activeElement).toBe(initialAlert));
    await act(async () => {
      view.client.setQueryData(["lan-app-access", appId], { expectedRevisionNumber: 2, availability: "local_only" });
      await Promise.resolve();
    });

    const currentAlert = screen.getByRole("alert");
    expect(currentAlert.textContent).toContain("LAN address action stopped");
    expect(document.activeElement).toBe(currentAlert);
    expect(screen.queryByText("Local only")).toBeNull();
  });

  it.each([
    ["a replacement document", (popupRecord: ReturnType<typeof popupWindow>["popupRecord"]) => { popupRecord.document = {} as Document; }],
    ["a cross-origin inspection failure", (popupRecord: ReturnType<typeof popupWindow>["popupRecord"]) => {
      Object.defineProperty(popupRecord, "document", { configurable: true, get: () => { throw new DOMException("Blocked", "SecurityError"); } });
    }],
  ])("closes the preopened window when it has %s before navigation", async (_case, changeWindow) => {
    const displayed = verifiedAccess();
    const secondStatus = deferred<SystemStatus>();
    vi.spyOn(api, "lanAccess").mockResolvedValue(displayed);
    vi.spyOn(api, "status").mockResolvedValueOnce(normalStatus).mockReturnValueOnce(secondStatus.promise);
    mockProfile();
    const { popup, popupRecord, replace, close } = popupWindow();
    vi.spyOn(window, "open").mockReturnValue(popup);

    renderPanel();
    fireEvent.click(await screen.findByRole("button", { name: "Open LAN address" }));
    await screen.findByText("Checking LAN address");
    changeWindow(popupRecord);
    await act(async () => secondStatus.resolve(normalStatus));

    const alert = await screen.findByRole("alert");
    await waitFor(() => expect(document.activeElement).toBe(alert));
    expect(alert.textContent).toContain("new window changed");
    expect(close).toHaveBeenCalledTimes(1);
    expect(replace).not.toHaveBeenCalled();
    expect(screen.queryByText(lanURL)).toBeNull();
  });

  it("expires the total Open proof and fences a late status response", async () => {
    const displayed = verifiedAccess();
    const firstStatus = deferred<SystemStatus>();
    const access = vi.spyOn(api, "lanAccess").mockResolvedValue(displayed);
    const status = vi.spyOn(api, "status").mockReturnValueOnce(firstStatus.promise);
    mockProfile();
    const { popup, replace, close } = popupWindow();
    vi.spyOn(window, "open").mockReturnValue(popup);

    renderPanel();
    const button = await screen.findByRole("button", { name: "Open LAN address" });
    vi.useFakeTimers();
    fireEvent.click(button);
    expect(screen.getByText("Checking LAN address")).not.toBeNull();
    await act(async () => { vi.advanceTimersByTime(LAN_ADDRESS_ACTION_DEADLINE_MS); });

    const alert = screen.getByRole("alert");
    expect(document.activeElement).toBe(alert);
    expect(alert.textContent).toContain("within 10 seconds");
    expect(close).toHaveBeenCalledTimes(1);
    expect(replace).not.toHaveBeenCalled();
    await act(async () => { firstStatus.resolve(normalStatus); await Promise.resolve(); });
    expect(status).toHaveBeenCalledTimes(1);
    expect(access).toHaveBeenCalledTimes(1);
    expect(replace).not.toHaveBeenCalled();
  });

  it("does not copy or continue proof reads after the total action deadline", async () => {
    const displayed = verifiedAccess();
    const firstStatus = deferred<SystemStatus>();
    const access = vi.spyOn(api, "lanAccess").mockResolvedValue(displayed);
    const status = vi.spyOn(api, "status").mockReturnValueOnce(firstStatus.promise);
    mockProfile();
    const writeText = vi.fn();
    vi.stubGlobal("navigator", { clipboard: { writeText } });

    renderPanel();
    const button = await screen.findByRole("button", { name: "Copy address" });
    vi.useFakeTimers();
    fireEvent.click(button);
    await act(async () => { vi.advanceTimersByTime(LAN_ADDRESS_ACTION_DEADLINE_MS); });
    await act(async () => { firstStatus.resolve(normalStatus); await Promise.resolve(); });

    expect(screen.getByRole("alert").textContent).toContain("within 10 seconds");
    expect(status).toHaveBeenCalledTimes(1);
    expect(access).toHaveBeenCalledTimes(1);
    expect(writeText).not.toHaveBeenCalled();
  });

  it("copies only the URL returned by the fresh ordered proof", async () => {
    const displayed = verifiedAccess();
    const order: string[] = [];
    let accessReads = 0;
    vi.spyOn(api, "lanAccess").mockImplementation(async () => {
      accessReads += 1;
      if (accessReads === 1) return displayed;
      order.push("fresh access");
      return { ...displayed, observedAt: new Date().toISOString() };
    });
    vi.spyOn(api, "status")
      .mockImplementationOnce(async () => { order.push("status before"); return normalStatus; })
      .mockImplementationOnce(async () => { order.push("status after"); return normalStatus; });
    mockProfile();
    const writeText = vi.fn(async (value: string) => { order.push("clipboard"); return value; });
    vi.stubGlobal("navigator", { clipboard: { writeText } });

    renderPanel();
    const button = await screen.findByRole("button", { name: "Copy address" });
    button.focus();
    expect(document.activeElement).toBe(button);
    fireEvent.click(button);

    await waitFor(() => expect(writeText).toHaveBeenCalledWith(lanURL));
    expect(order).toEqual(["status before", "fresh access", "status after", "clipboard"]);
    expect((await screen.findAllByText("The freshly proved LAN address was copied.")).length).toBeGreaterThan(0);
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole("button", { name: "Copy address" })));
  });

  it.each([
    ["application drift", (displayed: LANAppAccessRead) => ({ ...displayed, desiredAccess: { ...revision, appId: "99999999-9999-4999-8999-999999999999", allocation: { ...revision.allocation, appId: "99999999-9999-4999-8999-999999999999" } } })],
    ["revision drift", (displayed: LANAppAccessRead) => ({ ...displayed, expectedRevisionNumber: 2, desiredAccess: { ...revision, revisionNumber: 2 } })],
    ["allocation drift", (displayed: LANAppAccessRead) => ({ ...displayed, desiredAccess: { ...revision, allocation: { ...revision.allocation, id: "99999999-9999-4999-8999-999999999999" } } })],
    ["URL drift", (displayed: LANAppAccessRead) => ({ ...displayed, url: "http://192.168.50.5:8100/" })],
    ["a stale observation", (displayed: LANAppAccessRead) => ({ ...displayed, observedAt: "2020-01-01T00:00:00Z" })],
  ])("closes the preopened window and withholds the address after %s", async (_case, freshAccess) => {
    const displayed = verifiedAccess();
    vi.spyOn(api, "lanAccess").mockResolvedValueOnce(displayed).mockResolvedValueOnce(freshAccess(displayed) as LANAppAccessRead);
    const status = vi.spyOn(api, "status").mockResolvedValue(normalStatus);
    mockProfile();
    const { popup, replace, close } = popupWindow();
    vi.spyOn(window, "open").mockReturnValue(popup);

    renderPanel();
    fireEvent.click(await screen.findByRole("button", { name: "Open LAN address" }));

    const alert = await screen.findByRole("alert");
    await waitFor(() => expect(document.activeElement).toBe(alert));
    expect(alert.textContent).toContain("changed during the check");
    expect(status).toHaveBeenCalledTimes(2);
    expect(close).toHaveBeenCalledTimes(1);
    expect(replace).not.toHaveBeenCalled();
    expect(screen.queryByText(lanURL)).toBeNull();
    expect(screen.queryByRole("button", { name: "Open LAN address" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Copy address" })).toBeNull();
  });

  it.each([
    ["first", [{ daemon: "running", capabilities: null, diagnostics: {} }], 1, 1],
    ["second", [normalStatus, { ...normalStatus, diagnostics: [] }], 2, 2],
  ])("rejects a malformed %s status body", async (_position, statuses, expectedStatusReads, expectedAccessReads) => {
    const displayed = verifiedAccess();
    const access = vi.spyOn(api, "lanAccess").mockResolvedValue(displayed);
    const status = vi.spyOn(api, "status");
    for (const value of statuses) status.mockResolvedValueOnce(value as SystemStatus);
    mockProfile();
    const { popup, replace, close } = popupWindow();
    vi.spyOn(window, "open").mockReturnValue(popup);

    renderPanel();
    fireEvent.click(await screen.findByRole("button", { name: "Open LAN address" }));

    const alert = await screen.findByRole("alert");
    await waitFor(() => expect(document.activeElement).toBe(alert));
    expect(status).toHaveBeenCalledTimes(expectedStatusReads);
    expect(access).toHaveBeenCalledTimes(expectedAccessReads);
    expect(close).toHaveBeenCalledTimes(1);
    expect(replace).not.toHaveBeenCalled();
  });

  it.each([
    ["a recovery daemon", { ...normalStatus, daemon: "recovery" }],
    ["an unavailable generated runtime", { ...normalStatus, capabilities: { ...normalStatus.capabilities, generatedRuntime: false } }],
    ["a fake runtime", { ...normalStatus, capabilities: { ...normalStatus.capabilities, fakeRuntime: true } }],
    ["a missing Compose capability", { ...normalStatus, capabilities: { fakeRuntime: false, generatedRuntime: true, githubConnections: false } }],
    ["a missing fake-runtime capability", { ...normalStatus, capabilities: { composeRuntime: false, generatedRuntime: true, githubConnections: false } }],
    ["a missing generated-runtime capability", { ...normalStatus, capabilities: { composeRuntime: false, fakeRuntime: false, githubConnections: false } }],
    ["a missing GitHub capability", { ...normalStatus, capabilities: { composeRuntime: false, fakeRuntime: false, generatedRuntime: true } }],
  ])("rejects %s before reading fresh LAN access", async (_case, unsafeStatus) => {
    const displayed = verifiedAccess();
    const access = vi.spyOn(api, "lanAccess").mockResolvedValue(displayed);
    const status = vi.spyOn(api, "status").mockResolvedValue(unsafeStatus as SystemStatus);
    mockProfile();
    const { popup, replace, close } = popupWindow();
    vi.spyOn(window, "open").mockReturnValue(popup);

    renderPanel();
    fireEvent.click(await screen.findByRole("button", { name: "Open LAN address" }));

    const alert = await screen.findByRole("alert");
    await waitFor(() => expect(document.activeElement).toBe(alert));
    expect(status).toHaveBeenCalledTimes(1);
    expect(access).toHaveBeenCalledTimes(1);
    expect(close).toHaveBeenCalledTimes(1);
    expect(replace).not.toHaveBeenCalled();
    expect(screen.queryByText(lanURL)).toBeNull();
  });

  it("closes the pending window when recovery begins", async () => {
    const displayed = verifiedAccess();
    const access = vi.spyOn(api, "lanAccess").mockResolvedValue(displayed);
    const status = vi.spyOn(api, "status")
      .mockResolvedValueOnce(normalStatus)
      .mockRejectedValueOnce(new APIError({ status: 503, code: "gateway_reconciliation_required", detail: "Recovery required" }));
    mockProfile();
    const { popup, replace, close } = popupWindow();
    vi.spyOn(window, "open").mockReturnValue(popup);

    renderPanel();
    fireEvent.click(await screen.findByRole("button", { name: "Open LAN address" }));

    expect((await screen.findByRole("alert")).textContent).toContain("entered LAN recovery");
    expect(status).toHaveBeenCalledTimes(2);
    expect(access).toHaveBeenCalledTimes(2);
    expect(close).toHaveBeenCalledTimes(1);
    expect(replace).not.toHaveBeenCalled();
  });

  it("closes the pending window immediately when another API response signals recovery", async () => {
    const displayed = verifiedAccess();
    const firstStatus = deferred<SystemStatus>();
    vi.spyOn(api, "lanAccess").mockResolvedValue(displayed);
    vi.spyOn(api, "status").mockReturnValueOnce(firstStatus.promise);
    mockProfile();
    const { popup, replace, close } = popupWindow();
    vi.spyOn(window, "open").mockReturnValue(popup);
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({ code: "gateway_reconciliation_required", detail: "Recovery required" }), { status: 503 })));

    renderPanel();
    fireEvent.click(await screen.findByRole("button", { name: "Open LAN address" }));
    await screen.findByText("Checking LAN address");
    await act(async () => { await api.apps().catch(() => undefined); });

    expect((await screen.findByRole("alert")).textContent).toContain("entered LAN recovery");
    expect(close).toHaveBeenCalledTimes(1);
    expect(replace).not.toHaveBeenCalled();
    await act(async () => firstStatus.resolve(normalStatus));
  });

  it("closes a pending preopened window when the panel unmounts", async () => {
    const displayed = verifiedAccess();
    const firstStatus = deferred<SystemStatus>();
    vi.spyOn(api, "lanAccess").mockResolvedValue(displayed);
    vi.spyOn(api, "status").mockReturnValueOnce(firstStatus.promise).mockResolvedValueOnce(normalStatus);
    mockProfile();
    const { popup, replace, close } = popupWindow();
    vi.spyOn(window, "open").mockReturnValue(popup);

    const view = renderPanel();
    fireEvent.click(await screen.findByRole("button", { name: "Open LAN address" }));
    await screen.findByText("Checking LAN address");
    view.unmount();

    expect(close).toHaveBeenCalledTimes(1);
    await act(async () => firstStatus.resolve(normalStatus));
    expect(replace).not.toHaveBeenCalled();
  });

  it("closes and cancels a pending Open proof when LAN management becomes disabled", async () => {
    const displayed = verifiedAccess();
    const firstStatus = deferred<SystemStatus>();
    const access = vi.spyOn(api, "lanAccess").mockResolvedValue(displayed);
    const status = vi.spyOn(api, "status").mockReturnValueOnce(firstStatus.promise);
    mockProfile();
    const { popup, replace, close } = popupWindow();
    vi.spyOn(window, "open").mockReturnValue(popup);

    const view = renderPanel();
    fireEvent.click(await screen.findByRole("button", { name: "Open LAN address" }));
    await screen.findByText("Checking LAN address");
    view.rerender(<QueryClientProvider client={view.client}><LANApplicationAccessPanel appId={appId} role="administrator" userId={userId} enabled={false}/></QueryClientProvider>);

    await waitFor(() => expect(close).toHaveBeenCalledTimes(1));
    await act(async () => firstStatus.resolve(normalStatus));
    expect(replace).not.toHaveBeenCalled();
    expect(status).toHaveBeenCalledTimes(1);
    expect(access).toHaveBeenCalledTimes(1);
    expect(screen.queryByText(lanURL)).toBeNull();
  });

  it.each([
    ["role", "viewer", userId],
    ["operator identity", "administrator", ""],
  ])("cancels a pending Copy proof when the %s loses administrator authority", async (_case, nextRole, nextUserId) => {
    const displayed = verifiedAccess();
    const firstStatus = deferred<SystemStatus>();
    const access = vi.spyOn(api, "lanAccess").mockResolvedValue(displayed);
    const status = vi.spyOn(api, "status").mockReturnValueOnce(firstStatus.promise);
    mockProfile();
    const writeText = vi.fn();
    vi.stubGlobal("navigator", { clipboard: { writeText } });

    const view = renderPanel();
    fireEvent.click(await screen.findByRole("button", { name: "Copy address" }));
    await screen.findByText("Checking LAN address");
    view.rerender(<QueryClientProvider client={view.client}><LANApplicationAccessPanel appId={appId} role={nextRole} userId={nextUserId} enabled/></QueryClientProvider>);

    await act(async () => firstStatus.resolve(normalStatus));
    expect(writeText).not.toHaveBeenCalled();
    expect(status).toHaveBeenCalledTimes(1);
    expect(access).toHaveBeenCalledTimes(1);
    expect(screen.queryByText(lanURL)).toBeNull();
  });

  it("reports a blocked popup without issuing proof reads", async () => {
    const displayed = verifiedAccess();
    const access = vi.spyOn(api, "lanAccess").mockResolvedValue(displayed);
    const status = vi.spyOn(api, "status");
    mockProfile();
    vi.spyOn(window, "open").mockReturnValue(null);

    renderPanel();
    fireEvent.click(await screen.findByRole("button", { name: "Open LAN address" }));

    const alert = await screen.findByRole("alert");
    await waitFor(() => expect(document.activeElement).toBe(alert));
    expect(alert.textContent).toContain("blocked an isolated new window");
    expect(status).not.toHaveBeenCalled();
    expect(access).toHaveBeenCalledTimes(1);
    expect(screen.queryByText(lanURL)).toBeNull();
  });

  it("withholds the address and focuses guidance when clipboard writing fails", async () => {
    const displayed = verifiedAccess();
    vi.spyOn(api, "lanAccess").mockResolvedValue(displayed);
    vi.spyOn(api, "status").mockResolvedValue(normalStatus);
    mockProfile();
    const writeText = vi.fn().mockRejectedValue(new Error("permission denied"));
    vi.stubGlobal("navigator", { clipboard: { writeText } });

    renderPanel();
    fireEvent.click(await screen.findByRole("button", { name: "Copy address" }));

    const alert = await screen.findByRole("alert");
    await waitFor(() => expect(document.activeElement).toBe(alert));
    expect(alert.textContent).toContain("clipboard permission");
    expect(writeText).toHaveBeenCalledWith(lanURL);
    expect(screen.queryByText(lanURL)).toBeNull();
  });

  it("does not query LAN state for a viewer", () => {
    const access = vi.spyOn(api, "lanAccess");
    const profileRead = vi.spyOn(api, "lanGatewayProfile");
    renderPanel("viewer");
    expect(screen.getByText("An administrator can inspect and manage LAN sharing for this application.")).not.toBeNull();
    expect(access).not.toHaveBeenCalled();
    expect(profileRead).not.toHaveBeenCalled();
  });
});
