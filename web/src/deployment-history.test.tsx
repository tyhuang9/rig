import { readFileSync } from "node:fs";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { APIError, api } from "./api";
import { DeploymentHistoryPanel } from "./deployment-history";

const appId = "app-1";
const fingerprint = "a".repeat(64);
const release = {
  id: "release-1",
  appId,
  sourceProvider: "github",
  repositoryOwner: "octo",
  repositoryName: "service",
  trackedRef: "refs/heads/main",
  composePath: "deploy/compose.yaml",
  configurationRevisionNumber: 4,
  createdAt: "2026-08-01T10:00:00Z",
  resolvedSha: "abcdef1234567890fedcba9876543210",
  workspaceState: "ready",
  workspacePath: "C:\\workspace\\internal-only",
  sourceProviderBody: "provider-secret-body",
  secretToken: "super-secret-token",
};
const deployment = {
  id: "deployment-1",
  appId,
  jobId: "job-1",
  releaseId: release.id,
  status: "needs_attention",
  configurationMode: "current",
  actualConfigurationRevisionNumber: 4,
  startedAt: "2026-08-01T10:00:00Z",
  findings: [
    {
      id: "finding-1",
      deploymentId: "deployment-1",
      policyVersion: "v1",
      capability: "privileged",
      scope: "service:web",
      fingerprint,
      disposition: "approval_required",
      createdAt: "2026-08-01T10:00:00Z",
    },
  ],
};
const waitingJob = {
  id: "job-1",
  type: "deploy",
  resourceType: "application",
  resourceId: appId,
  status: "waiting_user",
  pauseDisposition: "approval_required",
  checkpoint: "approval_required",
  phase: "approval_required",
  attempt: 1,
  progress: 50,
  createdAt: "2026-08-01T10:00:00Z",
  updatedAt: "2026-08-01T10:00:00Z",
};

function renderPanel(
  props: Partial<React.ComponentProps<typeof DeploymentHistoryPanel>> = {},
) {
  let root = document.getElementById("root");
  if (!root) {
    root = document.createElement("div");
    root.id = "root";
    document.body.append(root);
  }
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  const view = render(
    <QueryClientProvider client={client}>
      <button type="button">Background action</button>
      <DeploymentHistoryPanel
        appId={appId}
        composeRuntime
        fakeRuntime={false}
        {...props}
      />
    </QueryClientProvider>,
    { container: root },
  );
  return { client, ...view };
}

function mockData({
  deployments = [deployment],
  releases = [release],
  approvals = [],
  jobs = [waitingJob],
}: {
  deployments?: (typeof deployment)[];
  releases?: (typeof release)[];
  approvals?: unknown[];
  jobs?: (typeof waitingJob)[];
} = {}) {
  vi.spyOn(api, "deployments").mockResolvedValue({
    items: deployments,
  } as never);
  vi.spyOn(api, "releases").mockResolvedValue({ items: releases } as never);
  vi.spyOn(api, "runtimeApprovals").mockResolvedValue({
    items: approvals,
  } as never);
  vi.spyOn(api, "jobs").mockResolvedValue({ items: jobs } as never);
  vi.spyOn(api, "deployApplication").mockResolvedValue({
    created: true,
    job: { id: "queued-1" },
  } as never);
  vi.spyOn(api, "deployRelease").mockResolvedValue({
    created: true,
    job: { id: "queued-2" },
  } as never);
  vi.spyOn(api, "grantRuntimeApproval").mockResolvedValue({
    created: true,
    approval: { id: "approval-1" },
  } as never);
  vi.spyOn(api, "revokeRuntimeApproval").mockResolvedValue({
    approval: { id: "approval-1" },
  } as never);
  vi.spyOn(api, "resumeJob").mockResolvedValue({
    job: { id: waitingJob.id },
  } as never);
}

