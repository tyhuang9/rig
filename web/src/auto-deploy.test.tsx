import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { APIError, api, type ApplicationAutoDeployStatus } from "./api";
import { AutoDeployPanel } from "./auto-deploy";

const appId = "app/one";
const status = {
  applicationId: appId, revision: 0, enabled: false, state: "disabled", source: {
    type: "github", connectionId: "connection-1", repositoryOwner: "octo", repositoryName: "service", trackedBranch: "main",
  }, sourceScopeActive: false, latestResolvedSha: "abcdef1234567890", activeSha: "", lastSuccessfulDeployedSha: "fedcba9876543210", pausedSha: "", retryAttempt: 0, updatedAt: "2026-08-26T12:00:00Z",
};
const relay = { availability: "available", state: "ready", paused: false, outcome: "ready", diagnosticsUnavailable: false, pendingCommands: 0, activeLeases: 0, expiredLeases: 0, oldestPendingAgeSeconds: 0, observerDropped: 0 };
const connection = { id: "connection-1", provider: "github", status: "connected", credentialGeneration: 1, createdAt: "2026-08-01T00:00:00Z", updatedAt: "2026-08-01T00:00:00Z" };

function mockData(overrides: Partial<ApplicationAutoDeployStatus> = {}) {
  vi.spyOn(api, "getApplicationAutoDeploy").mockResolvedValue({ ...status, ...overrides } as never);
  vi.spyOn(api, "relayStatus").mockResolvedValue(relay as never);
  vi.spyOn(api, "sourceConnections").mockResolvedValue({ items: [connection] } as never);
  vi.spyOn(api, "updateApplicationAutoDeploy").mockResolvedValue({ ...status, ...overrides } as never);
  vi.spyOn(api, "resumeApplicationAutoDeploy").mockResolvedValue({ ...status, ...overrides, state: "idle", enabled: true } as never);
}

function renderPanel(props: Partial<React.ComponentProps<typeof AutoDeployPanel>> = {}) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(<QueryClientProvider client={client}><AutoDeployPanel appId={appId} composeRuntime githubConnections {...props}/></QueryClientProvider>);
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: Error) => void;
  const promise = new Promise<T>((done, fail) => { resolve = done; reject = fail; });
  return { promise, resolve, reject };
}

