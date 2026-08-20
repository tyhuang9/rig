import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { APIError, api } from "./api";
import { SourceWizard } from "./source-wizard";

const connection = {
  id: "0123456789abcdef0123456789abcdef",
  provider: "github" as const,
  status: "connected" as const,
  providerLogin: "rig-admin",
  credentialGeneration: 1,
  createdAt: "2026-01-01T00:00:00Z",
  updatedAt: "2026-01-01T00:00:00Z",
};

function renderWizard() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const onCreated = vi.fn();
  render(<QueryClientProvider client={client}><SourceWizard onCancel={vi.fn()} onCreated={onCreated} /></QueryClientProvider>);
  return { onCreated };
}

function mockCommon(enabled = true) {
  vi.spyOn(api, "status").mockResolvedValue({ capabilities: { githubConnections: enabled } } as never);
  vi.spyOn(api, "sourceConnections").mockResolvedValue({ items: [connection] });
  vi.spyOn(api, "githubInstallations").mockResolvedValue({ page: 1, perPage: 30, totalCount: 1, items: [{ id: 10, accountLogin: "octo-org", accountType: "Organization", targetType: "Organization", repositorySelection: "selected", cachedAt: "2026-01-01T00:00:00Z" }] });
  vi.spyOn(api, "githubRepositories").mockResolvedValue({ page: 1, perPage: 30, totalCount: 1, items: [{ id: 20, owner: "octo-org", name: "web", defaultBranch: "main", private: true, archived: false, disabled: false }] });
  vi.spyOn(api, "githubBranches").mockResolvedValue({ page: 1, perPage: 30, items: [{ name: "main", sha: "abc123", protected: true }] });
  vi.spyOn(api, "inspect");
  vi.spyOn(api, "createApp").mockResolvedValue({ id: "app-1" } as never);
}

