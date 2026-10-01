import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { api } from "./api";
import { LANApplicationAccessPanel } from "./lan-application-access";

const appId = "11111111-1111-4111-8111-111111111111";
const profile = { id: "22222222-2222-4222-8222-222222222222", revisionNumber: 2, spec: { interfaceId: "nic-1", selectedIpv4: "192.168.50.4", portStart: 8100, portEnd: 8119 } };
const revision = {
  id: "33333333-3333-4333-8333-333333333333", appId, revisionNumber: 1, operationId: "44444444-4444-4444-8444-444444444444", specDigest: "b".repeat(64), approvedBy: "admin", approvedAt: "2026-10-01T00:00:00Z",
  allocation: { id: "55555555-5555-4555-8555-555555555555", appId, port: 8100, ownerOperationId: "44444444-4444-4444-8444-444444444444", gatewayProfileRevisionId: profile.id, gatewayProfileRevisionNumber: 2, state: "active" },
};

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((next) => { resolve = next; });
  return { promise, resolve };
}

function renderPanel(role = "administrator") {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(<QueryClientProvider client={client}><LANApplicationAccessPanel appId={appId} role={role} enabled/></QueryClientProvider>);
}

describe("LANApplicationAccessPanel", () => {
  afterEach(() => { cleanup(); vi.restoreAllMocks(); });

  it("requires reservation review, exact approval, and activation review before it grants a route", async () => {
    const access = vi.spyOn(api, "lanAccess")
      .mockResolvedValueOnce({ expectedRevisionNumber: 0, availability: "local_only" } as never)
      .mockResolvedValue({ expectedRevisionNumber: 1, availability: "unverified", desiredAccess: revision } as never);
    vi.spyOn(api, "lanGatewayProfile").mockResolvedValue({ expectedRevisionNumber: 2, candidates: [], desiredProfile: profile } as never);
    const reserve = vi.spyOn(api, "reserveLANAccess").mockImplementation(async (_id, request) => ({
      created: true,
      approvalDigest: "a".repeat(64),
      allocation: { ...revision.allocation, state: "reserved", ownerOperationId: request.operationId },
    }) as never);
    const approve = vi.spyOn(api, "approveLANAccess").mockResolvedValue({ created: true, revision } as never);
    const grant = vi.spyOn(api, "grantLANAccess").mockImplementation(async (_id, request) => ({
      created: true,
      claim: { attemptId: request.attemptId, appId, allocationId: revision.allocation.id, ownerOperationId: revision.operationId, accessRevisionId: revision.id, accessRevisionNumber: 1, port: 8100, state: "committed", updatedAt: "2026-10-01T00:00:00Z" },
    }) as never);
    vi.spyOn(api, "lanGrant").mockResolvedValue({ claim: { attemptId: "attempt", state: "committed" }, observed: { availability: "committed" } } as never);

    renderPanel();
    await screen.findByText("Local only");
    fireEvent.click(screen.getByRole("button", { name: "Reserve LAN port for review" }));

    const accessReview = await screen.findByRole("dialog", { name: "Review LAN application access" });
    expect(accessReview.textContent).toContain("Reserved port");
    expect(accessReview.textContent).toContain("8100");
    expect(accessReview.textContent).toContain("a".repeat(64));
    expect(screen.queryByText(/http:\/\/192\.168/)).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Approve LAN access" }));
    await waitFor(() => expect(approve).toHaveBeenCalledWith(appId, expect.objectContaining({
      allocationId: revision.allocation.id,
      expectedRevisionNumber: 0,
      approvalDigest: "a".repeat(64),
    })));

    const activationLauncher = await screen.findByRole("button", { name: "Review LAN activation" });
    await waitFor(() => expect(document.activeElement).toBe(activationLauncher));
    fireEvent.click(activationLauncher);
    const activationReview = await screen.findByRole("dialog", { name: "Review LAN activation" });
    expect(activationReview.textContent).toContain(revision.specDigest);
    fireEvent.click(screen.getByRole("button", { name: "Grant and attest LAN route" }));
    await waitFor(() => expect(grant).toHaveBeenCalledWith(appId, expect.objectContaining({
      accessRevisionId: revision.id,
      accessRevisionNumber: 1,
      approvalDigest: revision.specDigest,
      attemptId: expect.any(String),
    })));
    const claimHeading = await screen.findByRole("heading", { name: "Activation claim" });
    await waitFor(() => expect(document.activeElement).toBe(claimHeading));
    expect(reserve).toHaveBeenCalledWith(appId, expect.objectContaining({
      expectedRevisionNumber: 0,
      gatewayProfileRevisionId: profile.id,
      gatewayProfileRevisionNumber: 2,
      operationId: expect.any(String),
    }));
    expect(access).toHaveBeenCalled();
    expect(screen.queryByRole("link", { name: "Open LAN address" })).toBeNull();
  });

  it("withholds controls and the address from viewers", () => {
    const access = vi.spyOn(api, "lanAccess");
    const profileRead = vi.spyOn(api, "lanGatewayProfile");

    renderPanel("viewer");

    expect(screen.getByText("An administrator can inspect and manage LAN sharing for this application.")).not.toBeNull();
    expect(access).not.toHaveBeenCalled();
    expect(profileRead).not.toHaveBeenCalled();
    expect(screen.queryByRole("link", { name: /open lan/i })).toBeNull();
  });

  it("does not start a replacement grant when a refreshed read lacks its durable claim identity", async () => {
    vi.spyOn(api, "lanAccess").mockResolvedValue({ expectedRevisionNumber: 1, availability: "unverified", desiredAccess: revision } as never);
    vi.spyOn(api, "lanGatewayProfile").mockResolvedValue({ expectedRevisionNumber: 2, candidates: [], desiredProfile: profile } as never);
    const grant = vi.spyOn(api, "grantLANAccess");

    renderPanel();

    expect(await screen.findByText("Activation claim identity is unavailable.")).not.toBeNull();
    expect(screen.queryByRole("button", { name: /grant and attest/i })).toBeNull();
    expect(grant).not.toHaveBeenCalled();
  });

  it("returns focus to the reservation launcher when its review is cancelled", async () => {
    vi.spyOn(api, "lanAccess").mockResolvedValue({ expectedRevisionNumber: 0, availability: "local_only" } as never);
    vi.spyOn(api, "lanGatewayProfile").mockResolvedValue({ expectedRevisionNumber: 2, candidates: [], desiredProfile: profile } as never);
    vi.spyOn(api, "reserveLANAccess").mockImplementation(async (_id, request) => ({
      created: true,
      approvalDigest: "a".repeat(64),
      allocation: { ...revision.allocation, state: "reserved", ownerOperationId: request.operationId },
    }) as never);

    renderPanel();
    fireEvent.click(await screen.findByRole("button", { name: "Reserve LAN port for review" }));
    await screen.findByRole("dialog", { name: "Review LAN application access" });
    const launcher = screen.getByRole("button", { name: "Review reserved port", hidden: true });
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));

    await waitFor(() => expect(document.activeElement).toBe(launcher));
  });

  it("announces fresh verification and an expired LAN observation", async () => {
    const observedAt = new Date().toISOString();
    const verified = { expectedRevisionNumber: 1, availability: "verified", desiredAccess: revision, url: "http://192.168.50.4:8100/", observedAt };
    vi.spyOn(api, "lanAccess")
      .mockResolvedValueOnce(verified as never)
      .mockResolvedValueOnce({ ...verified, observedAt: "2000-01-01T00:00:00Z" } as never);
    vi.spyOn(api, "lanGatewayProfile").mockResolvedValue({ expectedRevisionNumber: 2, candidates: [], desiredProfile: profile } as never);

    renderPanel();
    await waitFor(() => expect(screen.getByRole("status").textContent).toBe(`LAN route verified at ${observedAt}.`));
    fireEvent.click(screen.getByRole("button", { name: "Check LAN access" }));

    expect(await screen.findByText("LAN route verification expired")).not.toBeNull();
    expect(screen.getByRole("status").textContent).toContain("LAN route verification expired");
    expect(screen.queryByRole("link", { name: "Open LAN address" })).toBeNull();
  });

  it("replays an ambiguous reservation only with the original exact request", async () => {
    const operationId = "66666666-6666-4666-8666-666666666666";
    vi.spyOn(crypto, "randomUUID").mockReturnValue(operationId);
    vi.spyOn(api, "lanAccess").mockResolvedValue({ expectedRevisionNumber: 0, availability: "local_only" } as never);
    vi.spyOn(api, "lanGatewayProfile").mockResolvedValue({ expectedRevisionNumber: 2, candidates: [], desiredProfile: profile } as never);
    const reserve = vi.spyOn(api, "reserveLANAccess")
      .mockRejectedValueOnce(new Error("connection interrupted"))
      .mockImplementation(async (_id, request) => ({
        created: true,
        approvalDigest: "a".repeat(64),
        allocation: { ...revision.allocation, state: "reserved", ownerOperationId: request.operationId },
      }) as never);

    renderPanel();
    fireEvent.click(await screen.findByRole("button", { name: "Reserve LAN port for review" }));
    expect(await screen.findByText("Reservation result needs reconciliation")).not.toBeNull();
    expect(screen.queryByRole("button", { name: "Reserve LAN port for review" })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Replay exact reservation" }));

    await screen.findByRole("dialog", { name: "Review LAN application access" });
    expect(reserve).toHaveBeenCalledTimes(2);
    expect(reserve.mock.calls[1]).toEqual(reserve.mock.calls[0]);
    expect(crypto.randomUUID).toHaveBeenCalledTimes(1);
  });

  it("reconciles an ambiguous reservation only when the current access state names that operation", async () => {
    const operationId = "77777777-7777-4777-8777-777777777777";
    vi.spyOn(crypto, "randomUUID").mockReturnValue(operationId);
    vi.spyOn(api, "lanAccess")
      .mockResolvedValueOnce({ expectedRevisionNumber: 0, availability: "local_only" } as never)
      .mockResolvedValueOnce({ expectedRevisionNumber: 1, availability: "unverified", desiredAccess: { ...revision, operationId } } as never);
    vi.spyOn(api, "lanGatewayProfile").mockResolvedValue({ expectedRevisionNumber: 2, candidates: [], desiredProfile: profile } as never);
    const reserve = vi.spyOn(api, "reserveLANAccess").mockRejectedValue(new Error("connection interrupted"));

    renderPanel();
    fireEvent.click(await screen.findByRole("button", { name: "Reserve LAN port for review" }));
    await screen.findByText("Reservation result needs reconciliation");
    fireEvent.click(screen.getByRole("button", { name: "Check current access state" }));

    expect((await screen.findAllByText(new RegExp(`current access state confirms reservation operation ${operationId}`))).length).toBeGreaterThan(0);
    expect(screen.queryByRole("button", { name: "Replay exact reservation" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Reserve LAN port for review" })).toBeNull();
    expect(reserve).toHaveBeenCalledTimes(1);
  });

  it("withholds a previously verified LAN address while it refreshes the attestation", async () => {
    const observedAt = new Date().toISOString();
    const verified = { expectedRevisionNumber: 1, availability: "verified", desiredAccess: revision, url: "http://192.168.50.4:8100/", observedAt };
    const refreshed = deferred<typeof verified>();
    vi.spyOn(api, "lanAccess")
      .mockResolvedValueOnce(verified as never)
      .mockReturnValueOnce(refreshed.promise as never);
    vi.spyOn(api, "lanGatewayProfile").mockResolvedValue({ expectedRevisionNumber: 2, candidates: [], desiredProfile: profile } as never);

    renderPanel();
    await screen.findByRole("link", { name: "Open LAN address" });
    fireEvent.click(screen.getByRole("button", { name: "Check LAN access" }));

    expect(await screen.findByText("Checking LAN route")).not.toBeNull();
    expect(screen.queryByRole("link", { name: "Open LAN address" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Copy address" })).toBeNull();
    await act(async () => { refreshed.resolve(verified); });
    expect(await screen.findByRole("link", { name: "Open LAN address" })).not.toBeNull();
  });
});