describe("AutoDeployPanel", () => {
  beforeEach(() => { vi.restoreAllMocks(); mockData(); });
  afterEach(() => { vi.useRealTimers(); cleanup(); });

  it("shows the intentional revision-zero disabled state and enables only after local prerequisites", async () => {
    renderPanel();
    expect(await screen.findByText("Off")).not.toBeNull();
    const enable = screen.getByRole("button", { name: "Enable" });
    expect(enable.hasAttribute("disabled")).toBe(false);
    expect(screen.getByText(/Rig verifies repository relay authorization and controller access/).textContent).toContain("auto-deploy stays off");
    fireEvent.click(enable);
    await waitFor(() => expect(api.updateApplicationAutoDeploy).toHaveBeenCalledWith(appId, { expectedRevision: 0, enabled: true }));
    expect(screen.getByText("Auto-deploy status updated.")).not.toBeNull();
  });

  it("does not make source scope activity an enable prerequisite", async () => {
    mockData({ sourceScopeActive: false });
    renderPanel();
    expect((await screen.findByRole("button", { name: "Enable" }) as HTMLButtonElement).disabled).toBe(false);
    expect(screen.getByText("Not subscribed while off")).not.toBeNull();
  });

  it.each([
    ["GitHub connections are disabled by the administrator on this controller.", { githubConnections: false }],
    ["Connect the GitHub source used by this application.", {}],
  ])("blocks enabling for gate prerequisite %s", async (reason, props) => {
    if (reason === "Connect the GitHub source used by this application.") vi.mocked(api.sourceConnections).mockResolvedValue({ items: [] } as never);
    renderPanel(props);
    expect((await screen.findByRole("button", { name: "Enable" }) as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByText(reason)).not.toBeNull();
  });

  it.each([
    ["local", "Auto-deploy requires a GitHub source."],
    ["github", "A compatible runtime is required."],
  ])("communicates static enable prerequisites", async (sourceType, text) => {
    mockData({ source: { ...status.source, type: sourceType } });
    renderPanel({ composeRuntime: sourceType !== "github" ? true : false });
    expect((await screen.findByRole("button", { name: "Enable" }) as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByText(text)).not.toBeNull();
  });

  it("allows generated-only controllers to enable auto-deploy", async () => {
    renderPanel({ composeRuntime: false, generatedRuntime: true });
    expect((await screen.findByRole("button", { name: "Enable" }) as HTMLButtonElement).disabled).toBe(false);
  });

  it("does not fetch GitHub prerequisites for a local application source", async () => {
    mockData({ source: { ...status.source, type: "local" } });
    renderPanel();
    expect(await screen.findByText("Auto-deploy requires a GitHub source.")).not.toBeNull();
    expect(api.getApplicationAutoDeploy).toHaveBeenCalledTimes(1);
    expect(api.relayStatus).not.toHaveBeenCalled();
    expect(api.sourceConnections).not.toHaveBeenCalled();
  });

  it("fetches both GitHub prerequisites after valid same-app status loads", async () => {
    renderPanel();
    await screen.findByText("Off");
    await waitFor(() => expect(api.relayStatus).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(api.sourceConnections).toHaveBeenCalledTimes(1));
  });

  it("blocks enabling while relay initializes or is unavailable but preserves turn-off during relay failure", async () => {
    vi.mocked(api.relayStatus).mockResolvedValue({ ...relay, availability: "initializing" } as never);
    renderPanel();
    expect((await screen.findByRole("button", { name: "Enable" }) as HTMLButtonElement).disabled).toBe(true);
    cleanup();
    mockData({ enabled: true, state: "idle" });
    vi.mocked(api.relayStatus).mockRejectedValue(new Error("relay offline"));
    renderPanel();
    const off = await screen.findByRole("button", { name: "Turn off" });
    expect((off as HTMLButtonElement).disabled).toBe(false);
    fireEvent.click(off);
    await waitFor(() => expect(api.updateApplicationAutoDeploy).toHaveBeenCalledWith(appId, { expectedRevision: 0, enabled: false }));
  });

  it("blocks enable for an unavailable relay and preserves turn-off during relay and source outages", async () => {
    vi.mocked(api.relayStatus).mockResolvedValue({ ...relay, availability: "unavailable" } as never);
    renderPanel();
    expect((await screen.findByRole("button", { name: "Enable" }) as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByText("The relay is unavailable.")).not.toBeNull();
    cleanup();
    mockData({ enabled: true, state: "idle" });
    vi.mocked(api.relayStatus).mockRejectedValue(new Error("relay offline"));
    vi.mocked(api.sourceConnections).mockRejectedValue(new Error("source offline"));
    renderPanel();
    const off = await screen.findByRole("button", { name: "Turn off" });
    expect((off as HTMLButtonElement).disabled).toBe(false);
  });

  it("keeps source connection errors local and disables only enable", async () => {
    vi.mocked(api.sourceConnections).mockRejectedValue(new Error("connection offline"));
    renderPanel();
    expect((await screen.findByRole("button", { name: "Enable" }) as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByRole("button", { name: "Retry connection check" })).not.toBeNull();
  });

  it("uses an alert for an auto-deploy query error while preserving the sole polite live region", async () => {
    vi.mocked(api.getApplicationAutoDeploy).mockRejectedValue(new Error("status offline"));
    renderPanel();
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("status offline");
    expect(screen.getAllByRole("status")).toHaveLength(1);
  });

  it("reserves loading geometry with decorative skeletons and no additional live region", () => {
    const pending = deferred<ApplicationAutoDeployStatus>();
    vi.mocked(api.getApplicationAutoDeploy).mockReturnValue(pending.promise);
    const view = renderPanel();
    expect(screen.getByText("Loading auto-deploy status…")).not.toBeNull();
    expect(screen.getByRole("status").textContent).toBe("Loading auto-deploy status…");
    const skeleton = view.container.querySelector('.auto-deploy-loading-skeleton[aria-hidden="true"]');
    expect(skeleton?.children).toHaveLength(3);
    expect(screen.getAllByRole("status")).toHaveLength(1);
  });

  it("moves focus to the panel heading after the main status retry succeeds", async () => {
    vi.mocked(api.getApplicationAutoDeploy)
      .mockRejectedValueOnce(new Error("status offline"))
      .mockResolvedValue(status as never);
    renderPanel();
    const retry = await screen.findByRole("button", { name: "Retry" });
    retry.focus();
    fireEvent.click(retry);
    await waitFor(() => expect(api.getApplicationAutoDeploy).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole("heading", { name: "Auto-deploy" })));
  });

  it("keeps focus on the main status retry when refetch fails", async () => {
    vi.mocked(api.getApplicationAutoDeploy).mockRejectedValue(new Error("status offline"));
    renderPanel();
    const retry = await screen.findByRole("button", { name: "Retry" });
    retry.focus();
    fireEvent.click(retry);
    await waitFor(() => expect(api.getApplicationAutoDeploy).toHaveBeenCalledTimes(2));
    expect(document.activeElement).toBe(screen.getByRole("button", { name: "Retry" }));
  });

  it("does not move focus when an old app status retry succeeds after a route switch", async () => {
    const oldRetry = deferred<ApplicationAutoDeployStatus>();
    let oldAppCalls = 0;
    vi.mocked(api.getApplicationAutoDeploy).mockImplementation((id) => {
      if (id === appId) {
        oldAppCalls += 1;
        return oldAppCalls === 1 ? Promise.reject(new Error("status offline")) : oldRetry.promise;
      }
      return Promise.resolve({ ...status, applicationId: id, source: { ...status.source, connectionId: "connection-2" } } as never);
    });
    vi.mocked(api.sourceConnections).mockResolvedValue({ items: [connection, { ...connection, id: "connection-2" }] } as never);
    const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
    const view = render(<QueryClientProvider client={client}><AutoDeployPanel appId={appId} composeRuntime githubConnections/></QueryClientProvider>);
    fireEvent.click(await screen.findByRole("button", { name: "Retry" }));
    await waitFor(() => expect(api.getApplicationAutoDeploy).toHaveBeenCalledTimes(2));
    view.rerender(<QueryClientProvider client={client}><AutoDeployPanel appId="app/two" composeRuntime githubConnections/></QueryClientProvider>);
    const newAppAction = await screen.findByRole("button", { name: "Enable" });
    newAppAction.focus();
    await act(async () => oldRetry.resolve({ ...status, applicationId: appId }));
    await act(async () => { await new Promise((resolve) => window.setTimeout(resolve, 1)); });
    expect(document.activeElement).toBe(newAppAction);
  });

  it("announces a relay failure and moves focus after its retry succeeds", async () => {
    vi.mocked(api.relayStatus)
      .mockRejectedValueOnce(new Error("relay offline"))
      .mockResolvedValue(relay as never);
    renderPanel();
    const retry = await screen.findByRole("button", { name: "Retry relay check" });
    await waitFor(() => expect(screen.getByRole("status").textContent).toContain("Relay status is unavailable."));
    expect(screen.getAllByRole("status")).toHaveLength(1);
    expect(screen.queryByRole("alert")).toBeNull();
    retry.focus();
    fireEvent.click(retry);
    await waitFor(() => expect(api.relayStatus).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole("heading", { name: "Auto-deploy" })));
  });

  it("keeps focus and deduplicates the relay announcement when retry fails", async () => {
    vi.mocked(api.relayStatus).mockRejectedValue(new Error("relay offline"));
    renderPanel();
    const retry = await screen.findByRole("button", { name: "Retry relay check" });
    const live = screen.getByRole("status");
    await waitFor(() => expect(live.textContent).toContain("Relay status is unavailable."));
    const announcement = live.textContent;
    retry.focus();
    fireEvent.click(retry);
    await waitFor(() => expect(api.relayStatus).toHaveBeenCalledTimes(2));
    expect(document.activeElement).toBe(screen.getByRole("button", { name: "Retry relay check" }));
    expect(live.textContent).toBe(announcement);
    expect(screen.getAllByRole("status")).toHaveLength(1);
  });

  it("announces a source failure and moves focus after its retry succeeds", async () => {
    vi.mocked(api.sourceConnections)
      .mockRejectedValueOnce(new Error("source offline"))
      .mockResolvedValue({ items: [connection] } as never);
    renderPanel();
    const retry = await screen.findByRole("button", { name: "Retry connection check" });
    await waitFor(() => expect(screen.getByRole("status").textContent).toContain("GitHub connection status is unavailable."));
    expect(screen.getAllByRole("status")).toHaveLength(1);
    expect(screen.queryByRole("alert")).toBeNull();
    retry.focus();
    fireEvent.click(retry);
    await waitFor(() => expect(api.sourceConnections).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole("heading", { name: "Auto-deploy" })));
  });

  it("keeps focus and deduplicates the source announcement when retry fails", async () => {
    vi.mocked(api.sourceConnections).mockRejectedValue(new Error("source offline"));
    renderPanel();
    const retry = await screen.findByRole("button", { name: "Retry connection check" });
    const live = screen.getByRole("status");
    await waitFor(() => expect(live.textContent).toContain("GitHub connection status is unavailable."));
    const announcement = live.textContent;
    retry.focus();
    fireEvent.click(retry);
    await waitFor(() => expect(api.sourceConnections).toHaveBeenCalledTimes(2));
    expect(document.activeElement).toBe(screen.getByRole("button", { name: "Retry connection check" }));
    expect(live.textContent).toBe(announcement);
    expect(screen.getAllByRole("status")).toHaveLength(1);
  });

  it("requires an explicit reload after a CAS conflict and focuses its alert", async () => {
    vi.mocked(api.updateApplicationAutoDeploy).mockRejectedValue(new APIError({ status: 409, code: "auto_deploy_conflict", detail: "changed" }));
    renderPanel();
    fireEvent.click(await screen.findByRole("button", { name: "Enable" }));
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("Reload status");
    expect(document.activeElement).toBe(alert);
    fireEvent.click(screen.getByRole("button", { name: "Reload status" }));
    await waitFor(() => expect(api.getApplicationAutoDeploy).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole("heading", { name: "Auto-deploy" })));
    expect(api.updateApplicationAutoDeploy).toHaveBeenCalledTimes(1);
  });

  it("keeps actions locked before and during manual reload, then unlocks only after valid success", async () => {
    const reload = deferred<ApplicationAutoDeployStatus>();
    vi.mocked(api.getApplicationAutoDeploy)
      .mockResolvedValueOnce(status as never)
      .mockReturnValueOnce(reload.promise);
    vi.mocked(api.updateApplicationAutoDeploy).mockRejectedValue(new APIError({ status: 409, code: "auto_deploy_state_conflict", detail: "changed" }));
    renderPanel();
    fireEvent.click(await screen.findByRole("button", { name: "Enable" }));
    await screen.findByText("Auto-deploy was not updated.");
    const enable = screen.getByRole("button", { name: "Enable" }) as HTMLButtonElement;
    expect(enable.disabled).toBe(true);
    expect(screen.getByText("Reload status successfully before changing auto-deploy.")).not.toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Reload status" }));
    expect((screen.getByRole("button", { name: "Reloading…" }) as HTMLButtonElement).disabled).toBe(true);
    expect(enable.disabled).toBe(true);
    await act(async () => reload.resolve(status as never));
    await waitFor(() => expect((screen.getByRole("button", { name: "Enable" }) as HTMLButtonElement).disabled).toBe(false));
    expect(screen.queryByText("Auto-deploy was not updated.")).toBeNull();
    expect(document.activeElement).toBe(screen.getByRole("heading", { name: "Auto-deploy" }));
  });

  it("keeps the manual reload lock after failed refetch and a later ordinary retry", async () => {
    vi.mocked(api.getApplicationAutoDeploy)
      .mockResolvedValueOnce(status as never)
      .mockRejectedValueOnce(new Error("status offline"))
      .mockResolvedValue(status as never);
    vi.mocked(api.updateApplicationAutoDeploy).mockRejectedValue(new APIError({ status: 409, code: "auto_deploy_state_conflict", detail: "changed" }));
    renderPanel();
    fireEvent.click(await screen.findByRole("button", { name: "Enable" }));
    fireEvent.click(await screen.findByRole("button", { name: "Reload status" }));
    const retry = await screen.findByRole("button", { name: "Retry" });
    expect(screen.getByRole("alert").textContent).toContain("status offline");
    fireEvent.click(retry);
    await screen.findByText("Auto-deploy was not updated.");
    expect((screen.getByRole("button", { name: "Enable" }) as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByRole("button", { name: "Reload status" })).not.toBeNull();
  });

  it("fails closed when query status belongs to another application", async () => {
    mockData({ applicationId: "app/other" });
    renderPanel();
    expect((await screen.findByRole("alert")).textContent).toContain("incomplete auto-deploy status");
    expect(screen.queryByRole("button", { name: "Enable" })).toBeNull();
  });

  it("requires manual reload when a mutation result belongs to another application", async () => {
    vi.mocked(api.updateApplicationAutoDeploy).mockResolvedValue({ ...status, applicationId: "app/other", enabled: true } as never);
    renderPanel();
    fireEvent.click(await screen.findByRole("button", { name: "Enable" }));
    await screen.findByText("Auto-deploy was not updated.");
    expect(screen.queryByText("Auto-deploy status updated.")).toBeNull();
    expect((screen.getByRole("button", { name: "Enable" }) as HTMLButtonElement).disabled).toBe(true);
  });

  it.each([
    ["capability_unavailable", "does not currently support auto-deploy"],
    ["auto_deploy_state_conflict", "source or state is not ready"],
    ["auto_deploy_prerequisite_missing", "repository relay authorization and controller access"],
  ])("maps controller mutation code %s to safe actionable copy", async (code, copy) => {
    vi.mocked(api.updateApplicationAutoDeploy).mockRejectedValue(new APIError({ status: 409, code, detail: "internal controller detail" }));
    renderPanel();
    fireEvent.click(await screen.findByRole("button", { name: "Enable" }));
    expect((await screen.findByRole("alert")).textContent).toContain(copy);
    expect(screen.queryByText("internal controller detail")).toBeNull();
  });

  it.each([
    ["approval_required", "Review Deployment history below", false],
    ["deployment_plan_review_required", "repository structure changed", true],
    ["migration_approval_required", "approve the pinned plan", false],
    ["insufficient_replacement_capacity", "temporary RAM or disk", false],
    ["deployment_failed", "previous auto-deployment failed", true],
    ["missing_configuration", "configuration is missing", true],
    ["source_access_lost", "GitHub access has been lost", true],
    ["invalid_source", "tracked GitHub source is invalid", true],
    ["provider_unavailable", "GitHub is currently unavailable", true],
    ["relay_unavailable", "relay is unavailable", true],
    ["unexpected_pause", "unexpected_pause", true],
  ])("maps paused state %s without bypassing approval", async (pauseCode, text, resume) => {
    mockData({ enabled: true, state: "paused", pauseCode, pausedSha: "abc123" });
    renderPanel();
    expect(await screen.findByText(new RegExp(text, "i"))).not.toBeNull();
    expect(Boolean(screen.queryByRole("button", { name: "Resume" }))).toBe(resume);
    if (resume) expect(screen.getByText(/Resume to ask Rig to revalidate and retry/i)).not.toBeNull();
  });

  it("routes plan-review pauses to the deployment setup panel", async () => {
    mockData({ enabled: true, state: "paused", pauseCode: "deployment_plan_review_required" });
    renderPanel();
    const action = await screen.findByRole("link", { name: "Review deployment setup" });
    expect(action.getAttribute("href")).toBe("#application-plan-title");
  });

  it.each(["approval_required", "migration_approval_required", "insufficient_replacement_capacity"])("routes active-job pause %s to deployment history", async (pauseCode) => {
    mockData({ enabled: true, state: "paused", pauseCode, activeJobId: "job-1" });
    renderPanel();
    const action = await screen.findByRole("link", { name: "Review waiting deployment" });
    expect(action.getAttribute("href")).toBe("#deployment-history-title");
    expect(screen.queryByRole("button", { name: "Resume" })).toBeNull();
  });

  it("resumes a known non-approval pause without requiring an active job or SHA", async () => {
    mockData({ enabled: true, state: "paused", pauseCode: "deployment_failed", revision: 7, activeJobId: undefined, activeSha: "", pausedSha: "", latestResolvedSha: "" });
    vi.mocked(api.resumeApplicationAutoDeploy).mockResolvedValue({ ...status, enabled: true, state: "paused", pauseCode: "deployment_failed", revision: 8, activeJobId: undefined, activeSha: "", pausedSha: "", latestResolvedSha: "" } as never);
    renderPanel();
    fireEvent.click(await screen.findByRole("button", { name: "Resume" }));
    await waitFor(() => expect(api.resumeApplicationAutoDeploy).toHaveBeenCalledWith(appId, { expectedRevision: 7 }));
    expect(screen.getByText("Auto-deploy resumed.")).not.toBeNull();
  });

  it("holds source-access-lost resume until the matching connection is connected and the source is subscribed", async () => {
    mockData({ enabled: true, state: "paused", pauseCode: "source_access_lost", activeJobId: "job-1", sourceScopeActive: true });
    vi.mocked(api.sourceConnections).mockResolvedValue({ items: [{ ...connection, status: "access_lost" }] } as never);
    renderPanel();
    const resume = await screen.findByRole("button", { name: "Resume" });
    expect((resume as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByText(/Reconnect GitHub in the existing source connection flow, return here, then choose Retry connection check/)).not.toBeNull();
    expect(screen.getByRole("button", { name: "Retry connection check" })).not.toBeNull();
  });

  it.each([
    [0, "A retry is scheduled."],
    [3, "Retry attempt 3 is scheduled."],
  ])("humanizes retry scheduling for attempt %s", async (retryAttempt, expected) => {
    mockData({ enabled: true, state: "retry_wait", retryAttempt, nextRetryAt: undefined });
    renderPanel();
    expect(await screen.findByText(expected)).not.toBeNull();
    expect(screen.queryByText(/Retry 0/)).toBeNull();
  });

  it("keeps source-access-lost resume disabled on a source-query error and enables it once restored", async () => {
    mockData({ enabled: true, state: "paused", pauseCode: "source_access_lost", activeJobId: "job-1", sourceScopeActive: true });
    vi.mocked(api.sourceConnections).mockRejectedValue(new Error("connection unavailable"));
    renderPanel();
    expect((await screen.findByRole("button", { name: "Resume" }) as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByText("The GitHub connection could not be checked before resuming.")).not.toBeNull();
    cleanup();
    mockData({ enabled: true, state: "paused", pauseCode: "source_access_lost", activeJobId: "job-1", sourceScopeActive: true });
    renderPanel();
    expect((await screen.findByRole("button", { name: "Resume" }) as HTMLButtonElement).disabled).toBe(false);
  });

  it("locks config changes during an active auto-deployment and shows safe diagnostics", async () => {
    mockData({ enabled: true, state: "deploying", activeJobId: "job-1", activeSha: "abcdef1234567890" });
    renderPanel();
    const turnOff = await screen.findByRole("button", { name: "Turn off" });
    expect((turnOff as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByText(/Settings cannot change/)).not.toBeNull();
    expect(screen.getByText("octo/service")).not.toBeNull();
    expect(screen.getAllByText("abcdef1234567890")).toHaveLength(2);
  });

  it.each(["approval_required", "source_access_lost"])("blocks turn off while paused with active job for %s", async (pauseCode) => {
    mockData({ enabled: true, state: "paused", pauseCode, activeJobId: "job-1", sourceScopeActive: true });
    renderPanel();
    const turnOff = await screen.findByRole("button", { name: "Turn off" });
    expect((turnOff as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByText("Settings cannot change while an active auto-deploy job exists.")).not.toBeNull();
  });

  it.each([
    ["disabled", "job-1", "Settings cannot change while an active auto-deploy job exists."],
    ["dispatching", undefined, "Settings cannot change while a deployment is being prepared or deployed."],
    ["deploying", undefined, "Settings cannot change while a deployment is being prepared or deployed."],
  ])("gives disabled Enable a nonempty busy reason for %s", async (stateName, activeJobId, reason) => {
    mockData({ enabled: false, state: stateName, activeJobId });
    renderPanel();
    const enable = await screen.findByRole("button", { name: "Enable" }) as HTMLButtonElement;
    expect(enable.disabled).toBe(true);
    const descriptionId = enable.getAttribute("aria-describedby");
    expect(descriptionId).toBe("auto-deploy-enable-reason");
    expect(document.getElementById(descriptionId ?? "")?.textContent).toBe(reason);
    fireEvent.click(enable);
    expect(api.updateApplicationAutoDeploy).not.toHaveBeenCalled();
  });

  it("keeps missing SHAs visible and accessible while preserving short and full forms for present SHAs", async () => {
    mockData({ activeSha: "abcdef1234567890", pausedSha: "" });
    renderPanel();
    await screen.findByText("Off");
    const activeSHA = screen.getByText("Active SHA").parentElement?.querySelector("dd");
    expect(activeSHA?.querySelector('[aria-hidden="true"]')?.textContent).toBe("abcdef123456");
    expect(activeSHA?.querySelector(".sr-only")?.textContent).toBe("abcdef1234567890");
    const pausedSHA = screen.getByText("Paused SHA").parentElement?.querySelector("dd");
    expect(pausedSHA?.textContent).toBe("Not recorded");
    expect(pausedSHA?.querySelector("[aria-hidden]")).toBeNull();
    expect(pausedSHA?.querySelector(".sr-only")).toBeNull();
  });

  it("renders an unknown nonempty controller state as a safe fallback and fails closed for mutations", async () => {
    mockData({ state: "future_controller_state" });
    renderPanel();
    expect(await screen.findByText("Status unavailable")).not.toBeNull();
    expect(screen.getByText("Auto-deploy returned an unsupported state.")).not.toBeNull();
    expect(screen.queryByText("The API returned an incomplete auto-deploy status.")).toBeNull();
    const enable = screen.getByRole("button", { name: "Enable" });
    expect((enable as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getAllByText(/Reload status before changing auto-deploy/)).toHaveLength(2);
    fireEvent.click(enable);
    expect(api.updateApplicationAutoDeploy).not.toHaveBeenCalled();
    expect(api.resumeApplicationAutoDeploy).not.toHaveBeenCalled();
  });

  it("refreshes both source and per-app status to recover source-access-lost resume without remounting", async () => {
    const initial = { ...status, enabled: true, state: "paused", pauseCode: "source_access_lost", sourceScopeActive: false };
    const restored = { ...initial, sourceScopeActive: true };
    mockData(initial);
    vi.mocked(api.getApplicationAutoDeploy)
      .mockResolvedValueOnce(initial as never)
      .mockResolvedValueOnce(restored as never);
    vi.mocked(api.sourceConnections)
      .mockResolvedValueOnce({ items: [{ ...connection, status: "access_lost" }] } as never)
      .mockResolvedValueOnce({ items: [connection] } as never);
    renderPanel();
    const resume = await screen.findByRole("button", { name: "Resume" });
    expect((resume as HTMLButtonElement).disabled).toBe(true);
    fireEvent.click(screen.getByRole("button", { name: "Retry connection check" }));
    await waitFor(() => expect(api.sourceConnections).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(api.getApplicationAutoDeploy).toHaveBeenCalledTimes(2));
    expect((await screen.findByRole("button", { name: "Resume" }) as HTMLButtonElement).disabled).toBe(false);
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole("heading", { name: "Auto-deploy" })));
  });

  it("keeps focus and announces source failure when source-access recovery retry fails", async () => {
    const paused = { ...status, enabled: true, state: "paused", pauseCode: "source_access_lost", sourceScopeActive: false };
    mockData(paused);
    vi.mocked(api.sourceConnections)
      .mockResolvedValueOnce({ items: [{ ...connection, status: "access_lost" }] } as never)
      .mockRejectedValue(new Error("source offline"));
    renderPanel();
    const retry = await screen.findByRole("button", { name: "Retry connection check" });
    retry.focus();
    fireEvent.click(retry);
    await waitFor(() => expect(api.sourceConnections).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(screen.getByRole("status").textContent).toContain("GitHub connection status is unavailable."));
    expect(document.activeElement).toBe(screen.getByRole("button", { name: "Retry connection check" }));
    expect(screen.getAllByRole("status")).toHaveLength(1);
  });

  it("restores focus when partial source-access recovery replaces the activated retry", async () => {
    const paused = { ...status, enabled: true, state: "paused", pauseCode: "source_access_lost", sourceScopeActive: true };
    mockData(paused);
    vi.mocked(api.getApplicationAutoDeploy)
      .mockResolvedValueOnce(paused as never)
      .mockRejectedValue(new Error("status offline"));
    vi.mocked(api.sourceConnections)
      .mockResolvedValueOnce({ items: [{ ...connection, status: "access_lost" }] } as never)
      .mockResolvedValue({ items: [connection] } as never);
    renderPanel();
    const retry = await screen.findByRole("button", { name: "Retry connection check" });
    retry.focus();
    fireEvent.click(retry);
    await waitFor(() => expect(api.sourceConnections).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(api.getApplicationAutoDeploy).toHaveBeenCalledTimes(2));
    await screen.findByRole("button", { name: "Retry" });
    expect(retry.isConnected).toBe(false);
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole("heading", { name: "Auto-deploy" })));
  });

  it("keeps focus on the same source-access retry when both refetches settle but access remains unresolved", async () => {
    const paused = { ...status, enabled: true, state: "paused", pauseCode: "source_access_lost", sourceScopeActive: false };
    mockData(paused);
    vi.mocked(api.sourceConnections).mockResolvedValue({ items: [{ ...connection, status: "access_lost" }] } as never);
    renderPanel();
    const retry = await screen.findByRole("button", { name: "Retry connection check" });
    retry.focus();
    fireEvent.click(retry);
    await waitFor(() => expect(api.sourceConnections).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(api.getApplicationAutoDeploy).toHaveBeenCalledTimes(2));
    await act(async () => { await new Promise((resolve) => window.setTimeout(resolve, 1)); });
    expect(retry.isConnected).toBe(true);
    expect(screen.getByRole("button", { name: "Retry connection check" })).toBe(retry);
    expect(document.activeElement).toBe(retry);
  });

  it("does not move focus when stale source-access recovery settles after a route switch", async () => {
    const paused = { ...status, enabled: true, state: "paused", pauseCode: "source_access_lost", sourceScopeActive: false };
    const statusRetry = deferred<ApplicationAutoDeployStatus>();
    const sourceRetry = deferred<Awaited<ReturnType<typeof api.sourceConnections>>>();
    vi.mocked(api.getApplicationAutoDeploy)
      .mockResolvedValueOnce(paused as never)
      .mockReturnValueOnce(statusRetry.promise)
      .mockResolvedValue({ ...status, applicationId: "app/two" } as never);
    vi.mocked(api.sourceConnections)
      .mockResolvedValueOnce({ items: [{ ...connection, status: "access_lost" }] } as never)
      .mockReturnValueOnce(sourceRetry.promise);
    const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
    const view = render(<QueryClientProvider client={client}><button type="button">Outside</button><AutoDeployPanel appId={appId} composeRuntime githubConnections/></QueryClientProvider>);
    fireEvent.click(await screen.findByRole("button", { name: "Retry connection check" }));
    await waitFor(() => expect(api.sourceConnections).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(api.getApplicationAutoDeploy).toHaveBeenCalledTimes(2));
    view.rerender(<QueryClientProvider client={client}><button type="button">Outside</button><AutoDeployPanel appId="app/two" composeRuntime githubConnections/></QueryClientProvider>);
    const outside = screen.getByRole("button", { name: "Outside" });
    outside.focus();
    await act(async () => {
      sourceRetry.resolve({ items: [connection] } as never);
      statusRetry.resolve(paused);
    });
    await act(async () => { await new Promise((resolve) => window.setTimeout(resolve, 1)); });
    expect(document.activeElement).toBe(outside);
  });

  it("serializes rapid clicks and keeps the polite announcement persistent", async () => {
    let resolve!: (value: never) => void;
    vi.mocked(api.updateApplicationAutoDeploy).mockReturnValue(new Promise((done) => { resolve = done; }));
    renderPanel();
    const enable = await screen.findByRole("button", { name: "Enable" });
    fireEvent.click(enable); fireEvent.click(enable);
    await waitFor(() => expect(api.updateApplicationAutoDeploy).toHaveBeenCalledTimes(1));
    expect(screen.getByRole("button", { name: "Enabling…" })).not.toBeNull();
    const enabling = screen.getByRole("button", { name: "Enabling…" });
    expect(enabling.getAttribute("aria-describedby")).toBe("auto-deploy-enable-reason");
    expect(document.getElementById("auto-deploy-enable-reason")?.textContent).toBe("Auto-deploy is being updated.");
    expect(screen.getByRole("status").getAttribute("aria-atomic")).toBe("true");
    await act(async () => resolve({ ...status, enabled: true, revision: 1 } as never));
  });

  it("announces a real state transition after an enable success announced idle", async () => {
    const idle = { ...status, enabled: true, state: "idle", revision: 1 };
    const dispatching = { ...idle, state: "dispatching" };
    vi.mocked(api.getApplicationAutoDeploy)
      .mockResolvedValueOnce(status as never)
      .mockResolvedValueOnce(dispatching as never);
    vi.mocked(api.updateApplicationAutoDeploy).mockResolvedValue(idle as never);
    renderPanel();
    fireEvent.click(await screen.findByRole("button", { name: "Enable" }));
    await waitFor(() => expect(api.updateApplicationAutoDeploy).toHaveBeenCalledTimes(1));
    expect(await screen.findByText("Auto-deploy state: Preparing deployment.")).not.toBeNull();
  });

  it("synchronously locks a paused app across turn-off and resume mutations", async () => {
    let resolve!: (value: never) => void;
    mockData({ enabled: true, state: "paused", pauseCode: "deployment_failed" });
    vi.mocked(api.updateApplicationAutoDeploy).mockReturnValue(new Promise((done) => { resolve = done; }));
    renderPanel();
    const turnOff = await screen.findByRole("button", { name: "Turn off" });
    const resume = screen.getByRole("button", { name: "Resume" });
    fireEvent.click(turnOff);
    fireEvent.click(resume);
    await waitFor(() => expect(api.updateApplicationAutoDeploy).toHaveBeenCalledTimes(1));
    expect(api.resumeApplicationAutoDeploy).not.toHaveBeenCalled();
    await act(async () => resolve({ ...status, enabled: false, state: "disabled", revision: 1 } as never));
  });

  it("does not poll a stable terminal status", async () => {
    vi.useFakeTimers();
    renderPanel();
    await act(async () => {});
    await act(async () => { await vi.advanceTimersByTimeAsync(6_000); });
    expect(api.getApplicationAutoDeploy).toHaveBeenCalledTimes(1);
  });

  it.each(["dispatching", "deploying", "retry_wait", "paused"])("polls active auto-deploy state %s", async (state) => {
    vi.useFakeTimers();
    mockData({ enabled: true, state, activeJobId: state === "paused" ? "job-1" : undefined });
    renderPanel();
    await act(async () => { await vi.advanceTimersByTimeAsync(2_000); });
    await act(async () => { await vi.advanceTimersByTimeAsync(100); });
    expect(api.getApplicationAutoDeploy).toHaveBeenCalledTimes(2);
  });

  it("stops transient-state polling after a cached-data refetch failure", async () => {
    vi.useFakeTimers();
    vi.mocked(api.getApplicationAutoDeploy)
      .mockResolvedValueOnce({ ...status, enabled: true, state: "dispatching" } as never)
      .mockRejectedValue(new Error("status polling failed"));
    renderPanel();
    await act(async () => { await vi.advanceTimersByTimeAsync(2_000); });
    await act(async () => { await vi.advanceTimersByTimeAsync(1); });
    expect(api.getApplicationAutoDeploy).toHaveBeenCalledTimes(2);
    expect(screen.getByRole("alert").textContent).toContain("status polling failed");
    expect(screen.queryByRole("button", { name: "Turn off" })).toBeNull();
    await act(async () => { await vi.advanceTimersByTimeAsync(6_000); });
    expect(api.getApplicationAutoDeploy).toHaveBeenCalledTimes(2);
  });

  it("stops relay-initializing polling after a cached-data refetch failure", async () => {
    vi.useFakeTimers();
    vi.mocked(api.relayStatus)
      .mockResolvedValueOnce({ ...relay, availability: "initializing" } as never)
      .mockRejectedValue(new Error("relay polling failed"));
    renderPanel();
    await act(async () => { await vi.advanceTimersByTimeAsync(2_000); });
    await act(async () => { await vi.advanceTimersByTimeAsync(1); });
    expect(api.relayStatus).toHaveBeenCalledTimes(2);
    expect(screen.getByRole("status").textContent).toContain("Relay status is unavailable.");
    expect((screen.getByRole("button", { name: "Enable" }) as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByText("Relay availability could not be confirmed.")).not.toBeNull();
    await act(async () => { await vi.advanceTimersByTimeAsync(6_000); });
    expect(api.relayStatus).toHaveBeenCalledTimes(2);
  });

  it("announces meaningful polled state and pause-code changes without timestamp churn", async () => {
    vi.useFakeTimers();
    vi.mocked(api.getApplicationAutoDeploy)
      .mockResolvedValueOnce({ ...status, enabled: true, state: "dispatching" } as never)
      .mockResolvedValueOnce({ ...status, enabled: true, state: "paused", pauseCode: "deployment_failed", activeJobId: "job-1", updatedAt: "2026-08-27T00:00:00Z" } as never);
    renderPanel();
    await act(async () => { await vi.advanceTimersByTimeAsync(1); });
    expect(screen.getByRole("status").textContent).toBe("Auto-deploy state: Preparing deployment.");
    await act(async () => { await vi.advanceTimersByTimeAsync(2_000); });
    await act(async () => { await vi.advanceTimersByTimeAsync(1); });
    expect(api.getApplicationAutoDeploy).toHaveBeenCalledTimes(2);
    expect(screen.getByRole("status").textContent).toBe("Auto-deploy state: Paused (deployment_failed).");
  });

  it("announces retry-attempt changes without announcing retry timestamp-only changes", async () => {
    vi.useFakeTimers();
    vi.mocked(api.getApplicationAutoDeploy)
      .mockResolvedValueOnce({ ...status, enabled: true, state: "retry_wait", retryAttempt: 1, nextRetryAt: "2026-08-27T00:00:00Z" } as never)
      .mockResolvedValueOnce({ ...status, enabled: true, state: "retry_wait", retryAttempt: 2, nextRetryAt: "2026-08-27T00:01:00Z" } as never)
      .mockResolvedValueOnce({ ...status, enabled: true, state: "retry_wait", retryAttempt: 2, nextRetryAt: "2026-08-27T00:02:00Z" } as never);
    renderPanel();
    await act(async () => { await vi.advanceTimersByTimeAsync(1); });
    expect(screen.getByRole("status").textContent).toBe("Auto-deploy state: Retry scheduled, attempt 1.");
    await act(async () => { await vi.advanceTimersByTimeAsync(2_000); });
    expect(screen.getByRole("status").textContent).toBe("Auto-deploy state: Retry scheduled, attempt 2.");
    const attemptAnnouncement = screen.getByRole("status").textContent;
    await act(async () => { await vi.advanceTimersByTimeAsync(2_000); });
    expect(api.getApplicationAutoDeploy).toHaveBeenCalledTimes(3);
    expect(screen.getByRole("status").textContent).toBe(attemptAnnouncement);
    expect(screen.getByRole("status").textContent).not.toContain("2026");
  });

  it.each([["dispatching", false], ["paused", true]])("stops polling after %s converges to idle", async (state, activeJob) => {
    vi.useFakeTimers();
    vi.mocked(api.getApplicationAutoDeploy)
      .mockResolvedValueOnce({ ...status, enabled: true, state, activeJobId: activeJob ? "job-1" : undefined } as never)
      .mockResolvedValueOnce({ ...status, enabled: true, state: "idle" } as never);
    renderPanel();
    await act(async () => { await vi.advanceTimersByTimeAsync(2_000); });
    await act(async () => { await vi.advanceTimersByTimeAsync(6_000); });
    expect(api.getApplicationAutoDeploy).toHaveBeenCalledTimes(2);
  });

  it("fences deferred old-app mutation success while allowing a new-app mutation", async () => {
    const old = deferred<ApplicationAutoDeployStatus>();
    const fresh = deferred<ApplicationAutoDeployStatus>();
    vi.mocked(api.getApplicationAutoDeploy).mockImplementation((id) => Promise.resolve({ ...status, applicationId: id, source: { ...status.source, connectionId: id === appId ? "connection-1" : "connection-2" } } as never));
    vi.mocked(api.sourceConnections).mockResolvedValue({ items: [connection, { ...connection, id: "connection-2" }] } as never);
    vi.mocked(api.updateApplicationAutoDeploy).mockImplementation((id) => id === appId ? old.promise : fresh.promise);
    const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
    const view = render(<QueryClientProvider client={client}><AutoDeployPanel appId={appId} composeRuntime githubConnections/></QueryClientProvider>);
    fireEvent.click(await screen.findByRole("button", { name: "Enable" }));
    view.rerender(<QueryClientProvider client={client}><AutoDeployPanel appId="app/two" composeRuntime githubConnections/></QueryClientProvider>);
    fireEvent.click(await screen.findByRole("button", { name: "Enable" }));
    await waitFor(() => expect(api.updateApplicationAutoDeploy).toHaveBeenCalledTimes(2));
    await act(async () => old.resolve({ ...status, applicationId: appId, enabled: true, revision: 1 }));
    expect(screen.queryByText("Auto-deploy status updated.")).toBeNull();
    expect(client.getQueryData<ApplicationAutoDeployStatus>(["application-auto-deploy", "app/two"])).toMatchObject({ applicationId: "app/two", revision: 0 });
    await act(async () => fresh.resolve({ ...status, applicationId: "app/two", enabled: true, revision: 1 }));
    expect(await screen.findByText("Auto-deploy status updated.")).not.toBeNull();
  });

  it("renders and releases concurrent per-app mutation locks independently across route switches", async () => {
    const first = deferred<ApplicationAutoDeployStatus>();
    const second = deferred<ApplicationAutoDeployStatus>();
    vi.mocked(api.getApplicationAutoDeploy).mockImplementation((id) => Promise.resolve({
      ...status,
      applicationId: id,
      source: { ...status.source, connectionId: id === appId ? "connection-1" : "connection-2" },
    } as never));
    vi.mocked(api.sourceConnections).mockResolvedValue({ items: [connection, { ...connection, id: "connection-2" }] } as never);
    vi.mocked(api.updateApplicationAutoDeploy).mockImplementation((id) => id === appId ? first.promise : second.promise);
    const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
    const view = render(<QueryClientProvider client={client}><AutoDeployPanel appId={appId} composeRuntime githubConnections/></QueryClientProvider>);
    fireEvent.click(await screen.findByRole("button", { name: "Enable" }));
    view.rerender(<QueryClientProvider client={client}><AutoDeployPanel appId="app/two" composeRuntime githubConnections/></QueryClientProvider>);
    fireEvent.click(await screen.findByRole("button", { name: "Enable" }));
    await waitFor(() => expect(api.updateApplicationAutoDeploy).toHaveBeenCalledTimes(2));
    view.rerender(<QueryClientProvider client={client}><AutoDeployPanel appId={appId} composeRuntime githubConnections/></QueryClientProvider>);
    expect((await screen.findByRole("button", { name: "Enable" }) as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByText("Auto-deploy is being updated.")).not.toBeNull();
    await act(async () => first.resolve({ ...status, applicationId: appId, enabled: true, revision: 1 }));
    await waitFor(() => expect((screen.getByRole("button", { name: "Enable" }) as HTMLButtonElement).disabled).toBe(false));
    view.rerender(<QueryClientProvider client={client}><AutoDeployPanel appId="app/two" composeRuntime githubConnections/></QueryClientProvider>);
    expect((await screen.findByRole("button", { name: "Enable" }) as HTMLButtonElement).disabled).toBe(true);
    await act(async () => second.resolve({ ...status, applicationId: "app/two", enabled: true, revision: 1 }));
    await waitFor(() => expect((screen.getByRole("button", { name: "Enable" }) as HTMLButtonElement).disabled).toBe(false));
  });

  it("fences deferred old-app mutation errors from the new app", async () => {
    const old = deferred<ApplicationAutoDeployStatus>();
    vi.mocked(api.getApplicationAutoDeploy).mockImplementation((id) => Promise.resolve({ ...status, applicationId: id, source: { ...status.source, connectionId: id === appId ? "connection-1" : "connection-2" } } as never));
    vi.mocked(api.sourceConnections).mockResolvedValue({ items: [connection, { ...connection, id: "connection-2" }] } as never);
    vi.mocked(api.updateApplicationAutoDeploy).mockImplementation((id) => id === appId ? old.promise : Promise.resolve({ ...status, applicationId: id, enabled: true } as never));
    const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
    const view = render(<QueryClientProvider client={client}><AutoDeployPanel appId={appId} composeRuntime githubConnections/></QueryClientProvider>);
    fireEvent.click(await screen.findByRole("button", { name: "Enable" }));
    view.rerender(<QueryClientProvider client={client}><AutoDeployPanel appId="app/two" composeRuntime githubConnections/></QueryClientProvider>);
    await screen.findByRole("button", { name: "Enable" });
    await act(async () => old.reject(new APIError({ status: 409, code: "auto_deploy_state_conflict", detail: "old app" })));
    expect(screen.queryByRole("alert")).toBeNull();
    expect(screen.queryByText("old app")).toBeNull();
  });
});
