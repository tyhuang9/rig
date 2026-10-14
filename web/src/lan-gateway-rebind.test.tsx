import { StrictMode } from "react";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { APIError, api } from "./api";
import { LANGatewayPanel } from "./lan-gateway";
import { LANGatewayRebindRecoveryScreen } from "./lan-gateway-rebind";

const predecessorId = "11111111-1111-4111-8111-111111111111";
const operationId = "22222222-2222-4222-8222-222222222222";
const successorRevisionId = "33333333-3333-4333-8333-333333333333";
const successorOperationId = "44444444-4444-4444-8444-444444444444";
const appOne = "55555555-5555-4555-8555-555555555555";
const appTwo = "66666666-6666-4666-8666-666666666666";
const candidate = { interfaceId: "nic-current", name: "Ethernet", selectedIpv4: "192.168.50.4", prefix: "24" };
const successorCandidate = { interfaceId: "nic-new", name: "Wi-Fi", selectedIpv4: "10.0.0.9", prefix: "24" };
const predecessor = {
  id: predecessorId,
  revisionNumber: 4,
  specDigest: "a".repeat(64),
  operationId: "77777777-7777-4777-8777-777777777777",
  approvedAt: "2026-10-01T00:00:00Z",
  approvedBy: "admin",
  spec: { interfaceId: candidate.interfaceId, selectedIpv4: candidate.selectedIpv4, portStart: 8100, portEnd: 8119 },
};
const profile = { expectedRevisionNumber: 4, candidates: [candidate, successorCandidate], desiredProfile: predecessor };
const upgrade = { observed: { availability: "serving" } };

function entry(input: Record<string, unknown>, ordinal: number, appId: string) {
  const number = String(ordinal).repeat(64).slice(0, 64);
  return {
    version: 2, operationId: input.operationId, ordinal, appId,
    allocationId: ordinal === 1 ? "88888888-8888-4888-8888-888888888888" : "99999999-9999-4999-8999-999999999999",
    allocationOwnerOperationId: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", allocationState: "active",
    accessRevisionId: ordinal === 1 ? "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb" : "cccccccc-cccc-4ccc-8ccc-cccccccccccc",
    accessRevisionNumber: 3, accessSpecDigest: number, grantAttemptId: ordinal === 1 ? "dddddddd-dddd-4ddd-8ddd-dddddddddddd" : "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee",
    grantStateSequence: 2, grantProtectedStateDigest: "f".repeat(64), sourceProfileRevisionId: predecessorId,
    sourceProfileRevisionNumber: 4, sourceProfileSpecDigest: "a".repeat(64), servingDeploymentId: ordinal === 1 ? "12121212-1212-4212-8212-121212121212" : "13131313-1313-4313-8313-131313131313",
    servingReleaseId: ordinal === 1 ? "14141414-1414-4414-8414-141414141414" : "15151515-1515-4515-8515-151515151515",
    servingSlot: ordinal === 1 ? "blue" : "green", routeGeneration: 2, port: 8103 + ordinal,
  };
}

function reviewFor(input: Record<string, unknown>) {
  return {
    version: 1,
    proposalDigest: "b".repeat(64), configureApprovalDigest: "c".repeat(64), rebindApprovalDigest: "d".repeat(64),
    spec: {
      version: 2, operationId: input.operationId,
      predecessor: { lineage: { kind: "gateway_upgrade", operationId: "16161616-1616-4616-8616-161616161616", profileRevisionId: predecessorId, profileRevisionNumber: 4, profileSpecDigest: "a".repeat(64) } },
      successorProtectedGeneration: 7, successorProfileRevisionId: input.successorProfileRevisionId,
      successorProfileRevisionNumber: input.successorProfileRevisionNumber, successorProfileOperationId: input.successorProfileOperationId,
      successorProfile: input.successorProfile, rosterVersion: 2, rosterDigest: "e".repeat(64), rosterCount: 2,
      runtimeHeadsVersion: 1, runtimeHeadsDigest: "f".repeat(64), runtimeHeadsCount: 2,
    },
    roster: [entry(input, 1, appOne), entry(input, 2, appTwo)],
    runtimeHeads: [
      { appId: appOne, deploymentId: "12121212-1212-4212-8212-121212121212", releaseId: "14141414-1414-4414-8414-141414141414", slot: "blue", generation: 2, updatedAt: "2026-10-01T00:00:00Z" },
      { appId: appTwo, deploymentId: "13131313-1313-4313-8313-131313131313", releaseId: "15151515-1515-4515-8515-151515151515", slot: "green", generation: 3, updatedAt: "2026-10-01T00:00:00Z" },
    ],
  };
}

