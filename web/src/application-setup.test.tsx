import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { APIError, api, type Application, type ApplicationConfiguration, type DeploymentPlanRevision } from "./api";
import { ApplicationDeploymentSetup } from "./application-setup";

vi.mock("./application-configuration", () => ({
  ApplicationConfigurationPanel: ({ onContinue }: { onContinue?: () => void }) => <button type="button" onClick={onContinue}>Continue to access</button>,
}));

const app = { id: "app-1", name: "Notes", source: { type: "github", repositoryOwner: "owner", repositoryName: "notes", trackedBranch: "main" } } as Application;
const plan = { revisionId: "plan-1", revisionNumber: 2, state: "accepted", strategy: "generated_node", migration: { present: false } } as DeploymentPlanRevision;
const configuration = { revisionId: "config-1", revisionNumber: 3, formatVersion: 2, deploymentPlanRevisionId: "plan-1", deploymentPlanRevisionNumber: 2, entries: [] } as ApplicationConfiguration;
const queuedJob = { id: "job-1", status: "queued", type: "deploy", resourceId: "app-1" };
const completedJob = { ...queuedJob, status: "succeeded" };
const recordedDeployment = { id: "deployment-1", jobId: "job-1", status: "succeeded", releaseId: "release-1", deploymentPlanRevisionId: "plan-1", deploymentPlanRevisionNumber: 2, actualConfigurationRevisionId: "config-1", actualConfigurationRevisionNumber: 3 };
const reviewedPins = { planId: "plan-1", planNumber: 2, configurationId: "config-1", configurationNumber: 3 };

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((done, fail) => { resolve = done; reject = fail; });
  return { promise, resolve, reject };
}

function renderSetup(initialApp = app) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const view = render(<QueryClientProvider client={client}><MemoryRouter><ApplicationDeploymentSetup app={initialApp}/></MemoryRouter></QueryClientProvider>);
  return { client, rerender: (nextApp: Application) => view.rerender(<QueryClientProvider client={client}><MemoryRouter><ApplicationDeploymentSetup app={nextApp}/></MemoryRouter></QueryClientProvider>) };
}

