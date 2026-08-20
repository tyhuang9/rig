import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { APIError, api } from "./api";
import { isDeviceAuthorizationExpired, SourceWizard } from "./source-wizard";

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
  return { client, onCreated };
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

async function selectConnectedGitHub() {
  fireEvent.click(screen.getByLabelText(/^github repository$/i));
  const connectionSelect = await screen.findByLabelText(/^github connection$/i);
  await screen.findByRole("option", { name: /@rig-admin/i });
  fireEvent.change(connectionSelect, { target: { value: connection.id } });
}

async function selectInstallation() {
  const installation = await screen.findByLabelText(/^github app installation$/i);
  await screen.findByRole("option", { name: /octo-org/i });
  fireEvent.change(installation, { target: { value: "10" } });
}

async function selectRepository() {
  const repository = await screen.findByLabelText(/^repository$/i);
  await screen.findByRole("option", { name: /octo-org\/web/i });
  fireEvent.change(repository, { target: { value: "20" } });
}

async function selectBranch() {
  const trackedBranch = await screen.findByLabelText(/^tracked branch$/i);
  await screen.findByRole("option", { name: /main/i });
  fireEvent.change(trackedBranch, { target: { value: "main" } });
}

async function inspectExactSource() {
  fireEvent.click(screen.getByRole("button", { name: /find compose files/i }));
  const composeFile = await screen.findByLabelText(/^compose file$/i);
  fireEvent.change(composeFile, { target: { value: "compose.yaml" } });
  fireEvent.click(screen.getByRole("button", { name: /inspect selected compose file/i }));
  await screen.findByText(/source inspection completed/i);
}

function mockCleanInspection() {
  vi.mocked(api.inspect)
    .mockResolvedValueOnce({ source: { type: "github" }, resolvedSha: "abc123", composeCandidates: ["compose.yaml"], services: [], findings: [] })
    .mockResolvedValueOnce({ source: { type: "github", composePath: "compose.yaml" }, resolvedSha: "abc123", composeCandidates: ["compose.yaml"], services: [{ name: "web" }], findings: [] });
}

async function reachCleanExactSource() {
  fireEvent.change(screen.getByLabelText(/application name/i), { target: { value: "GitHub app" } });
  await selectConnectedGitHub();
  await selectInstallation();
  await selectRepository();
  await selectBranch();
  await inspectExactSource();
  expect(screen.getByRole("button", { name: /save application/i }).hasAttribute("disabled")).toBe(false);
}