function terminalStatus(id: string) {
  return {
    version: 1, operationId: id, availability: "available", active: false, historicalTerminal: true,
    terminalProof: "unavailable", currentProof: "unavailable", operationFence: "released", allowedActions: [],
    operation: { operationId: id, storedState: "committed", stateSequence: 4, databaseCommitObserved: true, terminalDisposition: "commit" },
  };
}

function terminalMutation(id: string) {
  return {
    created: true, recoveryRequired: false, terminalProof: "verified", currentProof: "verified", fenceReleased: true,
    operation: { operationId: id, storedState: "committed", stateSequence: 4, databaseCommitObserved: true, terminalDisposition: "commit" },
  };
}

function renderPanel() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  render(<QueryClientProvider client={client}><LANGatewayPanel role="administrator"/></QueryClientProvider>);
  return client;
}

async function chooseSuccessor() {
  fireEvent.change(await screen.findByLabelText("Interface and IPv4 address", { selector: "#lan-rebind-interface" }), { target: { value: `${successorCandidate.interfaceId}\u0000${successorCandidate.selectedIpv4}` } });
}

async function openReview() {
  await chooseSuccessor();
  fireEvent.click(screen.getByRole("button", { name: "Review gateway change" }));
  return screen.findByRole("dialog", { name: "Review gateway change" });
}

