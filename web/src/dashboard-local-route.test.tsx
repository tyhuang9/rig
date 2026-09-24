import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { api } from "./api";
import { ApplicationDetailPage } from "./dashboard";

vi.mock("./application-plan-panel", () => ({ ApplicationPlanPanel: () => null }));
vi.mock("./application-configuration", () => ({ ApplicationConfigurationPanel: () => null }));
vi.mock("./auto-deploy", () => ({ AutoDeployPanel: () => null }));
vi.mock("./deployment-history", () => ({ DeploymentHistoryPanel: () => null, deploymentPlanOrLegacy: async () => null }));

const previousDeployment = {
  id: "deployment-1", appId: "app-1", status: "succeeded", runtimeStrategy: "generated_node",
  releaseId: "release-1", deploymentPlanRevisionId: "plan-1", deploymentPlanRevisionNumber: 2,
  actualConfigurationRevisionId: "config-1", actualConfigurationRevisionNumber: 3,
};
const failedReplacement = {
  ...previousDeployment, id: "deployment-2", status: "failed", releaseId: "release-2",
};
const verifiedRoute = {
  status: "verified", scope: "controller_loopback", url: "http://app-1.rig.localhost:8080/",
  deploymentId: "deployment-1", releaseId: "release-1", planRevisionId: "plan-1",
  planRevisionNumber: 2, configurationRevisionId: "config-1", configurationRevisionNumber: 3,
  observedAt: new Date().toISOString(),
};

function renderDetail() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const view = render(<QueryClientProvider client={client}><MemoryRouter initialEntries={["/apps/app-1"]}>
    <Routes><Route path="/apps/:id" element={<ApplicationDetailPage/>}/></Routes>
  </MemoryRouter></QueryClientProvider>);
  return { client, view };
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => { resolve = done; });
  return { promise, resolve };
}

describe("application controller-host route", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
    vi.spyOn(api, "app").mockResolvedValue({ id: "app-1", name: "Notes", slug: "notes", status: "ready", source: { type: "github" } } as never);
    vi.spyOn(api, "status").mockResolvedValue({ capabilities: { generatedRuntime: true, fakeRuntime: false, composeRuntime: false, githubConnections: false } } as never);
    vi.spyOn(api, "deployments").mockResolvedValue({ items: [failedReplacement, previousDeployment] } as never);
    vi.spyOn(api, "localRoute").mockResolvedValue(verifiedRoute as never);
  });
  afterEach(cleanup);

  it("labels the prior verified deployment as still serving after a failed replacement", async () => {
    renderDetail();
    const link = await screen.findByRole("link", { name: "Open verified local site" });
    await waitFor(() => expect(link.getAttribute("href")).toBe("http://app-1.rig.localhost:8080/"));
    expect(screen.getByText(/a previous successful deployment remains serving after the latest deployment failed/i)).toBeTruthy();
    expect(screen.getByText(/accessible only from the controller host/i)).toBeTruthy();
    expect(within(screen.getByRole("region", { name: "Controller-host route" })).getByRole("status").textContent).toMatch(/a previous successful deployment remains serving/i);
  });

  it("does not link a route when its successful deployment is absent from loaded history", async () => {
    vi.mocked(api.deployments).mockResolvedValue({ items: [failedReplacement] } as never);
    renderDetail();
    expect(await screen.findByText(/no verified local route matches a successful deployment/i)).toBeTruthy();
    const link = screen.getByRole("link", { name: "Open verified local site" });
    expect(link.hasAttribute("href")).toBe(false);
    expect(link.getAttribute("aria-disabled")).toBe("true");
  });

  it("keeps the focused link stable and checks a newly successful replacement after history changes", async () => {
    const replacementRoute = deferred<Awaited<ReturnType<typeof api.localRoute>>>();
    vi.mocked(api.localRoute).mockResolvedValueOnce(verifiedRoute as never).mockReturnValueOnce(replacementRoute.promise);
    const { client } = renderDetail();
    const link = await screen.findByRole("link", { name: "Open verified local site" });
    await waitFor(() => expect(link.hasAttribute("href")).toBe(true));
    link.focus();
    act(() => client.setQueryData(["deployments", "app-1"], { items: [{ ...failedReplacement, status: "succeeded" }, previousDeployment] }));
    expect(await screen.findByText(/checking the active local route and deployment history/i)).toBeTruthy();
    expect(screen.getByRole("link", { name: "Open verified local site" })).toBe(link);
    expect(document.activeElement).toBe(link);
    expect(link.hasAttribute("href")).toBe(false);
    await act(async () => replacementRoute.resolve({ ...verifiedRoute, deploymentId: "deployment-2", releaseId: "release-2" } as never));
    await waitFor(() => expect(link.hasAttribute("href")).toBe(true));
    expect(document.activeElement).toBe(link);
    expect(screen.getByText(/current successful deployment is serving/i)).toBeTruthy();
    expect(api.localRoute).toHaveBeenCalledTimes(2);
  });

  it("keeps the retry button focused while a manual route check runs", async () => {
    const retrying = deferred<Awaited<ReturnType<typeof api.localRoute>>>();
    vi.mocked(api.localRoute).mockRejectedValueOnce(new Error("Docker unavailable")).mockReturnValueOnce(retrying.promise);
    renderDetail();
    expect(await screen.findByText(/could not verify the local route against deployment history/i)).toBeTruthy();
    const retry = screen.getByRole("button", { name: "Check route again" });
    retry.focus();
    fireEvent.click(retry);
    expect(await screen.findByText(/checking the active local route and deployment history/i)).toBeTruthy();
    expect(screen.getByRole("button", { name: "Check route again" })).toBe(retry);
    expect(document.activeElement).toBe(retry);
    expect(retry.getAttribute("aria-disabled")).toBe("true");
    await act(async () => retrying.resolve(verifiedRoute as never));
    await waitFor(() => expect(retry.getAttribute("aria-disabled")).toBe("false"));
    expect(document.activeElement).toBe(retry);
    expect(screen.getByRole("link", { name: "Open verified local site" }).getAttribute("href")).toBe("http://app-1.rig.localhost:8080/");
    expect(within(screen.getByRole("region", { name: "Controller-host route" })).getByRole("status").textContent).toMatch(/a previous successful deployment remains serving/i);
  });

  it("removes the old link when Caddy changes without a history change", async () => {
    const changedRoute = deferred<Awaited<ReturnType<typeof api.localRoute>>>();
    vi.mocked(api.localRoute).mockResolvedValueOnce(verifiedRoute as never).mockReturnValueOnce(changedRoute.promise);
    renderDetail();
    const link = await screen.findByRole("link", { name: "Open verified local site" });
    await waitFor(() => expect(link.hasAttribute("href")).toBe(true));
    fireEvent.click(screen.getByRole("button", { name: "Check route again" }));
    expect(await screen.findByText(/checking the active local route/i)).toBeTruthy();
    expect(link.hasAttribute("href")).toBe(false);
    await act(async () => changedRoute.resolve({ ...verifiedRoute, deploymentId: "another-deployment" } as never));
    await waitFor(() => expect(screen.getByText(/no verified local route matches a successful deployment/i)).toBeTruthy());
    expect(link.hasAttribute("href")).toBe(false);
  });
});