describe("isDeviceAuthorizationExpired", () => {
  const now = Date.parse("2026-08-20T12:00:00Z");

  it("parses timezone offsets numerically and recognizes future and expired values", () => {
    expect(isDeviceAuthorizationExpired("2026-08-20T08:00:01-05:00", now)).toBe(false);
    expect(isDeviceAuthorizationExpired("2026-08-20T06:59:59-05:00", now)).toBe(true);
  });

  it("treats invalid expiration values as expired", () => {
    expect(isDeviceAuthorizationExpired("not-a-timestamp", now)).toBe(true);
  });
});

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

  it("focuses the error summary and links name and local path validation messages", async () => {
    renderWizard();
    fireEvent.click(screen.getByRole("button", { name: /save application/i }));

    const summary = await screen.findByText(/check the highlighted fields/i);
    await waitFor(() => expect(document.activeElement).toBe(summary));
    const name = screen.getByLabelText(/application name/i);
    const localPath = screen.getByLabelText(/local source path/i);
    expect(name.getAttribute("aria-invalid")).toBe("true");
    expect(name.getAttribute("aria-describedby")).toBe("wizard-name-error");
    expect(document.getElementById("wizard-name-error")?.textContent).toMatch(/enter an application name/i);
    expect(localPath.getAttribute("aria-invalid")).toBe("true");
    expect(localPath.getAttribute("aria-describedby")).toBe("wizard-source-path-error");
    expect(document.getElementById("wizard-source-path-error")?.textContent).toMatch(/enter a local source path/i);
  });

  it("links the description length error and clears it when edited", async () => {
    renderWizard();
    fireEvent.change(screen.getByLabelText(/application name/i), { target: { value: "Local app" } });
    fireEvent.change(screen.getByLabelText(/local source path/i), { target: { value: "C:/projects/local" } });
    const description = screen.getByLabelText(/^description$/i);
    fireEvent.change(description, { target: { value: "x".repeat(301) } });
    fireEvent.click(screen.getByRole("button", { name: /save application/i }));

    expect(await screen.findByText(/description must be 300 characters or fewer/i)).toBeTruthy();
    expect(description.getAttribute("aria-invalid")).toBe("true");
    expect(description.getAttribute("aria-describedby")).toBe("wizard-description-error");
    fireEvent.change(description, { target: { value: "Short description" } });
    expect(description.getAttribute("aria-invalid")).toBe("false");
    expect(description.getAttribute("aria-describedby")).toBeNull();
    expect(screen.queryByText(/description must be 300 characters or fewer/i)).toBeNull();
  });

  it("focuses the error summary after a create failure", async () => {
    vi.mocked(api.createApp).mockRejectedValueOnce(new APIError({ status: 503, code: "provider_unavailable", detail: "Application storage is temporarily unavailable." }));
    renderWizard();
    fireEvent.change(screen.getByLabelText(/application name/i), { target: { value: "Local app" } });
    fireEvent.change(screen.getByLabelText(/local source path/i), { target: { value: "C:/projects/local" } });
    fireEvent.click(screen.getByRole("button", { name: /save application/i }));

    const summary = await screen.findByText(/application storage is temporarily unavailable/i);
    await waitFor(() => expect(document.activeElement).toBe(summary));
  });

  it("shows local inspection failures in context and clears them when the source changes", async () => {
    vi.mocked(api.inspect).mockRejectedValueOnce(new APIError({ status: 422, code: "invalid_source", detail: "The selected folder could not be inspected." }));
    renderWizard();
    const localPath = screen.getByLabelText(/local source path/i);
    fireEvent.change(localPath, { target: { value: "C:/projects/broken" } });
    fireEvent.click(screen.getByRole("button", { name: /check source/i }));
    expect(await screen.findByText(/selected folder could not be inspected/i)).toBeTruthy();

    fireEvent.change(localPath, { target: { value: "C:/projects/fixed" } });
    expect(screen.queryByText(/selected folder could not be inspected/i)).toBeNull();
    fireEvent.click(screen.getByLabelText(/^github repository$/i));
    fireEvent.click(screen.getByLabelText(/^local folder$/i));
    expect(screen.queryByText(/selected folder could not be inspected/i)).toBeNull();
  });

  it("shows the capability-disabled state without calling provider endpoints", async () => {
    vi.restoreAllMocks();
    mockCommon(false);
    renderWizard();
    fireEvent.click(screen.getByLabelText(/github repository/i));

    expect(await screen.findByText(/github connections are unavailable/i)).toBeTruthy();
    expect(api.sourceConnections).not.toHaveBeenCalled();
  });

  it("focuses a GitHub prerequisite summary when an incomplete form is submitted", async () => {
    renderWizard();
    fireEvent.change(screen.getByLabelText(/application name/i), { target: { value: "GitHub app" } });
    fireEvent.click(screen.getByLabelText(/^github repository$/i));
    await screen.findByLabelText(/^github connection$/i);
    const saveButton = screen.getByRole("button", { name: /save application/i });
    expect(saveButton.getAttribute("aria-describedby")).toBe("github-save-help");
    expect(document.getElementById("github-save-help")?.textContent).toMatch(/choose a connected github account before saving/i);
    const form = screen.getByText("Application source").closest("form");
    if (!form) throw new Error("Expected source wizard form");
    fireEvent.submit(form);

    const summary = await screen.findByText(/complete the github source steps and a clean exact-source inspection/i);
    await waitFor(() => expect(document.activeElement).toBe(summary));
  });

  it("distinguishes a capability error from disabled and retries it", async () => {
    vi.mocked(api.status)
      .mockRejectedValueOnce(new APIError({ status: 503, code: "provider_unavailable", detail: "Controller status is temporarily unavailable." }))
      .mockResolvedValueOnce({ capabilities: { githubConnections: true } } as never);
    renderWizard();
    fireEvent.click(screen.getByLabelText(/^github repository$/i));

    expect(await screen.findByText(/controller status is temporarily unavailable/i)).toBeTruthy();
    expect(screen.queryByText(/github connections are unavailable/i)).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: /retry capability check/i }));
    expect(await screen.findByLabelText(/^github connection$/i)).toBeTruthy();
  });

  it("keeps a stable disabled connection control while loading", async () => {
    let resolveConnections: ((value: { items: Array<typeof connection> }) => void) | undefined;
    vi.mocked(api.sourceConnections).mockImplementationOnce(() => new Promise((resolve) => { resolveConnections = resolve; }));
    renderWizard();
    fireEvent.click(screen.getByLabelText(/^github repository$/i));

    const select = await screen.findByLabelText(/^github connection$/i) as HTMLSelectElement;
    expect(select.disabled).toBe(true);
    expect(screen.getByRole("option", { name: /loading connections/i })).toBeTruthy();
    resolveConnections?.({ items: [connection] });
    await waitFor(() => expect(select.disabled).toBe(false));
  });

  it("keeps the connection control and retries a failed connection list", async () => {
    vi.mocked(api.sourceConnections)
      .mockRejectedValueOnce(new APIError({ status: 503, code: "provider_unavailable", detail: "Connections are temporarily unavailable." }))
      .mockResolvedValueOnce({ items: [connection] });
    renderWizard();
    fireEvent.click(screen.getByLabelText(/^github repository$/i));

    const select = await screen.findByLabelText(/^github connection$/i) as HTMLSelectElement;
    expect(select.disabled).toBe(true);
    expect(await screen.findByText(/connections are temporarily unavailable/i)).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: /retry connections/i }));
    await screen.findByRole("option", { name: /@rig-admin/i });
    expect(select.disabled).toBe(false);
  });

  it("keeps a failed discovery list in context and retries it", async () => {
    vi.mocked(api.githubInstallations)
      .mockRejectedValueOnce(new APIError({ status: 503, code: "provider_unavailable", detail: "Installations are temporarily unavailable." }))
      .mockResolvedValueOnce({ page: 1, perPage: 30, totalCount: 1, items: [{ id: 10, accountLogin: "octo-org", accountType: "Organization", targetType: "Organization", repositorySelection: "selected", cachedAt: "2026-01-01T00:00:00Z" }] });
    renderWizard();
    await selectConnectedGitHub();

    const select = await screen.findByLabelText(/^github app installation$/i) as HTMLSelectElement;
    expect(select.disabled).toBe(true);
    expect(await screen.findByText(/installations are temporarily unavailable/i)).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: /retry github app installation/i }));
    await screen.findByRole("option", { name: /octo-org/i });
    expect(select.disabled).toBe(false);
  });

  it("uses one persistent atomic live region from device instructions through connection", async () => {
    vi.spyOn(api, "startGitHubConnection").mockResolvedValue({ connectionId: "new-connection", userCode: "ABCD-EFGH", verificationUri: "https://github.com/login/device", installUrl: "https://github.com/apps/rig/installations/new", expiresAt: "2099-01-01T00:00:00Z", pollIntervalSeconds: 1 });
    vi.spyOn(api, "pollGitHubConnection").mockResolvedValue({ ...connection, id: "new-connection", status: "connected" });
    const { client } = renderWizard();
    fireEvent.click(screen.getByLabelText(/^github repository$/i));
    await screen.findByLabelText(/^github connection$/i);
    vi.useFakeTimers();
    fireEvent.click(screen.getByRole("button", { name: /connect github/i }));
    await vi.advanceTimersByTimeAsync(0);

    const code = screen.getByText("ABCD-EFGH");
    const liveRegion = code.closest("[role='status']");
    expect(liveRegion?.getAttribute("aria-live")).toBe("polite");
    expect(liveRegion?.getAttribute("aria-atomic")).toBe("true");
    expect(screen.getAllByRole("status")).toHaveLength(1);
    expect(screen.getByRole("link", { name: /open github device authorization \(opens in a new tab\)/i }).getAttribute("rel")).toBe("noreferrer");
    expect(screen.getByRole("link", { name: /install or configure the rig github app \(opens in a new tab\)/i }).getAttribute("target")).toBe("_blank");

    await vi.advanceTimersByTimeAsync(1000);
    expect(api.pollGitHubConnection).toHaveBeenCalledTimes(1);
    await vi.runOnlyPendingTimersAsync();
    const connectedRegion = screen.getByText(/connection status: connected/i).closest("[role='status']");
    expect(connectedRegion).toBe(liveRegion);
    expect(screen.queryByText("ABCD-EFGH")).toBeNull();
    expect(screen.getAllByRole("status")).toHaveLength(1);

    await act(async () => {
      client.setQueryData(["source-connections"], { items: [{ ...connection, id: "new-connection", status: "access_lost" }] });
      await vi.runOnlyPendingTimersAsync();
    });
    const accessLostRegion = screen.getByText(/connection status: access lost/i).closest("[role='status']");
    expect(accessLostRegion).toBe(liveRegion);
  });

  it("disables every connection action while one mutation is pending", async () => {
    let resolveRefresh: ((value: typeof connection) => void) | undefined;
    vi.spyOn(api, "refreshSourceConnection").mockImplementation(() => new Promise((resolve) => { resolveRefresh = resolve; }));
    renderWizard();
    await selectConnectedGitHub();
    await screen.findByRole("button", { name: /refresh connection/i });
    fireEvent.click(screen.getByRole("button", { name: /refresh connection/i }));

    await waitFor(() => expect((screen.getByRole("button", { name: /connect github/i }) as HTMLButtonElement).disabled).toBe(true));
    expect((screen.getByRole("button", { name: /refreshing/i }) as HTMLButtonElement).disabled).toBe(true);
    expect((screen.getByRole("button", { name: /disconnect/i }) as HTMLButtonElement).disabled).toBe(true);
    resolveRefresh?.(connection);
    await waitFor(() => expect((screen.getByRole("button", { name: /connect github/i }) as HTMLButtonElement).disabled).toBe(false));
  });

  it("treats an invalid device expiration as terminal and never polls", async () => {
    vi.spyOn(api, "startGitHubConnection").mockResolvedValue({ connectionId: "new-connection", userCode: "ABCD-EFGH", verificationUri: "https://github.com/login/device", installUrl: "https://github.com/apps/rig/installations/new", expiresAt: "invalid", pollIntervalSeconds: 5 });
    const poll = vi.spyOn(api, "pollGitHubConnection");
    renderWizard();
    fireEvent.click(screen.getByLabelText(/^github repository$/i));
    await screen.findByLabelText(/^github connection$/i);
    const connectionRegion = screen.getByRole("status");
    fireEvent.click(screen.getByRole("button", { name: /connect github/i }));

    const expiration = await screen.findByText(/github authorization expired/i);
    expect(expiration.closest("[role='status']")).toBe(connectionRegion);
    expect(connectionRegion.textContent).toMatch(/connection status: expired/i);
    expect(screen.queryByText("ABCD-EFGH")).toBeNull();
    expect(poll).not.toHaveBeenCalled();
  });

  it("shows recovery guidance when no installations are available", async () => {
    vi.mocked(api.githubInstallations)
      .mockResolvedValueOnce({ page: 1, perPage: 30, totalCount: 0, items: [] })
      .mockResolvedValueOnce({ page: 1, perPage: 30, totalCount: 1, items: [{ id: 10, accountLogin: "octo-org", accountType: "Organization", targetType: "Organization", repositorySelection: "selected", cachedAt: "2026-01-01T00:00:00Z" }] });
    renderWizard();
    await selectConnectedGitHub();

    expect(await screen.findByText(/no github app installations found/i)).toBeTruthy();
    expect(screen.getByText(/install or configure the rig github app, then retry/i)).toBeTruthy();
    const installation = screen.getByLabelText(/^github app installation$/i) as HTMLSelectElement;
    expect(installation.disabled).toBe(true);
    expect(screen.queryByRole("navigation", { name: /github app installations pagination/i })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: /retry github app installation/i }));
    await screen.findByRole("option", { name: /octo-org/i });
    expect(installation.disabled).toBe(false);
    expect(screen.queryByRole("navigation", { name: /github app installations pagination/i })).toBeNull();
  });

  it("shows recovery guidance when no repositories are available", async () => {
    vi.mocked(api.githubRepositories)
      .mockResolvedValueOnce({ page: 1, perPage: 30, totalCount: 0, items: [] })
      .mockResolvedValueOnce({ page: 1, perPage: 30, totalCount: 1, items: [{ id: 20, owner: "octo-org", name: "web", defaultBranch: "main", private: true, archived: false, disabled: false }] });
    renderWizard();
    await selectConnectedGitHub();
    await selectInstallation();

    expect(await screen.findByText(/no repositories found/i)).toBeTruthy();
    expect(screen.getByText(/update the github app repository access, then retry/i)).toBeTruthy();
    const repository = screen.getByLabelText(/^repository$/i) as HTMLSelectElement;
    expect(repository.disabled).toBe(true);
    expect(screen.queryByRole("navigation", { name: /repositories pagination/i })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: /retry repository/i }));
    await screen.findByRole("option", { name: /octo-org\/web/i });
    expect(repository.disabled).toBe(false);
    expect(screen.queryByRole("navigation", { name: /repositories pagination/i })).toBeNull();
  });

  it("shows recovery guidance when no branches are available", async () => {
    vi.mocked(api.githubBranches)
      .mockResolvedValueOnce({ page: 1, perPage: 30, items: [] })
      .mockResolvedValueOnce({ page: 1, perPage: 30, items: [{ name: "main", sha: "abc123", protected: true }] });
    renderWizard();
    await selectConnectedGitHub();
    await selectInstallation();
    await selectRepository();

    expect(await screen.findByText(/no branches found/i)).toBeTruthy();
    expect(screen.getByText(/push a tracked branch or choose another repository, then retry/i)).toBeTruthy();
    const trackedBranch = screen.getByLabelText(/^tracked branch$/i) as HTMLSelectElement;
    expect(trackedBranch.disabled).toBe(true);
    expect(screen.queryByRole("navigation", { name: /branches pagination/i })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: /retry tracked branch/i }));
    await screen.findByRole("option", { name: /main/i });
    expect(trackedBranch.disabled).toBe(false);
    expect(screen.queryByRole("navigation", { name: /branches pagination/i })).toBeNull();
  });

  it("does not report a clean discovery with no Compose candidates as successful", async () => {
    vi.mocked(api.inspect).mockResolvedValueOnce({ source: { type: "github" }, resolvedSha: "abc123", composeCandidates: [], services: [], findings: [] });
    renderWizard();
    await selectConnectedGitHub();
    await selectInstallation();
    await selectRepository();
    await selectBranch();
    fireEvent.click(screen.getByRole("button", { name: /find compose files/i }));

    const emptyResult = (await screen.findByText(/no compose files found/i)).closest("[role='status']");
    expect(emptyResult?.getAttribute("aria-live")).toBe("polite");
    expect(emptyResult?.getAttribute("aria-atomic")).toBe("true");
    expect(emptyResult?.textContent).toMatch(/add a compose file to the tracked branch, then inspect again/i);
    expect(screen.queryByText(/source inspection completed/i)).toBeNull();
    expect(screen.queryByLabelText(/^compose file$/i)).toBeNull();
  });

  it("selects a GitHub source, requires an exact clean inspection, and sends only githubSource", async () => {
    vi.mocked(api.inspect)
      .mockResolvedValueOnce({ source: { type: "github" }, resolvedSha: "abc123", composeCandidates: ["compose.yaml"], services: [], findings: [] })
      .mockResolvedValueOnce({ source: { type: "github", composePath: "compose.yaml" }, resolvedSha: "abc123", composeCandidates: ["compose.yaml"], services: [{ name: "web" }], findings: [] });
    const { onCreated } = renderWizard();
    fireEvent.change(screen.getByLabelText(/application name/i), { target: { value: "GitHub app" } });
    await selectConnectedGitHub();
    await selectInstallation();
    await selectRepository();
    await selectBranch();
    expect(document.getElementById("github-save-help")?.textContent).toMatch(/find and choose a compose file before saving/i);
    fireEvent.click(screen.getByRole("button", { name: /find compose files/i }));
    await screen.findByLabelText(/^compose file$/i);
    expect(screen.getByRole("button", { name: /save application/i }).hasAttribute("disabled")).toBe(true);
    fireEvent.change(screen.getByLabelText(/compose file/i), { target: { value: "compose.yaml" } });
    expect(document.getElementById("github-save-help")?.textContent).toMatch(/inspect the selected compose file before saving/i);
    fireEvent.click(screen.getByRole("button", { name: /inspect selected compose file/i }));
    const cleanResult = (await screen.findByText(/source inspection completed/i)).closest("[role='status']");
    expect(cleanResult?.getAttribute("aria-live")).toBe("polite");
    expect(cleanResult?.getAttribute("aria-atomic")).toBe("true");
    expect(document.getElementById("github-save-help")?.textContent).toMatch(/ready to save/i);
    fireEvent.click(screen.getByRole("button", { name: /save application/i }));

    await waitFor(() => expect(api.createApp).toHaveBeenCalledWith({
      name: "GitHub app",
      description: "",
      githubSource: { connectionId: connection.id, installationId: 10, repositoryId: 20, branch: "main", composePath: "compose.yaml" },
    }, expect.anything()));
    expect(onCreated).toHaveBeenCalledWith("app-1");
  });

  it("describes policy findings truthfully and keeps saving blocked", async () => {
    vi.mocked(api.inspect)
      .mockResolvedValueOnce({ source: { type: "github" }, composeCandidates: ["compose.yaml"], services: [], findings: [] })
      .mockResolvedValueOnce({ source: { type: "github", composePath: "compose.yaml" }, composeCandidates: ["compose.yaml"], services: [], findings: [{ code: "unsupported_path", message: "A referenced file leaves the release workspace." }] });
    renderWizard();
    fireEvent.change(screen.getByLabelText(/application name/i), { target: { value: "GitHub app" } });
    await selectConnectedGitHub();
    await selectInstallation();
    await selectRepository();
    await selectBranch();
    fireEvent.click(screen.getByRole("button", { name: /find compose files/i }));
    const composeFile = await screen.findByLabelText(/^compose file$/i);
    fireEvent.change(composeFile, { target: { value: "compose.yaml" } });
    fireEvent.click(screen.getByRole("button", { name: /inspect selected compose file/i }));

    const findingsResult = (await screen.findByText(/source requires changes before it can be saved/i)).closest("[role='status']");
    expect(findingsResult?.getAttribute("aria-live")).toBe("polite");
    expect(findingsResult?.getAttribute("aria-atomic")).toBe("true");
    expect(findingsResult?.textContent).toMatch(/referenced file leaves the release workspace/i);
    expect(screen.getByRole("button", { name: /save application/i }).hasAttribute("disabled")).toBe(true);
  });

  it("clears a successful inspection when an upstream branch changes", async () => {
    vi.mocked(api.inspect)
      .mockResolvedValueOnce({ source: { type: "github" }, composeCandidates: ["compose.yaml"], services: [], findings: [] })
      .mockResolvedValueOnce({ source: { type: "github" }, composeCandidates: ["compose.yaml"], services: [], findings: [] });
    renderWizard();
    await selectConnectedGitHub();
    await selectInstallation();
    await selectRepository();
    await selectBranch();
    await inspectExactSource();
    fireEvent.change(screen.getByLabelText(/^tracked branch$/i), { target: { value: "" } });

    expect(screen.queryByText(/source inspection completed/i)).toBeNull();
    expect(screen.getByRole("button", { name: /save application/i }).hasAttribute("disabled")).toBe(true);
  });

  it("clears installation and all downstream state when the installation page changes", async () => {
    vi.mocked(api.githubInstallations).mockImplementation(async (_connectionId, page = 1) => ({ page, perPage: 30, totalCount: 60, items: [{ id: page === 1 ? 10 : 11, accountLogin: page === 1 ? "octo-org" : "octo-2", accountType: "Organization", targetType: "Organization", repositorySelection: "selected" as const, cachedAt: "2026-01-01T00:00:00Z" }] }));
    mockCleanInspection();
    renderWizard();
    await reachCleanExactSource();

    fireEvent.click(screen.getByRole("button", { name: /next github app installations page/i }));
    await waitFor(() => expect(api.githubInstallations).toHaveBeenLastCalledWith(connection.id, 2, 30));
    expect((screen.getByLabelText(/^github app installation$/i) as HTMLSelectElement).value).toBe("");
    expect(screen.queryByLabelText(/^repository$/i)).toBeNull();
    expect(screen.queryByText(/source inspection completed/i)).toBeNull();
    expect(screen.getByRole("button", { name: /save application/i }).hasAttribute("disabled")).toBe(true);
  });

  it("clears repository and all downstream state when the repository page changes", async () => {
    vi.mocked(api.githubRepositories).mockImplementation(async (_connectionId, _installationId, page = 1) => ({ page, perPage: 30, totalCount: 60, items: [{ id: page === 1 ? 20 : 21, owner: "octo-org", name: `web-${page}`, defaultBranch: "main", private: true, archived: false, disabled: false }] }));
    mockCleanInspection();
    renderWizard();
    await reachCleanExactSource();

    fireEvent.click(screen.getByRole("button", { name: /next repositories page/i }));
    await waitFor(() => expect(api.githubRepositories).toHaveBeenLastCalledWith(connection.id, 10, 2, 30));
    expect((screen.getByLabelText(/^repository$/i) as HTMLSelectElement).value).toBe("");
    expect(screen.queryByLabelText(/^tracked branch$/i)).toBeNull();
    expect(screen.queryByText(/source inspection completed/i)).toBeNull();
    expect(screen.getByRole("button", { name: /save application/i }).hasAttribute("disabled")).toBe(true);
  });

  it("clears branch and Compose inspection state when the branch page changes", async () => {
    vi.mocked(api.githubBranches).mockImplementation(async (_connectionId, _installationId, _repositoryId, page = 1) => ({
      page,
      perPage: 30,
      items: page === 1
        ? [{ name: "main", sha: "abc123", protected: true }, ...Array.from({ length: 29 }, (_, index) => ({ name: `branch-${index + 1}`, sha: `sha-${index + 1}`, protected: false }))]
        : [{ name: "release", sha: "def456", protected: false }],
    }));
    mockCleanInspection();
    renderWizard();
    await reachCleanExactSource();

    fireEvent.click(screen.getByRole("button", { name: /next branches page/i }));
    await waitFor(() => expect(api.githubBranches).toHaveBeenLastCalledWith(connection.id, 10, 20, 2, 30));
    expect((screen.getByLabelText(/^tracked branch$/i) as HTMLSelectElement).value).toBe("");
    expect(screen.queryByLabelText(/^compose file$/i)).toBeNull();
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
    await selectConnectedGitHub();
    await screen.findByLabelText(/^github app installation$/i);
    await screen.findByRole("option", { name: /octo-1/i });
    fireEvent.click(screen.getByRole("button", { name: /next github app installations page/i }));
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
