import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { APIError, api } from "./api";
import { LANGatewayUpgradeRecoveryScreen } from "./lan-gateway-upgrade-recovery";

const operationId = "11111111-1111-4111-8111-111111111111";
const profileRevisionId = "22222222-2222-4222-8222-222222222222";
const actionDigest = "a".repeat(64);
const profileSpecDigest = "b".repeat(64);

const upgradeRead = {
  desiredClaim: {
    operationId,
    profileRevisionId,
    profileRevisionNumber: 5,
    profileSpecDigest,
    state: "unresolved",
    updatedAt: "2026-10-01T10:00:00Z",
  },
  proposal: {
    profileRevisionId,
    profileRevisionNumber: 5,
    actionDigest,
  },
  observed: {
    operationId,
    availability: "unknown",
  },
};

function locator() {
  return { version: 1, mode: "recovery_only", recovery: { kind: "gateway_upgrade", operationId } };
}

function mutation() {
  return {
    created: false,
    claim: {
      ...upgradeRead.desiredClaim,
      state: "committed",
    },
  };
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<T>((done, fail) => { resolve = done; reject = fail; });
  return { promise, resolve, reject };
}

function renderRecovery() {
  const onSignOut = vi.fn();
  const onCheckSystemStatus = vi.fn().mockResolvedValue(undefined);
  render(<LANGatewayUpgradeRecoveryScreen operationId={operationId} onSignOut={onSignOut} onCheckSystemStatus={onCheckSystemStatus}/>);
  return { onSignOut, onCheckSystemStatus };
}

async function reviewAndConfirm() {
  fireEvent.click(screen.getByRole("button", { name: "Review upgrade recovery" }));
  const checkbox = await screen.findByRole("checkbox", { name: "I approve recovery of this gateway upgrade." });
  fireEvent.click(checkbox);
}

