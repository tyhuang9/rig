import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { api } from "./api";
import { LANApplicationAccessPanel } from "./lan-application-access";

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

function renderPanel(role = "administrator", operatorId = userId) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return { client, ...render(<QueryClientProvider client={client}><LANApplicationAccessPanel appId={appId} role={role} userId={operatorId} enabled/></QueryClientProvider>) };
}

function mockProfile() {
  vi.spyOn(api, "lanGatewayProfile").mockResolvedValue({ expectedRevisionNumber: 2, candidates: [], desiredProfile: profile } as never);
}

describe("LANApplicationAccessPanel", () => {
  afterEach(() => {
    cleanup();
    window.sessionStorage.clear();
    vi.restoreAllMocks();
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
    expect(screen.queryByRole("link", { name: "Open LAN address" })).toBeNull();
  });

  it("retains and replays the exact uncertain disable request after reload", async () => {
    const initialAccess = { expectedRevisionNumber: 1, availability: "verified", desiredAccess: revision, grantClaim: grantClaim(), disableReview: disableReview(), url: "http://192.168.50.4:8100/", observedAt: new Date().toISOString() };
    vi.spyOn(crypto, "randomUUID").mockReturnValue(disableOperation);
    vi.spyOn(api, "lanAccess").mockResolvedValue(initialAccess as never);
    mockProfile();
    vi.spyOn(api, "lanGrant").mockResolvedValue({ claim: grantClaim(), observed: { availability: "committed" } } as never);
    const remove = vi.spyOn(api, "disableLANAccess").mockRejectedValueOnce(new Error("connection interrupted")).mockRejectedValueOnce(new Error("connection interrupted"));

    renderPanel();
    expect(await screen.findByRole("link", { name: "Open LAN address" })).not.toBeNull();
    fireEvent.click(await screen.findByRole("button", { name: "Review LAN sharing removal" }));
    fireEvent.click(screen.getByRole("button", { name: "Remove LAN sharing" }));
    const recovery = await screen.findByRole("heading", { name: "LAN sharing removal needs reconciliation" });
    await waitFor(() => expect(document.activeElement).toBe(recovery));
    expect(screen.queryByRole("link", { name: "Open LAN address" })).toBeNull();
    expect(JSON.parse(window.sessionStorage.getItem("rig-lan-access-disable:" + appId) || "{}")).toMatchObject({ version: 1, kind: "disable", appId, userId, request: { operationId: disableOperation, accessRevisionId: revision.id, approvalDigest: disableDigest } });

    cleanup();
    renderPanel();
    await screen.findByRole("heading", { name: "LAN sharing removal needs reconciliation" });
    expect(screen.queryByRole("link", { name: "Open LAN address" })).toBeNull();
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
    expect(screen.queryByRole("link", { name: "Open LAN address" })).toBeNull();
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
    await screen.findByRole("link", { name: "Open LAN address" });
    fireEvent.click(screen.getByRole("button", { name: "Check LAN access" }));
    expect(await screen.findByText("Checking LAN route")).not.toBeNull();
    expect(screen.queryByRole("link", { name: "Open LAN address" })).toBeNull();
    await act(async () => { refreshed.resolve(verified); });
    expect(await screen.findByRole("link", { name: "Open LAN address" })).not.toBeNull();
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