describe("SourceWizard", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
    mockCommon();
  });
  afterEach(() => {
    cleanup();
    vi.useRealTimers();
  });

  it("keeps the local source create flow usable", async () => {
    const { onCreated } = renderWizard();
    fireEvent.change(screen.getByLabelText(/application name/i), { target: { value: "Local app" } });
    fireEvent.change(screen.getByLabelText(/local source path/i), { target: { value: "C:/projects/local" } });
    fireEvent.click(screen.getByRole("button", { name: /save application/i }));

    await waitFor(() => expect(api.createApp).toHaveBeenCalledWith({ name: "Local app", description: "", sourcePath: "C:/projects/local" }, expect.anything()));
    expect(onCreated).toHaveBeenCalledWith("app-1");
  });

  it("shows the capability-disabled state without calling provider endpoints", async () => {
    vi.restoreAllMocks();
    mockCommon(false);
    renderWizard();
    fireEvent.click(screen.getByLabelText(/github repository/i));

    expect(await screen.findByText(/github connections are unavailable/i)).toBeTruthy();
    expect(api.sourceConnections).not.toHaveBeenCalled();
  });

  it("selects a GitHub source, requires an exact clean inspection, and sends only githubSource", async () => {
    vi.mocked(api.inspect)
      .mockResolvedValueOnce({ source: { type: "github" }, resolvedSha: "abc123", composeCandidates: ["compose.yaml"], services: [], findings: [] })
      .mockResolvedValueOnce({ source: { type: "github", composePath: "compose.yaml" }, resolvedSha: "abc123", composeCandidates: ["compose.yaml"], services: [{ name: "web" }], findings: [] });
    const { onCreated } = renderWizard();
    fireEvent.change(screen.getByLabelText(/application name/i), { target: { value: "GitHub app" } });
    fireEvent.click(screen.getByLabelText(/github repository/i));
    await screen.findByLabelText(/github connection/i);
    await screen.findByRole("option", { name: /@rig-admin/i });
    fireEvent.change(screen.getByLabelText(/github connection/i), { target: { value: connection.id } });
    await screen.findByLabelText(/github app installation/i);
    await screen.findByRole("option", { name: /octo-org/i });
    fireEvent.change(screen.getByLabelText(/github app installation/i), { target: { value: "10" } });
    await screen.findByLabelText(/^repository$/i);
    await screen.findByRole("option", { name: /octo-org\/web/i });
    fireEvent.change(screen.getByLabelText(/^repository$/i), { target: { value: "20" } });
    await screen.findByLabelText(/tracked branch/i);
    await screen.findByRole("option", { name: /main/i });
    fireEvent.change(screen.getByLabelText(/tracked branch/i), { target: { value: "main" } });
    fireEvent.click(screen.getByRole("button", { name: /find compose files/i }));
    await screen.findByLabelText(/compose file/i);
    expect(screen.getByRole("button", { name: /save application/i }).hasAttribute("disabled")).toBe(true);
    fireEvent.change(screen.getByLabelText(/compose file/i), { target: { value: "compose.yaml" } });
    fireEvent.click(screen.getByRole("button", { name: /inspect selected compose file/i }));
    await screen.findByText(/source inspection completed/i);
    fireEvent.click(screen.getByRole("button", { name: /save application/i }));

    await waitFor(() => expect(api.createApp).toHaveBeenCalledWith({
      name: "GitHub app",
      description: "",
      githubSource: { connectionId: connection.id, installationId: 10, repositoryId: 20, branch: "main", composePath: "compose.yaml" },
    }, expect.anything()));
    expect(onCreated).toHaveBeenCalledWith("app-1");
  });

  it("clears a successful inspection when an upstream branch changes", async () => {
    vi.mocked(api.inspect)
      .mockResolvedValueOnce({ source: { type: "github" }, composeCandidates: ["compose.yaml"], services: [], findings: [] })
      .mockResolvedValueOnce({ source: { type: "github" }, composeCandidates: ["compose.yaml"], services: [], findings: [] });
    renderWizard();
    fireEvent.click(screen.getByLabelText(/github repository/i));
    await screen.findByLabelText(/github connection/i);
    await screen.findByRole("option", { name: /@rig-admin/i });
    fireEvent.change(screen.getByLabelText(/github connection/i), { target: { value: connection.id } });
    await screen.findByLabelText(/github app installation/i);
    await screen.findByRole("option", { name: /octo-org/i });
    fireEvent.change(screen.getByLabelText(/github app installation/i), { target: { value: "10" } });
    await screen.findByLabelText(/^repository$/i);
    await screen.findByRole("option", { name: /octo-org\/web/i });
    fireEvent.change(screen.getByLabelText(/^repository$/i), { target: { value: "20" } });
    await screen.findByLabelText(/tracked branch/i);
    await screen.findByRole("option", { name: /main/i });
    fireEvent.change(screen.getByLabelText(/tracked branch/i), { target: { value: "main" } });
    fireEvent.click(screen.getByRole("button", { name: /find compose files/i }));
    await screen.findByLabelText(/compose file/i);
    fireEvent.change(screen.getByLabelText(/compose file/i), { target: { value: "compose.yaml" } });
    fireEvent.click(screen.getByRole("button", { name: /inspect selected compose file/i }));
    await screen.findByText(/source inspection completed/i);
    fireEvent.change(screen.getByLabelText(/tracked branch/i), { target: { value: "" } });

    expect(screen.queryByText(/source inspection completed/i)).toBeNull();
    expect(screen.getByRole("button", { name: /save application/i }).hasAttribute("disabled")).toBe(true);
  });

  it("paginates installation results and honors device polling intervals without overlap", async () => {
    vi.mocked(api.githubInstallations).mockImplementation(async (_connectionId, page = 1) => ({ page, perPage: 30, totalCount: 60, items: [{ id: page, accountLogin: `octo-${page}`, accountType: "Organization", targetType: "Organization", repositorySelection: "selected" as const, cachedAt: "2026-01-01T00:00:00Z" }] }));
    vi.spyOn(api, "startGitHubConnection").mockResolvedValue({ connectionId: "new-connection", userCode: "ABCD-EFGH", verificationUri: "https://github.com/login/device", installUrl: "https://github.com/apps/rig/installations/new", expiresAt: "2099-01-01T00:00:00Z", pollIntervalSeconds: 5 });
    let completePoll: ((value: typeof connection) => void) | undefined;
    vi.spyOn(api, "pollGitHubConnection")
      .mockRejectedValueOnce(new APIError({ status: 429, code: "poll_too_soon", detail: "Try again shortly.", retryAfterSeconds: 9 }))
      .mockImplementationOnce(() => new Promise((resolve) => { completePoll = resolve; }));
    renderWizard();
    fireEvent.click(screen.getByLabelText(/github repository/i));
    await screen.findByLabelText(/github connection/i);
    await screen.findByRole("option", { name: /@rig-admin/i });
    fireEvent.change(screen.getByLabelText(/github connection/i), { target: { value: connection.id } });
    await screen.findByLabelText(/github app installation/i);
    await screen.findByRole("option", { name: /octo-1/i });
    fireEvent.click(screen.getByRole("button", { name: /^next$/i }));
    await waitFor(() => expect(api.githubInstallations).toHaveBeenLastCalledWith(connection.id, 2, 30));

    vi.useFakeTimers();
    fireEvent.click(screen.getByRole("button", { name: /connect github/i }));
    await vi.advanceTimersByTimeAsync(0);
    expect(screen.getByText(/ABCD-EFGH/)).toBeTruthy();
    await vi.advanceTimersByTimeAsync(5000);
    expect(api.pollGitHubConnection).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(8000);
    expect(api.pollGitHubConnection).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(1000);
    expect(api.pollGitHubConnection).toHaveBeenCalledTimes(2);
    await vi.advanceTimersByTimeAsync(10000);
    expect(api.pollGitHubConnection).toHaveBeenCalledTimes(2);
    completePoll?.({ ...connection, id: "new-connection", status: "connected" });
    await vi.runOnlyPendingTimersAsync();
  });
});
