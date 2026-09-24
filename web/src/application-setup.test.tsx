import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api, type Application, type ApplicationConfiguration, type DeploymentPlanRevision } from "./api";
import { ApplicationDeploymentSetup } from "./application-setup";

vi.mock("./application-configuration", () => ({
  ApplicationConfigurationPanel: ({ onContinue }: { onContinue?: () => void }) => <button type="button" onClick={onContinue}>Continue to access</button>,
}));

const app = { id: "app-1", name: "Notes", source: { type: "github", repositoryOwner: "owner", repositoryName: "notes", trackedBranch: "main" } } as Application;
const plan = { revisionId: "plan-1", revisionNumber: 2, state: "accepted", strategy: "generated_node", migration: { present: false } } as DeploymentPlanRevision;
const configuration = { revisionId: "config-1", revisionNumber: 3, formatVersion: 2, deploymentPlanRevisionId: "plan-1", deploymentPlanRevisionNumber: 2, entries: [] } as ApplicationConfiguration;
const queuedJob = { id: "job-1", status: "queued", type: "deploy", resourceId: "app-1" };
const completedJob = { ...queuedJob, status: "succeeded" };
const recordedDeployment = { id: "deployment-1", jobId: "job-1", status: "succeeded", releaseId: "release-1", actualConfigurationRevisionNumber: 3 };

function renderSetup() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(<QueryClientProvider client={client}><MemoryRouter><ApplicationDeploymentSetup app={app}/></MemoryRouter></QueryClientProvider>);
}

describe("ApplicationDeploymentSetup", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
    sessionStorage.clear();
    vi.spyOn(api, "deploymentPlan").mockResolvedValue(plan);
    vi.spyOn(api, "applicationConfiguration").mockResolvedValue(configuration);
    vi.spyOn(api, "status").mockResolvedValue({ capabilities: { generatedRuntime: true, fakeRuntime: false } } as never);
    vi.spyOn(api, "job").mockResolvedValue({ job: completedJob } as never);
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
    expect(screen.getByText("deployment-1")).toBeTruthy();
    expect(screen.getByText("release-1")).toBeTruthy();
    expect(screen.queryByRole("link", { name: /visit|open site|live url/i })).toBeNull();
  });

  it("resumes a known deployment job from the saved application ID after reload", async () => {
    sessionStorage.setItem("rig-setup-deployment:app-1", JSON.stringify({ signature: "plan-1:2:config-1:3", key: "same-key", jobId: "job-1" }));
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
    sessionStorage.setItem("rig-setup-deployment:app-1", JSON.stringify({ signature: "plan-1:2:config-1:3", key: "same-key", jobId: "job-1" }));
    vi.mocked(api.deployments).mockResolvedValue({ items: [] });
    renderSetup();
    expect(await screen.findByText(/waiting for the matching deployment record/i)).toBeTruthy();
    expect(screen.queryByText(/controller recorded a successful job and deployment/i)).toBeNull();
  });
});