describe("ApplicationDeploymentSetup", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
    sessionStorage.clear();
    vi.spyOn(api, "deploymentPlan").mockResolvedValue(plan);
    vi.spyOn(api, "applicationConfiguration").mockResolvedValue(configuration);
    vi.spyOn(api, "status").mockResolvedValue({ capabilities: { generatedRuntime: true, fakeRuntime: false } } as never);
    vi.spyOn(api, "job").mockResolvedValue(completedJob as never);
    vi.spyOn(api, "deployments").mockResolvedValue({ items: [recordedDeployment] } as never);
  });

  afterEach(() => cleanup());

  it("reviews scoped configuration and reuses one request key after an uncertain deployment response", async () => {
    const deploy = vi.spyOn(api, "deployApplication")
      .mockRejectedValueOnce(new Error("Connection interrupted"))
      .mockResolvedValueOnce({ created: false, job: queuedJob } as never);
    renderSetup();
    fireEvent.click(await screen.findByRole("button", { name: "Continue to access" }));
    expect(await screen.findByRole("heading", { name: "Access" })).toBeTruthy();
    expect(screen.getByText(/does not provision a database/i)).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Review deployment" }));
    expect(await screen.findByText("Revision 3")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Deploy application" }));
    expect(await screen.findByText("Connection interrupted")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Retry deployment request" }));
    expect(await screen.findByRole("heading", { name: "Deployment job" })).toBeTruthy();
    await screen.findByText(/controller recorded a successful job and deployment/i);
    expect(deploy).toHaveBeenCalledTimes(2);
    expect(deploy.mock.calls[0][1]).toBe(deploy.mock.calls[1][1]);
    expect(deploy.mock.calls[0][1]).toMatch(/^[0-9a-f-]{36}$/);
    expect(deploy.mock.calls[0][2]).toEqual({
      expectedPlanRevisionId: "plan-1", expectedPlanRevisionNumber: 2,
      expectedConfigurationRevisionId: "config-1", expectedConfigurationRevisionNumber: 3,
    });
    expect(deploy.mock.calls[1][2]).toEqual(deploy.mock.calls[0][2]);
    expect(screen.getByText("deployment-1")).toBeTruthy();
    expect(screen.getByText("release-1")).toBeTruthy();
    expect(screen.queryByRole("link", { name: /visit|open site|live url/i })).toBeNull();
  });

  it("resumes a known deployment job from the saved application ID after reload", async () => {
    sessionStorage.setItem("rig-setup-deployment:app-1", JSON.stringify({ signature: "plan-1:2:config-1:3", key: "same-key", jobId: "job-1", reviewedPins }));
    const deploy = vi.spyOn(api, "deployApplication");
    renderSetup();
    expect(await screen.findByRole("heading", { name: "Deployment job" })).toBeTruthy();
    expect(await screen.findByText(/controller recorded a successful job and deployment/i)).toBeTruthy();
    expect(screen.getByText("Job job-1")).toBeTruthy();
    expect(deploy).not.toHaveBeenCalled();
  });

  it("blocks deployment when scoped configuration belongs to another plan revision", async () => {
    vi.mocked(api.applicationConfiguration).mockResolvedValue({ ...configuration, deploymentPlanRevisionId: "old-plan" });
    sessionStorage.setItem("rig-setup-deployment:app-1", JSON.stringify({ signature: "plan-1:2:config-1:3", key: "same-key" }));
    const deploy = vi.spyOn(api, "deployApplication");
    renderSetup();
    expect(await screen.findByRole("heading", { name: "Review and deploy" })).toBeTruthy();
    expect(screen.getByText(/configuration no longer matches this accepted plan/i)).toBeTruthy();
    expect(screen.getByRole("button", { name: "Deploy application" }).hasAttribute("disabled")).toBe(true);
    expect(deploy).not.toHaveBeenCalled();
  });

  it("rechecks the accepted revision before queuing and stops when it changes", async () => {
    vi.mocked(api.deploymentPlan).mockResolvedValueOnce(plan).mockResolvedValueOnce(plan).mockResolvedValueOnce({ ...plan, revisionNumber: 3 });
    const deploy = vi.spyOn(api, "deployApplication");
    renderSetup();
    fireEvent.click(await screen.findByRole("button", { name: "Continue to access" }));
    await screen.findByRole("heading", { name: "Access" });
    fireEvent.click(screen.getByRole("button", { name: "Review deployment" }));
    fireEvent.click(screen.getByRole("button", { name: "Deploy application" }));
    expect(await screen.findByRole("heading", { name: "Configure application" })).toBeTruthy();
    expect(deploy).not.toHaveBeenCalled();
  });

  it("requires separate migration approval before deploying", async () => {
    vi.mocked(api.deploymentPlan).mockResolvedValue({ ...plan, migration: { present: true, approvalStatus: "pending" } });
    sessionStorage.setItem("rig-setup-deployment:app-1", JSON.stringify({ signature: "plan-1:2:config-1:3", key: "same-key" }));
    renderSetup();
    expect(await screen.findByRole("heading", { name: "Review and deploy" })).toBeTruthy();
    expect(screen.getByText(/database migration needs separate approval/i)).toBeTruthy();
    expect(screen.getByRole("button", { name: "Deploy application" }).hasAttribute("disabled")).toBe(true);
  });

  it("waits for the matching deployment record before reporting a successful job", async () => {
    sessionStorage.setItem("rig-setup-deployment:app-1", JSON.stringify({ signature: "plan-1:2:config-1:3", key: "same-key", jobId: "job-1", reviewedPins }));
    vi.mocked(api.deployments).mockResolvedValue({ items: [] });
    renderSetup();
    expect(await screen.findByText(/waiting for the matching deployment record/i)).toBeTruthy();
    expect(screen.queryByText(/controller recorded a successful job and deployment/i)).toBeNull();
  });

  it("does not reuse another application's attempt when the route changes without remounting its parent", async () => {
    sessionStorage.setItem("rig-setup-deployment:app-1", JSON.stringify({ signature: "plan-1:2:config-1:3", key: "old-app-key" }));
    const deploy = vi.spyOn(api, "deployApplication").mockResolvedValue({ created: true, job: { ...queuedJob, id: "job-2", resourceId: "app-2" } } as never);
    const { rerender } = renderSetup();
    await screen.findByRole("heading", { name: "Review and deploy" });
    rerender({ ...app, id: "app-2", name: "Other notes" });
    fireEvent.click(await screen.findByRole("button", { name: "Continue to access" }));
    await screen.findByRole("heading", { name: "Access" });
    fireEvent.click(screen.getByRole("button", { name: "Review deployment" }));
    fireEvent.click(screen.getByRole("button", { name: "Deploy application" }));
    await waitFor(() => expect(deploy).toHaveBeenCalled());
    expect(deploy.mock.calls[0][0]).toBe("app-2");
    expect(deploy.mock.calls[0][1]).not.toBe("old-app-key");
    expect(sessionStorage.getItem("rig-setup-deployment:app-1")).toContain("old-app-key");
  });

  it("keeps the submitted key and records its confirmed job after configuration changes in flight", async () => {
    const response = deferred<Awaited<ReturnType<typeof api.deployApplication>>>();
    const deploy = vi.spyOn(api, "deployApplication").mockReturnValue(response.promise);
    const { client } = renderSetup();
    fireEvent.click(await screen.findByRole("button", { name: "Continue to access" }));
    await screen.findByRole("heading", { name: "Access" });
    fireEvent.click(screen.getByRole("button", { name: "Review deployment" }));
    fireEvent.click(screen.getByRole("button", { name: "Deploy application" }));
    await waitFor(() => expect(deploy).toHaveBeenCalledTimes(1));
    const submittedKey = deploy.mock.calls[0][1];
    await act(async () => client.setQueryData(["app-configuration", "app-1"], { ...configuration, revisionId: "config-2", revisionNumber: 4 }));
    expect(await screen.findByText("Previous deployment request is unresolved.")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Queuing…" }).hasAttribute("disabled")).toBe(true);
    await act(async () => response.resolve({ created: true, job: queuedJob } as never));
    expect(await screen.findByRole("heading", { name: "Deployment job" })).toBeTruthy();
    expect(JSON.parse(sessionStorage.getItem("rig-setup-deployment:app-1") || "{}")).toMatchObject({ key: submittedKey, jobId: "job-1", signature: "plan-1:2:config-1:3", reviewedPins });
  });

  it("keeps an uncertain key after drift and requires a warned history check before resetting it", async () => {
    const response = deferred<Awaited<ReturnType<typeof api.deployApplication>>>();
    const deploy = vi.spyOn(api, "deployApplication").mockReturnValue(response.promise);
    const { client } = renderSetup();
    fireEvent.click(await screen.findByRole("button", { name: "Continue to access" }));
    await screen.findByRole("heading", { name: "Access" });
    fireEvent.click(screen.getByRole("button", { name: "Review deployment" }));
    fireEvent.click(screen.getByRole("button", { name: "Deploy application" }));
    await waitFor(() => expect(deploy).toHaveBeenCalledTimes(1));
    const submittedKey = deploy.mock.calls[0][1];
    await act(async () => client.setQueryData(["app-configuration", "app-1"], { ...configuration, revisionId: "config-2", revisionNumber: 4 }));
    await act(async () => response.reject(new Error("Connection interrupted")));
    expect(await screen.findByText("Previous deployment request is unresolved.")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Retry deployment request" }).hasAttribute("disabled")).toBe(true);
    expect(JSON.parse(sessionStorage.getItem("rig-setup-deployment:app-1") || "{}")).toMatchObject({ key: submittedKey });
    fireEvent.click(screen.getByRole("button", { name: "Resolve uncertain request" }));
    expect(screen.getByText(/starting again may queue another deployment/i)).toBeTruthy();
    const reset = screen.getByRole("button", { name: "Start a new setup request" });
    expect(reset.hasAttribute("disabled")).toBe(true);
    fireEvent.click(screen.getByRole("checkbox", { name: /i checked application history/i }));
    fireEvent.click(reset);
    expect(await screen.findByRole("heading", { name: "Configure application" })).toBeTruthy();
    expect(sessionStorage.getItem("rig-setup-deployment:app-1")).toBeNull();
    expect(deploy).toHaveBeenCalledTimes(1);
  });

  it("recovers a submitted job by exact request key after setup drift", async () => {
    sessionStorage.setItem("rig-setup-deployment:app-1", JSON.stringify({ signature: "plan-1:2:config-1:3", key: "prior-key", reviewedPins }));
    vi.mocked(api.applicationConfiguration).mockResolvedValue({ ...configuration, revisionId: "config-2", revisionNumber: 4 });
    const lookup = vi.spyOn(api, "deploymentJobByIdempotency").mockResolvedValue(completedJob as never);
    const deploy = vi.spyOn(api, "deployApplication");
    renderSetup();
    expect(await screen.findByText("Previous deployment request is unresolved.")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Find submitted job" }));
    expect(await screen.findByRole("heading", { name: "Deployment job" })).toBeTruthy();
    expect(lookup).toHaveBeenCalledWith("app-1", "prior-key");
    expect(deploy).not.toHaveBeenCalled();
    expect(JSON.parse(sessionStorage.getItem("rig-setup-deployment:app-1") || "{}")).toMatchObject({ key: "prior-key", jobId: "job-1" });
  });

  it("retains the exact key after a missing-job lookup because the request may still arrive", async () => {
    sessionStorage.setItem("rig-setup-deployment:app-1", JSON.stringify({ signature: "plan-1:2:config-1:3", key: "prior-key", reviewedPins }));
    vi.mocked(api.applicationConfiguration).mockResolvedValue({ ...configuration, revisionId: "config-2", revisionNumber: 4 });
    vi.spyOn(api, "deploymentJobByIdempotency").mockRejectedValue(new APIError({ status: 404, code: "job_not_found", detail: "Job was not found" }));
    renderSetup();
    fireEvent.click(await screen.findByRole("button", { name: "Find submitted job" }));
    expect(await screen.findByText(/request may still be in flight/i)).toBeTruthy();
    expect(JSON.parse(sessionStorage.getItem("rig-setup-deployment:app-1") || "{}")).toMatchObject({ key: "prior-key" });
    expect(screen.getByRole("button", { name: "Deploy application" }).hasAttribute("disabled")).toBe(true);
  });

  it("requires lookup for a saved request that predates reviewed revision pins", async () => {
    sessionStorage.setItem("rig-setup-deployment:app-1", JSON.stringify({ signature: "plan-1:2:config-1:3", key: "legacy-key" }));
    renderSetup();
    expect(await screen.findByText(/before exact revision pinning was available/i)).toBeTruthy();
    expect(screen.getByRole("button", { name: "Deploy application" }).hasAttribute("disabled")).toBe(true);
    expect(screen.getByRole("button", { name: "Find submitted job" })).toBeTruthy();
  });

  it("shows a known job even when the current plan, configuration, and status cannot load", async () => {
    sessionStorage.setItem("rig-setup-deployment:app-1", JSON.stringify({ signature: "plan-1:2:config-1:3", key: "same-key", jobId: "job-1", reviewedPins }));
    vi.mocked(api.deploymentPlan).mockRejectedValue(new Error("Plan unavailable"));
    vi.mocked(api.applicationConfiguration).mockRejectedValue(new Error("Configuration unavailable"));
    vi.mocked(api.status).mockRejectedValue(new Error("Status unavailable"));
    renderSetup();
    expect(await screen.findByRole("heading", { name: "Deployment job" })).toBeTruthy();
    expect(await screen.findByText(/controller recorded a successful job and deployment for the reviewed revisions/i)).toBeTruthy();
  });

  it("keeps a known job visible after the application's current plan is no longer generated", async () => {
    sessionStorage.setItem("rig-setup-deployment:app-1", JSON.stringify({ signature: "plan-1:2:config-1:3", key: "same-key", jobId: "job-1", reviewedPins }));
    vi.mocked(api.deploymentPlan).mockResolvedValue({ ...plan, state: "draft", strategy: "compose" });
    renderSetup();
    expect(await screen.findByRole("heading", { name: "Deployment job" })).toBeTruthy();
    expect(await screen.findByText(/controller recorded a successful job and deployment for the reviewed revisions/i)).toBeTruthy();
    expect(screen.queryByText("Generated setup is unavailable.")).toBeNull();
  });

  it("warns when a successful job deployed different revisions than the reviewed attempt", async () => {
    sessionStorage.setItem("rig-setup-deployment:app-1", JSON.stringify({ signature: "plan-1:2:config-1:3", key: "same-key", jobId: "job-1", reviewedPins }));
    vi.mocked(api.deployments).mockResolvedValue({ items: [{ ...recordedDeployment, actualConfigurationRevisionId: "config-2", actualConfigurationRevisionNumber: 4 }] } as never);
    renderSetup();
    expect(await screen.findByText(/deployed plan or configuration differs from the revision reviewed here/i)).toBeTruthy();
    expect(screen.queryByText(/successful job and deployment for the reviewed revisions/i)).toBeNull();
  });

  it("announces a pending configuration check and offers a retry after plan drift", async () => {
    const inspection = deferred<DeploymentPlanRevision>();
    vi.mocked(api.deploymentPlan).mockResolvedValueOnce(plan).mockReturnValueOnce(inspection.promise);
    renderSetup();
    fireEvent.click(await screen.findByRole("button", { name: "Continue to access" }));
    expect((await screen.findByText(/checking the accepted plan and saved configuration/i)).getAttribute("role")).toBe("status");
    await act(async () => inspection.resolve({ ...plan, revisionNumber: 3 }));
    expect(await screen.findByText("Cannot continue to access.")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Retry setup check" })).toBeTruthy();
    expect(screen.queryByRole("heading", { name: "Access" })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Retry setup check" }));
    expect(await screen.findByRole("heading", { name: "Access" })).toBeTruthy();
  });
});