describe("LANGatewayUpgradeRecoveryScreen", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
    vi.spyOn(api, "getControllerRecovery").mockResolvedValue(locator() as never);
    vi.spyOn(api, "lanGatewayUpgrade").mockResolvedValue(upgradeRead as never);
  });

  afterEach(() => cleanup());

  it("does not mutate on mount and shows the pinned recovery entry", () => {
    const recover = vi.spyOn(api, "recoverLANGatewayUpgrade");

    renderRecovery();

    expect(screen.getByRole("heading", { name: "LAN gateway upgrade recovery required" })).not.toBeNull();
    expect(screen.getByRole("button", { name: "Review upgrade recovery" })).not.toBeNull();
    expect(recover).not.toHaveBeenCalled();
    expect(document.title).toBe("LAN gateway upgrade recovery · hostd");
  });

  it("returns keyboard focus to the review action after cancellation", async () => {
    renderRecovery();
    await reviewAndConfirm();
    const cancel = screen.getByRole("button", { name: "Cancel" });
    cancel.focus();
    fireEvent.click(cancel);
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole("button", { name: "Review upgrade recovery" })));
    expect(screen.queryByRole("checkbox")).toBeNull();
  });

  it("announces and focuses a nonterminal status check without granting consent", async () => {
    renderRecovery();
    fireEvent.click(screen.getByRole("button", { name: "Check recovery again" }));
    const summary = await screen.findByText("Operation status checked");
    await waitFor(() => expect(document.activeElement).toBe(summary.parentElement));
    expect(summary.parentElement?.getAttribute("role")).toBe("status");
    expect(summary.parentElement?.textContent).toContain("unresolved");
    expect(screen.queryByRole("checkbox")).toBeNull();
  });

  it("offers only an explicit system-status check when normal discovery follows an unknown result", async () => {
    const recover = vi.spyOn(api, "recoverLANGatewayUpgrade").mockRejectedValue(new Error("lost response"));
    const { onCheckSystemStatus } = renderRecovery();
    await reviewAndConfirm();
    fireEvent.click(screen.getByRole("button", { name: "Recover this upgrade" }));
    const check = await screen.findByRole("button", { name: "Check operation status" });
    vi.mocked(api.getControllerRecovery).mockResolvedValue({ version: 1, mode: "normal" } as never);
    fireEvent.click(check);

    const systemCheck = await screen.findByRole("button", { name: "Check system status after restart" });
    expect(screen.getByText("System status confirmation required").parentElement?.textContent).toContain("does not confirm the upgrade outcome");
    expect(onCheckSystemStatus).not.toHaveBeenCalled();
    expect(recover).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole("checkbox")).toBeNull();
    expect(screen.queryByRole("button", { name: "Review upgrade recovery" })).toBeNull();
    fireEvent.click(systemCheck);
    await waitFor(() => expect(onCheckSystemStatus).toHaveBeenCalledTimes(1));
  });

  it.each([
    { version: 1, mode: "normal", recovery: null },
    { version: 1, mode: "normal", recovery: { kind: "gateway_upgrade", operationId } },
    { version: 2, mode: "normal" },
  ])("does not offer a system-status exit for malformed normal discovery %j", async (discovery) => {
    vi.mocked(api.getControllerRecovery).mockResolvedValue(discovery as never);
    const { onCheckSystemStatus } = renderRecovery();
    fireEvent.click(screen.getByRole("button", { name: "Check recovery again" }));
    await screen.findByRole("alert");
    expect(screen.queryByRole("button", { name: "Check system status after restart" })).toBeNull();
    expect(api.lanGatewayUpgrade).not.toHaveBeenCalled();
    expect(onCheckSystemStatus).not.toHaveBeenCalled();
  });

  it("reviews the complete exact snapshot and sends the canonical recovery body once", async () => {
    const recover = vi.spyOn(api, "recoverLANGatewayUpgrade").mockResolvedValue({ malformed: true } as never);
    renderRecovery();

    await reviewAndConfirm();

    expect(screen.getByText("Gateway profile digest").nextElementSibling?.textContent).toBe(profileSpecDigest);
    expect(screen.getByText("Upgrade action digest").nextElementSibling?.textContent).toBe(actionDigest);
    fireEvent.click(screen.getByRole("button", { name: "Recover this upgrade" }));

    await waitFor(() => expect(recover).toHaveBeenCalledWith({
      operationId,
      profileRevisionId,
      profileRevisionNumber: 5,
      actionDigest,
    }));
    expect(await screen.findByRole("button", { name: "Check operation status" })).not.toBeNull();
    expect(screen.queryByRole("button", { name: "Recover this upgrade" })).toBeNull();
  });

  it("fails closed when the fresh locator does not identify this recovery-only upgrade", async () => {
    vi.spyOn(api, "getControllerRecovery").mockResolvedValue({
      version: 1,
      mode: "normal",
      recovery: { kind: "gateway_upgrade", operationId },
    } as never);
    const read = vi.spyOn(api, "lanGatewayUpgrade");
    const recover = vi.spyOn(api, "recoverLANGatewayUpgrade");
    renderRecovery();

    fireEvent.click(screen.getByRole("button", { name: "Review upgrade recovery" }));

    expect((await screen.findByRole("alert")).textContent).toContain("Recovery cannot be confirmed");
    expect(read).not.toHaveBeenCalled();
    expect(recover).not.toHaveBeenCalled();
  });

  it.each([
    ["claim", { ...upgradeRead, desiredClaim: { ...upgradeRead.desiredClaim, operationId: "33333333-3333-4333-8333-333333333333" } }],
    ["proposal", { ...upgradeRead, proposal: { ...upgradeRead.proposal, profileRevisionNumber: 6 } }],
    ["observation", { ...upgradeRead, observed: { availability: "serving", operationId: "33333333-3333-4333-8333-333333333333" } }],
  ])("fails closed on a mismatched %s", async (_name, read) => {
    vi.spyOn(api, "lanGatewayUpgrade").mockResolvedValue(read as never);
    const recover = vi.spyOn(api, "recoverLANGatewayUpgrade");
    renderRecovery();

    fireEvent.click(screen.getByRole("button", { name: "Review upgrade recovery" }));

    expect((await screen.findByRole("alert")).textContent).toContain("Recovery cannot be confirmed");
    expect(recover).not.toHaveBeenCalled();
  });

  it("clears consent and suppresses POST when any reviewed upgrade field changes", async () => {
    vi.spyOn(api, "lanGatewayUpgrade")
      .mockResolvedValueOnce(upgradeRead as never)
      .mockResolvedValueOnce({ ...upgradeRead, desiredClaim: { ...upgradeRead.desiredClaim, updatedAt: "2026-10-01T10:00:05Z" } } as never);
    const recover = vi.spyOn(api, "recoverLANGatewayUpgrade");
    renderRecovery();

    await reviewAndConfirm();
    fireEvent.click(screen.getByRole("button", { name: "Recover this upgrade" }));

    expect((await screen.findByRole("alert")).textContent).toContain("changed during review");
    expect(recover).not.toHaveBeenCalled();
    expect(screen.queryByRole("checkbox", { name: "I approve recovery of this gateway upgrade." })).toBeNull();
  });

  it("does not duplicate a pending recovery request", async () => {
    const pending = deferred<ReturnType<typeof mutation>>();
    const recover = vi.spyOn(api, "recoverLANGatewayUpgrade").mockReturnValue(pending.promise as never);
    renderRecovery();

    await reviewAndConfirm();
    const submit = screen.getByRole("button", { name: "Recover this upgrade" });
    fireEvent.click(submit);
    fireEvent.click(submit);

    await waitFor(() => expect(recover).toHaveBeenCalledTimes(1));
    pending.resolve(mutation());
    expect(await screen.findByRole("button", { name: "Check operation status" })).not.toBeNull();
  });

  it("consumes consent without posting when the recovery pin changes before submission", async () => {
    const recover = vi.spyOn(api, "recoverLANGatewayUpgrade");
    renderRecovery();
    await reviewAndConfirm();
    vi.mocked(api.getControllerRecovery).mockResolvedValue({
      ...locator(), recovery: { kind: "gateway_upgrade", operationId: "33333333-3333-4333-8333-333333333333" },
    } as never);

    fireEvent.click(screen.getByRole("button", { name: "Recover this upgrade" }));

    expect(await screen.findByRole("button", { name: "Check operation status" })).not.toBeNull();
    expect(recover).not.toHaveBeenCalled();
    expect(api.lanGatewayUpgrade).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole("checkbox")).toBeNull();
  });

  it("ignores a delayed snapshot after the selected recovery operation changes", async () => {
    const oldRead = deferred<typeof upgradeRead>();
    const otherId = "33333333-3333-4333-8333-333333333333";
    vi.mocked(api.lanGatewayUpgrade).mockReturnValueOnce(oldRead.promise as never);
    const props = { onSignOut: () => undefined, onCheckSystemStatus: async () => undefined };
    const view = render(<LANGatewayUpgradeRecoveryScreen operationId={operationId} {...props}/>);
    fireEvent.click(screen.getByRole("button", { name: "Review upgrade recovery" }));
    await waitFor(() => expect(api.lanGatewayUpgrade).toHaveBeenCalledTimes(1));

    view.rerender(<LANGatewayUpgradeRecoveryScreen operationId={otherId} {...props}/>);
    vi.mocked(api.getControllerRecovery).mockResolvedValue({
      ...locator(), recovery: { kind: "gateway_upgrade", operationId: otherId },
    } as never);
    vi.mocked(api.lanGatewayUpgrade).mockResolvedValue({
      ...upgradeRead,
      desiredClaim: { ...upgradeRead.desiredClaim, operationId: otherId },
      observed: { ...upgradeRead.observed, operationId: otherId },
    } as never);
    fireEvent.click(screen.getByRole("button", { name: "Review upgrade recovery" }));
    await screen.findByRole("checkbox");
    await act(async () => { oldRead.resolve(upgradeRead); await oldRead.promise; });

    expect(screen.getByText("Recorded operation").nextElementSibling?.textContent).toBe(otherId);
    expect((screen.getByRole("checkbox") as HTMLInputElement).checked).toBe(false);
    expect((screen.getByRole("button", { name: "Recover this upgrade" }) as HTMLButtonElement).disabled).toBe(true);
  });

  it.each([
    [new APIError({ status: 403, code: "csrf_failed", detail: "CSRF failed" }), "CSRF failure"],
    [new APIError({ status: 409, code: "lan_gateway_replay_conflict", detail: "Other administrator replayed" }), "other administrator replay"],
    [new APIError({ status: 503, code: "lan_gateway_unavailable", detail: "Unavailable" }), "lost response"],
  ])("treats a %s as an unknown result until exact status is checked", async (failure) => {
    const recover = vi.spyOn(api, "recoverLANGatewayUpgrade").mockRejectedValue(failure);
    renderRecovery();

    await reviewAndConfirm();
    fireEvent.click(screen.getByRole("button", { name: "Recover this upgrade" }));

    expect((await screen.findByRole("alert")).textContent).toContain("result is not confirmed");
    expect(recover).toHaveBeenCalledTimes(1);
    expect(screen.getByRole("button", { name: "Check operation status" })).not.toBeNull();
    expect(screen.queryByRole("button", { name: "Recover this upgrade" })).toBeNull();
  });

  it.each(["serving", "unavailable"])("does not offer consent or POST for an initially terminal %s gateway", async (availability) => {
    vi.spyOn(api, "lanGatewayUpgrade").mockResolvedValue({
      ...upgradeRead,
      desiredClaim: { ...upgradeRead.desiredClaim, state: "committed" },
      observed: { operationId, availability },
    } as never);
    const recover = vi.spyOn(api, "recoverLANGatewayUpgrade");
    renderRecovery();

    fireEvent.click(screen.getByRole("button", { name: "Review upgrade recovery" }));

    await screen.findByText(availability === "serving" ? "Recorded gateway upgrade is terminal" : "Terminal outcome needs a live gateway check");
    expect(screen.queryByRole("checkbox", { name: "I approve recovery of this gateway upgrade." })).toBeNull();
    expect(screen.queryByRole("button", { name: "Recover this upgrade" })).toBeNull();
    expect(recover).not.toHaveBeenCalled();
  });
  it("keeps recovery isolated for a terminal claim without a serving observation", async () => {
    vi.spyOn(api, "lanGatewayUpgrade").mockResolvedValue({
      ...upgradeRead,
      desiredClaim: { ...upgradeRead.desiredClaim, state: "committed" },
      observed: { operationId, availability: "unknown" },
    } as never);
    const onCheckSystemStatus = vi.fn().mockResolvedValue(undefined);
    render(<LANGatewayUpgradeRecoveryScreen operationId={operationId} onSignOut={() => undefined} onCheckSystemStatus={onCheckSystemStatus}/>);

    fireEvent.click(screen.getByRole("button", { name: "Review upgrade recovery" }));

    expect(await screen.findByText("Terminal outcome needs a live gateway check")).not.toBeNull();
    expect(screen.queryByRole("button", { name: "Check system status after restart" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Recover this upgrade" })).toBeNull();
    expect(onCheckSystemStatus).not.toHaveBeenCalled();
  });

  it("delegates a terminal serving gateway to the explicit system-status check", async () => {
    vi.spyOn(api, "lanGatewayUpgrade").mockResolvedValue({
      ...upgradeRead,
      desiredClaim: { ...upgradeRead.desiredClaim, state: "committed" },
      observed: { operationId, availability: "serving" },
    } as never);
    const { onCheckSystemStatus } = renderRecovery();

    fireEvent.click(screen.getByRole("button", { name: "Review upgrade recovery" }));
    const check = await screen.findByRole("button", { name: "Check system status after restart" });
    fireEvent.click(check);

    await waitFor(() => expect(onCheckSystemStatus).toHaveBeenCalledTimes(1));
  });
});