describe("LAN gateway rebind panel", () => {
  beforeEach(() => {
    vi.spyOn(api, "getControllerRecovery").mockResolvedValue({ version: 1, mode: "normal" } as never);
    vi.spyOn(api, "apps").mockResolvedValue({ items: [] } as never);
  });
  afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
    window.history.replaceState({}, "", "/");
  });

  it("requires two distinct approvals for the complete server roster", async () => {
    vi.spyOn(api, "lanGatewayProfile").mockResolvedValue(profile as never);
    vi.spyOn(api, "lanGatewayUpgrade").mockResolvedValue(upgrade as never);
    const inspect = vi.spyOn(api, "inspectLANGatewayRebind").mockImplementation(async (input) => reviewFor(input as unknown as Record<string, unknown>) as never);
    const commit = vi.spyOn(api, "commitLANGatewayRebind").mockImplementation(async (input) => terminalMutation(input.operationId) as never);

    renderPanel();
    const dialog = await openReview();
    expect(dialog.textContent).toContain("Applications included (2)");
    expect(dialog.textContent).toContain(appOne);
    expect(dialog.textContent).toContain(appTwo);
    const apply = screen.getByRole("button", { name: "Apply gateway change" }) as HTMLButtonElement;
    expect(apply.disabled).toBe(true);
    fireEvent.click(screen.getByRole("checkbox", { name: "I approve these new gateway settings." }));
    expect(apply.disabled).toBe(true);
    fireEvent.click(screen.getByRole("checkbox", { name: `I approve moving all 2 listed applications to this gateway.` }));
    fireEvent.click(apply);

    await waitFor(() => expect(commit).toHaveBeenCalledTimes(1));
    expect(inspect).toHaveBeenCalledWith(expect.objectContaining({
      operationId: expect.any(String), successorProfileRevisionNumber: 5,
      successorProfile: { interfaceId: "nic-new", selectedIpv4: "10.0.0.9", portStart: 8100, portEnd: 8119 },
    }));
    expect(commit).toHaveBeenCalledWith(expect.objectContaining({
      operationId: inspect.mock.calls[0][0].operationId,
      successorProfileRevisionId: inspect.mock.calls[0][0].successorProfileRevisionId,
      successorProfileOperationId: inspect.mock.calls[0][0].successorProfileOperationId,
    }));
    expect(window.location.search).toBe("");
  });

  it("fails closed when a review omits part of the roster", async () => {
    vi.spyOn(api, "lanGatewayProfile").mockResolvedValue(profile as never);
    vi.spyOn(api, "lanGatewayUpgrade").mockResolvedValue(upgrade as never);
    vi.spyOn(api, "inspectLANGatewayRebind").mockImplementation(async (input) => ({ ...reviewFor(input as unknown as Record<string, unknown>), roster: [] }) as never);

    renderPanel();
    await chooseSuccessor();
    fireEvent.click(screen.getByRole("button", { name: "Review gateway change" }));

    expect((await screen.findByRole("alert")).textContent).toContain("complete gateway and application review");
    expect(screen.queryByRole("dialog", { name: "Review gateway change" })).toBeNull();
  });

  it("clears both approvals when the current profile snapshot changes", async () => {
    vi.spyOn(api, "lanGatewayProfile").mockResolvedValue(profile as never);
    vi.spyOn(api, "lanGatewayUpgrade").mockResolvedValue(upgrade as never);
    vi.spyOn(api, "inspectLANGatewayRebind").mockImplementation(async (input) => reviewFor(input as unknown as Record<string, unknown>) as never);
    const commit = vi.spyOn(api, "commitLANGatewayRebind");
    const client = renderPanel();
    await openReview();
    fireEvent.click(screen.getByRole("checkbox", { name: "I approve these new gateway settings." }));
    fireEvent.click(screen.getByRole("checkbox", { name: `I approve moving all 2 listed applications to this gateway.` }));
    act(() => client.setQueryData(["lan-gateway-profile"], { ...profile, desiredProfile: { ...predecessor, revisionNumber: 5 } }));

    expect(await screen.findByText("Gateway review expired")).not.toBeNull();
    expect(screen.queryByRole("button", { name: "Apply gateway change" })).toBeNull();
    expect(commit).not.toHaveBeenCalled();
  });

  it("retains a lost response operation in the URL and resolves only that operation", async () => {
    vi.spyOn(api, "lanGatewayProfile").mockResolvedValue(profile as never);
    vi.spyOn(api, "lanGatewayUpgrade").mockResolvedValue(upgrade as never);
    vi.spyOn(api, "inspectLANGatewayRebind").mockImplementation(async (input) => reviewFor(input as unknown as Record<string, unknown>) as never);
    const commit = vi.spyOn(api, "commitLANGatewayRebind").mockRejectedValue(new TypeError("connection lost"));
    const exactRead = vi.spyOn(api, "getLANGatewayRebind")
      .mockRejectedValueOnce(new Error("unavailable"))
      .mockImplementation(async (id) => terminalStatus(id) as never);

    renderPanel();
    await openReview();
    fireEvent.click(screen.getByRole("checkbox", { name: "I approve these new gateway settings." }));
    fireEvent.click(screen.getByRole("checkbox", { name: `I approve moving all 2 listed applications to this gateway.` }));
    fireEvent.click(screen.getByRole("button", { name: "Apply gateway change" }));
    await waitFor(() => expect(commit).toHaveBeenCalledTimes(1));
    const submitted = commit.mock.calls[0][0].operationId;
    expect(window.location.search).toContain(`gatewayRebindOperation=${submitted}`);
    expect(screen.queryByRole("button", { name: "Review gateway change" })).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: "Check operation status" }));
    await screen.findByText("Operation status unavailable");
    fireEvent.click(screen.getByRole("button", { name: "Check operation status" }));
    await waitFor(() => expect(exactRead).toHaveBeenLastCalledWith(submitted));
    expect(window.location.search).toContain(`gatewayRebindOperation=${submitted}`);
    fireEvent.click(await screen.findByRole("button", { name: "Return to gateway review" }));
    await waitFor(() => expect(window.location.search).toBe(""));
    expect(commit).toHaveBeenCalledTimes(1);
  });

  it("keeps an ambiguous commit pinned and does not submit it again", async () => {
    vi.spyOn(api, "lanGatewayProfile").mockResolvedValue(profile as never);
    vi.spyOn(api, "lanGatewayUpgrade").mockResolvedValue(upgrade as never);
    vi.spyOn(api, "inspectLANGatewayRebind").mockImplementation(async (input) => reviewFor(input as unknown as Record<string, unknown>) as never);
    const commit = vi.spyOn(api, "commitLANGatewayRebind").mockRejectedValue(new APIError({ status: 503, code: "service_unavailable", detail: "Unavailable" }));

    renderPanel();
    await openReview();
    fireEvent.click(screen.getByRole("checkbox", { name: "I approve these new gateway settings." }));
    fireEvent.click(screen.getByRole("checkbox", { name: `I approve moving all 2 listed applications to this gateway.` }));
    fireEvent.click(screen.getByRole("button", { name: "Apply gateway change" }));

    await waitFor(() => expect(commit).toHaveBeenCalledTimes(1));
    expect(await screen.findByText("Gateway change needs confirmation")).not.toBeNull();
    expect(screen.queryByRole("dialog", { name: "Review gateway change" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Apply gateway change" })).toBeNull();
  });

  it("resolves only a validated-absent conflict after fresh normal recovery discovery", async () => {
    vi.spyOn(api, "lanGatewayProfile").mockResolvedValue(profile as never);
    vi.spyOn(api, "lanGatewayUpgrade").mockResolvedValue(upgrade as never);
    vi.spyOn(api, "inspectLANGatewayRebind").mockImplementation(async (input) => reviewFor(input as unknown as Record<string, unknown>) as never);
    const commit = vi.spyOn(api, "commitLANGatewayRebind").mockRejectedValue(new APIError({ status: 409, code: "revision_conflict", detail: "Changed" }));
    vi.spyOn(api, "getLANGatewayRebind").mockRejectedValue(new APIError({ status: 404, code: "lan_gateway_rebind_not_found", detail: "Absent" }));

    renderPanel();
    await openReview();
    fireEvent.click(screen.getByRole("checkbox", { name: "I approve these new gateway settings." }));
    fireEvent.click(screen.getByRole("checkbox", { name: `I approve moving all 2 listed applications to this gateway.` }));
    fireEvent.click(screen.getByRole("button", { name: "Apply gateway change" }));
    await waitFor(() => expect(commit).toHaveBeenCalledTimes(1));
    fireEvent.click(screen.getByRole("button", { name: "Check operation status" }));
    expect(await screen.findByText(/Rig confirmed no recorded operation for this ID/)).not.toBeNull();
    fireEvent.click(await screen.findByRole("button", { name: "Return to gateway review" }));
    await waitFor(() => expect(window.location.search).toBe(""));
    expect(commit).toHaveBeenCalledTimes(1);
  });
});

describe("LAN gateway rebind recovery", () => {
  afterEach(() => { cleanup(); vi.restoreAllMocks(); });

  const activeStatus = {
    ...terminalStatus(operationId), active: true, historicalTerminal: false, operationFence: "held",
    operation: { operationId, storedState: "prepared", stateSequence: 1, databaseCommitObserved: false },
  };

  it.each([
    ["conflict", new APIError({ status: 409, code: "lan_gateway_conflict", detail: "Changed" })],
    ["security check", new APIError({ status: 403, code: "csrf_failed", detail: "Expired" })],
    ["unavailable", new APIError({ status: 503, code: "service_unavailable", detail: "Unavailable" })],
  ])("keeps status-first recovery after a %s despite a late mount read", async (_name, failure) => {
    let resolveLate!: (value: never) => void;
    const late = new Promise<never>((resolve) => { resolveLate = resolve; });
    const statusRead = vi.spyOn(api, "getLANGatewayRebind")
      .mockReturnValueOnce(late).mockResolvedValue(activeStatus as never);
    const preview = vi.spyOn(api, "inspectLANGatewayRebindRecovery").mockResolvedValue({
      version: 1, operationId, recoveryDigest: "a".repeat(64), recoveryApprovalDigest: "b".repeat(64),
      status: { ...activeStatus, allowedActions: ["reconcile"] },
    } as never);
    const reconcile = vi.spyOn(api, "reconcileLANGatewayRebind").mockRejectedValue(failure);
    render(<StrictMode><LANGatewayRebindRecoveryScreen operationId={operationId} onSignOut={() => undefined} onCheckSystemStatus={async () => undefined}/></StrictMode>);

    fireEvent.click(await screen.findByRole("button", { name: "Review recovery" }));
    fireEvent.click(await screen.findByRole("checkbox", { name: "I approve recovery of this gateway operation." }));
    fireEvent.click(screen.getByRole("button", { name: "Recover this operation" }));
    await screen.findByRole("button", { name: "Check operation status" });
    await act(async () => { resolveLate(activeStatus as never); await late; });
    expect(screen.queryByRole("button", { name: "Review recovery" })).toBeNull();
    expect(screen.queryByRole("checkbox")).toBeNull();
    expect(preview).toHaveBeenCalledTimes(1);
    expect(reconcile).toHaveBeenCalledTimes(1);
    fireEvent.click(screen.getByRole("button", { name: "Check operation status" }));
    await screen.findByRole("button", { name: "Review recovery" });
    expect(statusRead).toHaveBeenCalledTimes(3);
  });

  it("requires a fresh exact status after a failed recovery preview", async () => {
    const statusRead = vi.spyOn(api, "getLANGatewayRebind").mockResolvedValue(activeStatus as never);
    const preview = vi.spyOn(api, "inspectLANGatewayRebindRecovery").mockRejectedValue(new Error("connection lost"));
    const reconcile = vi.spyOn(api, "reconcileLANGatewayRebind");
    render(<LANGatewayRebindRecoveryScreen operationId={operationId} onSignOut={() => undefined} onCheckSystemStatus={async () => undefined}/>);
    fireEvent.click(await screen.findByRole("button", { name: "Review recovery" }));
    await screen.findByRole("button", { name: "Check operation status" });
    expect(screen.queryByRole("button", { name: "Review recovery" })).toBeNull();
    expect(screen.queryByRole("checkbox")).toBeNull();
    expect(preview).toHaveBeenCalledTimes(1);
    expect(reconcile).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "Check operation status" }));
    await screen.findByRole("button", { name: "Review recovery" });
    expect(statusRead).toHaveBeenCalledTimes(2);
  });


  it("keeps a historical outcome in recovery until current and terminal proof are qualified", async () => {
    const status = { ...terminalStatus(operationId), terminalProof: "unavailable", currentProof: "unavailable" };
    vi.spyOn(api, "getLANGatewayRebind").mockResolvedValue(status as never);
    const checkSystem = vi.fn().mockResolvedValue(undefined);
    render(<LANGatewayRebindRecoveryScreen operationId={operationId} onSignOut={() => undefined} onCheckSystemStatus={checkSystem}/>);

    expect(await screen.findByText("Terminal outcome recorded")).not.toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Check system status after restart" }));
    expect(checkSystem).toHaveBeenCalledTimes(1);
    expect(screen.getByText("Database commit recorded").nextElementSibling?.textContent).toBe("Yes");
  });
});