describe("DeploymentHistoryPanel", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
    mockData();
  });
  afterEach(() => {
    vi.useRealTimers();
    cleanup();
  });

  it("renders independent empty states and gates latest actions by runtime capability", async () => {
    mockData({ deployments: [], releases: [], approvals: [], jobs: [] });
    renderPanel({ composeRuntime: false, fakeRuntime: false });
    expect(
      await screen.findByText("No deployment has been recorded yet."),
    ).not.toBeNull();
    expect(screen.getByText("No releases are available yet.")).not.toBeNull();
    expect(
      screen
        .getByRole("button", { name: "Deploy latest" })
        .hasAttribute("disabled"),
    ).toBe(true);
    expect(
      screen.getByText("Deployment actions require a configured runtime."),
    ).not.toBeNull();
  });

  it("keeps waiting-job loading visible instead of showing completed approvals empty state", async () => {
    let resolveJobs!: (value: never) => void;
    vi.mocked(api.jobs).mockReturnValue(
      new Promise((done) => {
        resolveJobs = done;
      }),
    );
    renderPanel();
    expect(
      await screen.findByText("Checking waiting deployment jobs..."),
    ).not.toBeNull();
    expect(
      screen.queryByText("No runtime approvals or waiting policy findings."),
    ).toBeNull();
    await act(async () => resolveJobs({ items: [] } as never));
    expect(
      await screen.findByText("No runtime approvals or waiting policy findings."),
    ).not.toBeNull();
  });

  it("keeps waiting-job failure and retry visible instead of showing completed approvals empty state", async () => {
    vi.mocked(api.jobs)
      .mockRejectedValueOnce(new Error("Jobs are unavailable"))
      .mockResolvedValue({ items: [] } as never);
    renderPanel();
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("Jobs are unavailable");
    expect(
      screen.queryByText("No runtime approvals or waiting policy findings."),
    ).toBeNull();
    fireEvent.click(within(alert).getByRole("button", { name: "Retry" }));
    await waitFor(() => expect(api.jobs).toHaveBeenCalledTimes(2));
    expect(
      await screen.findByText("No runtime approvals or waiting policy findings."),
    ).not.toBeNull();
  });

  it("humanizes deployment states and applies their stable tones", async () => {
    mockData({
      deployments: [
        { ...deployment, id: "deployment-preparing", status: "preparing" },
        { ...deployment, id: "deployment-applying", status: "applying" },
        {
          ...deployment,
          id: "deployment-health",
          status: "waiting_health",
        },
        {
          ...deployment,
          id: "deployment-attention",
          status: "needs_attention",
        },
      ],
    });
    renderPanel();
    for (const label of [
      "Preparing",
      "Applying",
      "Waiting for health checks",
    ]) {
      const [status] = await screen.findAllByText(label);
      expect(status.closest(".status")?.classList.contains("status-active")).toBe(
        true,
      );
    }
    const [waiting] = await screen.findAllByText("Waiting for approval");
    expect(waiting.closest(".status")?.classList.contains("status-waiting")).toBe(
      true,
    );
    const [attention] = await screen.findAllByText("Needs attention");
    expect(
      attention.closest(".status")?.classList.contains("status-attention"),
    ).toBe(true);
  });

  it("uses a white focus outline for rail controls on dark rail surfaces", () => {
    expect(
      readFileSync("src/styles.css", "utf8"),
    ).toContain(
      ".rail a:focus-visible, .rail-link:focus-visible { outline-color: #fff; }",
    );
  });

  it("retries only a failed release section", async () => {
    vi.mocked(api.releases)
      .mockRejectedValueOnce(new Error("offline"))
      .mockResolvedValue({ items: [release] } as never);
    renderPanel();
    const retry = await screen.findAllByRole("button", { name: "Retry" });
    fireEvent.click(retry[0]);
    await waitFor(() => expect(api.releases).toHaveBeenCalledTimes(2));
    expect((await screen.findAllByText("abcdef123456")).length).toBeGreaterThan(
      0,
    );
  });

  it("shows copyable deployment diagnostics and safe release provenance", async () => {
    renderPanel();
    const rows = (await screen.findAllByText("deployment-1")).map((value) =>
      value.closest(".deployment-row"),
    );
    expect(rows).toHaveLength(2);
    for (const row of rows) {
      expect(row?.textContent).toContain("job-1");
      expect(row?.textContent).toContain("release-1");
      expect(row?.textContent).toContain(release.resolvedSha);
      expect(row?.textContent).toContain("github");
      expect(row?.textContent).toContain("octo/service");
      expect(row?.textContent).toContain("refs/heads/main");
      expect(row?.textContent).toContain("deploy/compose.yaml");
      expect(row?.textContent).not.toContain(release.workspacePath);
      expect(row?.textContent).not.toContain(release.sourceProviderBody);
      expect(row?.textContent).not.toContain(release.secretToken);
    }
  });

  it("queues latest deployment without claiming success and serializes the button", async () => {
    let resolve!: (value: never) => void;
    vi.mocked(api.deployApplication).mockReturnValue(
      new Promise((done) => {
        resolve = done;
      }),
    );
    renderPanel();
    const deploy = await screen.findByRole("button", { name: "Deploy latest" });
    fireEvent.click(deploy);
    fireEvent.click(deploy);
    await waitFor(() => expect(api.deployApplication).toHaveBeenCalledTimes(1));
    expect(deploy.hasAttribute("disabled")).toBe(true);
    await act(async () =>
      resolve({ created: true, job: { id: "queued-1" } } as never),
    );
    expect(
      await screen.findByText("Deployment job queued-1 queued."),
    ).not.toBeNull();
    expect(screen.queryByText(/deployment succeeded/i)).toBeNull();
  });

  it("disables every available deployment and approval action while deployment is pending", async () => {
    let resolve!: (value: never) => void;
    mockData({
      approvals: [
        {
          id: "approval-1",
          appId,
          capability: "network",
          fingerprint: "b".repeat(64),
          grantedAt: "2026-08-01T10:00:00Z",
          grantedBy: "user",
          policyVersion: "v1",
          scope: "service:web",
        },
      ],
    });
    vi.mocked(api.deployApplication).mockReturnValue(
      new Promise((done) => {
        resolve = done;
      }),
    );
    renderPanel();
    const latest = await screen.findByRole("button", { name: "Deploy latest" });
    const prior = (await screen.findAllByRole("button", {
      name: "Deploy release",
    }))[0] as HTMLButtonElement;
    const grant = screen.getByRole("button", { name: "Grant approval" });
    const revoke = screen.getByRole("button", { name: "Revoke approval" });
    fireEvent.click(latest);
    await waitFor(() => expect(api.deployApplication).toHaveBeenCalledTimes(1));
    expect(
      (screen.getByRole("button", { name: "Queuing..." }) as HTMLButtonElement)
        .disabled,
    ).toBe(true);
    expect(prior.disabled).toBe(true);
    expect((grant as HTMLButtonElement).disabled).toBe(true);
    expect((revoke as HTMLButtonElement).disabled).toBe(true);
    await act(async () =>
      resolve({ created: true, job: { id: "queued-1" } } as never),
    );
  });

  it("disables resume and the other available controls while resuming", async () => {
    let resolve!: (value: never) => void;
    mockData({
      approvals: [
        {
          id: "approval-required",
          appId,
          capability: "privileged",
          fingerprint,
          grantedAt: "2026-08-01T10:00:00Z",
          grantedBy: "user",
          policyVersion: "v1",
          scope: "service:web",
        },
        {
          id: "approval-extra",
          appId,
          capability: "network",
          fingerprint: "b".repeat(64),
          grantedAt: "2026-08-01T10:00:00Z",
          grantedBy: "user",
          policyVersion: "v1",
          scope: "service:web",
        },
      ],
    });
    vi.mocked(api.resumeJob).mockReturnValue(
      new Promise((done) => {
        resolve = done;
      }),
    );
    renderPanel();
    const latest = await screen.findByRole("button", { name: "Deploy latest" });
    const prior = (await screen.findAllByRole("button", {
      name: "Deploy release",
    }))[0] as HTMLButtonElement;
    const revoke = screen.getByRole("button", { name: "Revoke approval" });
    const resume = screen.getByRole("button", { name: "Resume waiting job" });
    fireEvent.click(resume);
    await waitFor(() => expect(api.resumeJob).toHaveBeenCalledWith(waitingJob.id));
    expect(
      (screen.getByRole("button", { name: "Resuming..." }) as HTMLButtonElement)
        .disabled,
    ).toBe(true);
    expect((latest as HTMLButtonElement).disabled).toBe(true);
    expect(prior.disabled).toBe(true);
    expect((revoke as HTMLButtonElement).disabled).toBe(true);
    await act(async () => resolve({ job: { id: waitingJob.id } } as never));
  });

  it("uses the selected current or original configuration for ready prior releases", async () => {
    renderPanel();
    fireEvent.click(
      (await screen.findAllByRole("button", { name: "Deploy release" }))[0],
    );
    const dialog = await screen.findByRole("dialog", {
      name: "Deploy prior release",
    });
    expect(document.activeElement).toBe(
      screen.getByLabelText("Current configuration"),
    );
    fireEvent.click(screen.getByLabelText("Original release configuration"));
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Deploy release" }),
    );
    await waitFor(() =>
      expect(api.deployRelease).toHaveBeenCalledWith(appId, release.id, {
        configurationMode: "original",
      }),
    );
    expect(dialog).not.toBeNull();
  });

  it("keeps non-ready releases natively disabled and traps, escapes, and restores dialog focus", async () => {
    mockData({ releases: [{ ...release, workspaceState: "missing" }] });
    renderPanel();
    expect(
      (
        (await screen.findByRole("button", {
          name: "Deploy release",
        })) as HTMLButtonElement
      ).disabled,
    ).toBe(true);
    cleanup();
    vi.restoreAllMocks();
    mockData();
    renderPanel();
    const launcher = await screen.findByRole("button", {
      name: "Deploy release",
    });
    const backgroundAction = screen.getByRole("button", {
      name: "Background action",
    });
    launcher.focus();
    fireEvent.click(launcher);
    const root = document.getElementById("root");
    expect(root?.contains(backgroundAction)).toBe(true);
    expect(root?.contains(launcher)).toBe(true);
    expect(root?.inert).toBe(true);
    expect(root?.getAttribute("aria-hidden")).toBe("true");
    const dialog = await screen.findByRole("dialog", {
      name: "Deploy prior release",
    });
    expect(dialog.getAttribute("aria-modal")).toBe("true");
    const title = document.getElementById(
      dialog.getAttribute("aria-labelledby") ?? "",
    );
    expect(title?.tagName).toBe("H2");
    expect(title?.textContent).toBe("Deploy prior release");
    const first = screen.getByLabelText("Current configuration");
    const last = within(dialog).getByRole("button", {
      name: "Deploy release",
    });
    last.focus();
    fireEvent.keyDown(document, { key: "Tab" });
    expect(document.activeElement).toBe(
      first,
    );
    first.focus();
    fireEvent.keyDown(document, { key: "Tab", shiftKey: true });
    expect(document.activeElement).toBe(last);
    fireEvent.keyDown(document, { key: "Escape" });
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(document.activeElement).toBe(launcher);
    expect(root?.inert).toBe(false);
    expect(root?.hasAttribute("aria-hidden")).toBe(false);
  });

  it("provides the full fingerprint as screen-reader content", async () => {
    renderPanel();
    const fullValue = await screen.findByText(`Fingerprint ${fingerprint}`);
    expect(fullValue.classList.contains("sr-only")).toBe(true);
    expect(
      screen
        .getByText(`Fingerprint ${fingerprint.slice(0, 12)}...`)
        .getAttribute("aria-hidden"),
    ).toBe("true");
  });

  it("serializes rapid prior-release requests with a single mutation", async () => {
    let resolve!: (value: never) => void;
    vi.mocked(api.deployRelease).mockReturnValue(
      new Promise((done) => {
        resolve = done;
      }),
    );
    renderPanel();
    fireEvent.click(
      (await screen.findAllByRole("button", { name: "Deploy release" }))[0],
    );
    const dialog = await screen.findByRole("dialog", {
      name: "Deploy prior release",
    });
    const submit = within(dialog).getByRole("button", {
      name: "Deploy release",
    });
    fireEvent.click(submit);
    fireEvent.click(submit);
    await waitFor(() => expect(api.deployRelease).toHaveBeenCalledTimes(1));
    await act(async () =>
      resolve({ created: true, job: { id: "queued-prior" } } as never),
    );
  });

  it("keeps a failed prior-release error inside its open modal", async () => {
    vi.mocked(api.deployRelease).mockRejectedValue(
      new Error("Prior release is unavailable"),
    );
    renderPanel();
    fireEvent.click(
      (await screen.findAllByRole("button", { name: "Deploy release" }))[0],
    );
    const dialog = await screen.findByRole("dialog", {
      name: "Deploy prior release",
    });
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Deploy release" }),
    );
    const alert = await within(dialog).findByRole("alert");
    expect(alert.textContent).toContain("Prior release is unavailable");
    expect(dialog.contains(document.activeElement)).toBe(true);
  });

  it("keeps a revoke modal open during a pending mutation and focuses its error", async () => {
    let reject!: (reason: Error) => void;
    mockData({
      approvals: [
        {
          id: "approval-1",
          appId,
          capability: "network",
          fingerprint: "b".repeat(64),
          grantedAt: "2026-08-01T10:00:00Z",
          grantedBy: "user",
          policyVersion: "v1",
          scope: "service:web",
        },
      ],
    });
    vi.mocked(api.revokeRuntimeApproval).mockReturnValue(
      new Promise((_, fail) => {
        reject = fail;
      }),
    );
    renderPanel();
    fireEvent.click(
      await screen.findByRole("button", { name: "Revoke approval" }),
    );
    const dialog = await screen.findByRole("dialog", {
      name: "Revoke runtime approval",
    });
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Revoke approval" }),
    );
    await waitFor(() =>
      expect(api.revokeRuntimeApproval).toHaveBeenCalledTimes(1),
    );
    await waitFor(() =>
      expect(
        within(dialog)
          .getByRole("button", { name: "Revoking..." })
          .hasAttribute("disabled"),
      ).toBe(true),
    );
    const pendingTab = new KeyboardEvent("keydown", {
      key: "Tab",
      bubbles: true,
      cancelable: true,
    });
    document.dispatchEvent(pendingTab);
    expect(pendingTab.defaultPrevented).toBe(true);
    expect(document.activeElement).toBe(dialog);
    fireEvent.keyDown(document, { key: "Escape" });
    expect(
      screen.getByRole("dialog", { name: "Revoke runtime approval" }),
    ).toBe(dialog);
    await act(async () => reject(new Error("Approval is in use")));
    const alert = await within(dialog).findByRole("alert");
    expect(document.activeElement).toBe(alert);
    expect(dialog.contains(document.activeElement)).toBe(true);
    const alertShiftTab = new KeyboardEvent("keydown", {
      key: "Tab",
      shiftKey: true,
      bubbles: true,
      cancelable: true,
    });
    document.dispatchEvent(alertShiftTab);
    expect(alertShiftTab.defaultPrevented).toBe(true);
    expect(document.activeElement).toBe(
      within(dialog).getByRole("button", { name: "Revoke approval" }),
    );
  });

  it("polls active work through waiting approval and stops after terminal convergence", async () => {
    vi.useFakeTimers();
    const queuedDeployment = { ...deployment, status: "preparing" };
    const completedDeployment = {
      ...deployment,
      status: "succeeded",
      findings: [],
    };
    const queuedJob = {
      ...waitingJob,
      status: "queued",
      pauseDisposition: undefined,
    };
    const waitingApprovalJob = { ...waitingJob };
    const completedJob = {
      ...waitingJob,
      status: "succeeded",
      pauseDisposition: undefined,
    };
    const deploymentsMock = vi.spyOn(api, "deployments");
    const jobsMock = vi.spyOn(api, "jobs");
    deploymentsMock
      .mockResolvedValueOnce({ items: [queuedDeployment] } as never)
      .mockResolvedValueOnce({ items: [deployment] } as never)
      .mockResolvedValue({ items: [completedDeployment] } as never);
    jobsMock
      .mockResolvedValueOnce({ items: [queuedJob] } as never)
      .mockResolvedValueOnce({ items: [waitingApprovalJob] } as never)
      .mockResolvedValue({ items: [completedJob] } as never);
    renderPanel();
    await act(async () => {
      await Promise.resolve();
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(2_000);
    });
    expect(jobsMock.mock.calls.length).toBeGreaterThanOrEqual(2);
    expect(deploymentsMock.mock.calls.length).toBeGreaterThanOrEqual(2);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(2_000);
    });
    const settledCalls = jobsMock.mock.calls.length;
    await act(async () => {
      await vi.advanceTimersByTimeAsync(2_000);
    });
    expect(jobsMock).toHaveBeenCalledTimes(settledCalls);
  });

  it("does not poll for an unrelated active application job", async () => {
    vi.useFakeTimers();
    const unrelatedActiveJob = {
      ...waitingJob,
      id: "job-unrelated",
      type: "inspect",
      status: "queued",
    };
    const completedDeployment = {
      ...deployment,
      status: "succeeded",
      findings: [],
    };
    mockData({ deployments: [completedDeployment], jobs: [unrelatedActiveJob] });
    const deploymentsMock = vi.mocked(api.deployments);
    const jobsMock = vi.mocked(api.jobs);
    renderPanel();
    await act(async () => {
      await Promise.resolve();
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(6_000);
    });
    expect(deploymentsMock).toHaveBeenCalledTimes(1);
    expect(jobsMock).toHaveBeenCalledTimes(1);
  });

  it("requires the exact matched waiting deployment and every fingerprint before separate grant then resume", async () => {
    vi.mocked(api.runtimeApprovals)
      .mockResolvedValueOnce({ items: [] } as never)
      .mockResolvedValue({
        items: [
          {
            id: "approval-1",
            appId,
            capability: "privileged",
            fingerprint,
            grantedAt: "2026-08-01T10:00:00Z",
            grantedBy: "user",
            policyVersion: "v1",
            scope: "service:web",
          },
        ],
      } as never);
    renderPanel();
    const grant = await screen.findByRole("button", { name: "Grant approval" });
    expect(
      screen.queryByRole("button", { name: "Resume waiting job" }),
    ).toBeNull();
    fireEvent.click(grant);
    await waitFor(() =>
      expect(api.grantRuntimeApproval).toHaveBeenCalledWith(appId, {
        fingerprint,
      }),
    );
    expect(api.resumeJob).not.toHaveBeenCalled();
    expect(
      await screen.findByRole("button", { name: "Resume waiting job" }),
    ).not.toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Resume waiting job" }));
    await waitFor(() =>
      expect(api.resumeJob).toHaveBeenCalledWith(waitingJob.id),
    );
  });

  it("requires explicit revoke confirmation and focuses approval-in-use errors", async () => {
    mockData({
      approvals: [
        {
          id: "approval-1",
          appId,
          capability: "network",
          fingerprint: "b".repeat(64),
          grantedAt: "2026-08-01T10:00:00Z",
          grantedBy: "user",
          policyVersion: "v1",
          scope: "service:web",
        },
      ],
    });
    vi.mocked(api.revokeRuntimeApproval).mockRejectedValue(
      new APIError({
        status: 409,
        code: "approval_in_use",
        detail: "Approval is in use",
      }),
    );
    renderPanel();
    fireEvent.click(
      await screen.findByRole("button", { name: "Revoke approval" }),
    );
    const dialog = await screen.findByRole("dialog", {
      name: "Revoke runtime approval",
    });
    fireEvent.click(
      screen.getAllByRole("button", { name: "Revoke approval" }).at(-1)!,
    );
    const alert = await within(dialog).findByRole("alert");
    expect(alert.textContent).toContain("Approval is in use");
    expect(document.activeElement).toBe(alert);
    expect(dialog.contains(document.activeElement)).toBe(true);
  });

  it("clears old application mutation state and uses only the new application id after a route switch", async () => {
    vi.mocked(api.deployments).mockImplementation(
      async (id) => ({ items: id === "app-a" ? [deployment] : [] }) as never,
    );
    vi.mocked(api.releases).mockImplementation(
      async (id) => ({ items: id === "app-a" ? [release] : [] }) as never,
    );
    const { client, rerender } = renderPanel({ appId: "app-a" });
    fireEvent.click(
      await screen.findByRole("button", { name: "Deploy latest" }),
    );
    expect(
      await screen.findByText("Deployment job queued-1 queued."),
    ).not.toBeNull();
    rerender(
      <QueryClientProvider client={client}>
        <DeploymentHistoryPanel
          appId="app-b"
          composeRuntime
          fakeRuntime={false}
        />
      </QueryClientProvider>,
    );
    await screen.findByText("No deployment has been recorded yet.");
    expect(screen.queryByText("Deployment job queued-1 queued.")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Deploy latest" }));
    await waitFor(() =>
      expect(api.deployApplication).toHaveBeenLastCalledWith("app-b"),
    );
  });

  it("ignores an in-flight old application deployment success after switching apps", async () => {
    let resolveOld!: (value: never) => void;
    vi.mocked(api.deployApplication).mockImplementation((id) =>
      id === "app-a"
        ? new Promise((resolve) => {
            resolveOld = resolve;
          })
        : Promise.resolve({ created: true, job: { id: "new-job" } } as never),
    );
    const { client, rerender } = renderPanel({ appId: "app-a" });
    fireEvent.click(
      await screen.findByRole("button", { name: "Deploy latest" }),
    );
    await waitFor(() =>
      expect(api.deployApplication).toHaveBeenCalledWith("app-a"),
    );
    rerender(
      <QueryClientProvider client={client}>
        <DeploymentHistoryPanel
          appId="app-b"
          composeRuntime
          fakeRuntime={false}
        />
      </QueryClientProvider>,
    );
    await act(async () =>
      resolveOld({ created: true, job: { id: "old-job" } } as never),
    );
    expect(screen.queryByText("Deployment job old-job queued.")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Deploy latest" }));
    await waitFor(() =>
      expect(api.deployApplication).toHaveBeenLastCalledWith("app-b"),
    );
  });

  it("ignores an in-flight old application deployment error after switching apps", async () => {
    let rejectOld!: (reason: Error) => void;
    vi.mocked(api.deployApplication).mockImplementation((id) =>
      id === "app-a"
        ? new Promise((_, reject) => {
            rejectOld = reject;
          })
        : Promise.resolve({ created: true, job: { id: "new-job" } } as never),
    );
    const { client, rerender } = renderPanel({ appId: "app-a" });
    fireEvent.click(
      await screen.findByRole("button", { name: "Deploy latest" }),
    );
    await waitFor(() =>
      expect(api.deployApplication).toHaveBeenCalledWith("app-a"),
    );
    rerender(
      <QueryClientProvider client={client}>
        <DeploymentHistoryPanel
          appId="app-b"
          composeRuntime
          fakeRuntime={false}
        />
      </QueryClientProvider>,
    );
    await act(async () => rejectOld(new Error("old application failed")));
    expect(screen.queryByText("old application failed")).toBeNull();
    expect(screen.queryByRole("alert")).toBeNull();
  });
});
