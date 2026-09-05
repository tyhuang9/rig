import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { useState } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { APIError, api, clearCSRF, type ConnectedGitHubRepository } from "./api";
import { GitHubConnectionCard, GitHubRepositoryPicker } from "./github-connection";

const connection = { id: "a".repeat(32), provider: "github" as const, status: "connected" as const, providerLogin: "octocat", credentialGeneration: 1, installUrl: "https://github.com/apps/rig/installations/new", createdAt: "2026-01-01T00:00:00Z", updatedAt: "2026-01-01T00:00:00Z" };
const repository = { connectionId: connection.id, installationId: 7, id: 9, owner: "octo-org", name: "web", defaultBranch: "main", private: true, archived: false, disabled: false, accountLogin: "octo-org" };
const page = { page: 1, perPage: 30, totalCount: 1, truncated: false, items: [repository] };
const authorization = () => ({ authorizationId: "b".repeat(32), connectionId: connection.id, userCode: "ABCD-EFGH", verificationUri: "https://github.com/login/device", installUrl: connection.installUrl, expiresAt: new Date(Date.now() + 60000).toISOString(), pollIntervalSeconds: 5 });
function deferred<T>() { let resolve!: (value: T) => void; const promise = new Promise<T>((done) => { resolve = done; }); return { promise, resolve }; }
function renderCard() { const client = new QueryClient({ defaultOptions: { queries: { retry: false } } }); return { client, ...render(<QueryClientProvider client={client}><GitHubConnectionCard /></QueryClientProvider>) }; }
async function tick(ms = 0) { await act(async () => { await vi.advanceTimersByTimeAsync(ms); }); }
async function beginAuthorization() {
  renderCard(); await tick();
  fireEvent.click(screen.getByRole("button", { name: "Connect GitHub" })); await tick();
  expect(screen.getByText("ABCD-EFGH")).toBeTruthy();
}

beforeEach(() => {
  vi.restoreAllMocks(); sessionStorage.clear();
  vi.spyOn(api, "status").mockResolvedValue({ capabilities: { githubConnections: true } } as never);
  vi.spyOn(api, "defaultSourceConnection").mockResolvedValue({ configured: true, connection });
  vi.spyOn(api, "defaultGitHubRepositories").mockResolvedValue(page);
  vi.spyOn(api, "startDefaultGitHubConnection").mockImplementation(async () => authorization());
  vi.spyOn(api, "pollDefaultGitHubConnection").mockResolvedValue({ authorizationId: "b".repeat(32), status: "connected", connection });
});
afterEach(() => { cleanup(); vi.useRealTimers(); });

describe("GitHubConnectionCard", () => {
  it("reuses a saved connection after remount without starting authorization", async () => {
    const first = renderCard();
    await screen.findByText("Connected as @octocat");
    expect(screen.queryByLabelText("GitHub connection")).toBeNull();
    first.unmount(); renderCard();
    await screen.findByText("Connected as @octocat");
    expect(api.startDefaultGitHubConnection).not.toHaveBeenCalled();
    expect(screen.queryByRole("button", { name: "Reconnect" })).toBeNull();
    expect(screen.getByRole("link", { name: /Manage repository access/ }).getAttribute("href")).toBe(connection.installUrl);
  });
  it("disables authorization when GitHub capability is disabled", async () => {
    vi.mocked(api.defaultSourceConnection).mockResolvedValue({ configured: true, connection: { ...connection, status: "access_lost" } });
    vi.mocked(api.status).mockResolvedValue({ capabilities: { githubConnections: false } } as never);
    renderCard(); await screen.findByText("GitHub connections are disabled on this controller.");
    expect((screen.getByRole("button", { name: "Reconnect" }) as HTMLButtonElement).disabled).toBe(true);
    expect(api.startDefaultGitHubConnection).not.toHaveBeenCalled();
  });
  it("retains a connection load error with an explicit retry", async () => {
    vi.mocked(api.defaultSourceConnection).mockRejectedValueOnce(new Error("sensitive provider body"));
    renderCard(); await screen.findByText("GitHub connection could not be loaded.");
    expect(document.body.textContent).not.toContain("sensitive provider body");
    fireEvent.click(screen.getByRole("button", { name: "Retry connection" }));
    await screen.findByText("Connected as @octocat");
  });
  it("guides device consent into repository access and preserves one live region and keyboard focus", async () => {
    vi.useFakeTimers();
    vi.mocked(api.defaultSourceConnection).mockResolvedValue({ configured: false });
    await beginAuthorization();
    const link = screen.getByRole("link", { name: "Authorize GitHub (opens in a new tab)" });
    expect(document.activeElement).toBe(link);
    const status = link.closest("[role='status']");
    expect(status?.getAttribute("aria-atomic")).toBe("true");
    vi.mocked(api.defaultSourceConnection).mockResolvedValue({ configured: true, connection });
    await tick(4999); expect(api.pollDefaultGitHubConnection).not.toHaveBeenCalled();
    await tick(1); await tick();
    expect(api.pollDefaultGitHubConnection).toHaveBeenCalledWith(connection.id, "b".repeat(32));
    expect(screen.getByText("Connected as @octocat").closest("[role='status']")).toBe(status);
    expect(document.activeElement).toBe(screen.getByRole("link", { name: /Manage repository access/ }));
    expect(sessionStorage.getItem("rig-github-authorization")).toBeNull();
  });
  it("resumes a saved per-tab attempt after remount without starting a second grant", async () => {
    vi.useFakeTimers();
    sessionStorage.setItem("rig-github-authorization", JSON.stringify(authorization()));
    renderCard(); await tick();
    expect(screen.getByText("ABCD-EFGH")).toBeTruthy();
    expect(api.startDefaultGitHubConnection).not.toHaveBeenCalled();
    await tick(5000);
    expect(api.pollDefaultGitHubConnection).toHaveBeenCalledTimes(1);
  });
  it("does not restore another account's attempt and clears authorization metadata on logout", async () => {
    sessionStorage.setItem("rig-github-authorization", JSON.stringify({ ...authorization(), connectionId: "c".repeat(32) }));
    renderCard(); await screen.findByText("Connected as @octocat");
    expect(screen.queryByText("ABCD-EFGH")).toBeNull();
    expect(api.pollDefaultGitHubConnection).not.toHaveBeenCalled();
    sessionStorage.setItem("rig-github-authorization", JSON.stringify(authorization()));
    clearCSRF(); expect(sessionStorage.getItem("rig-github-authorization")).toBeNull();
  });
  it("drives reconnect progress from the attempt status while the existing connection stays connected", async () => {
    vi.useFakeTimers();
    sessionStorage.setItem("rig-github-authorization", JSON.stringify(authorization()));
    vi.mocked(api.pollDefaultGitHubConnection).mockResolvedValue({ authorizationId: "b".repeat(32), status: "pending", connection });
    renderCard(); await tick(); await tick(5000);
    expect(screen.getByText("ABCD-EFGH")).toBeTruthy();
    expect(api.pollDefaultGitHubConnection).toHaveBeenCalledTimes(1);
    await tick(5000); expect(api.pollDefaultGitHubConnection).toHaveBeenCalledTimes(2);
  });
  it("honors Retry-After and never overlaps unresolved polls", async () => {
    vi.useFakeTimers();
    sessionStorage.setItem("rig-github-authorization", JSON.stringify(authorization()));
    const pending = deferred<Awaited<ReturnType<typeof api.pollDefaultGitHubConnection>>>();
    vi.mocked(api.pollDefaultGitHubConnection).mockRejectedValueOnce(new APIError({ status: 429, code: "poll_too_soon", detail: "wait", retryAfterSeconds: 9 })).mockReturnValueOnce(pending.promise);
    renderCard(); await tick(); await tick(5000); await tick(8999);
    expect(api.pollDefaultGitHubConnection).toHaveBeenCalledTimes(1);
    await tick(1); await tick(10000); expect(api.pollDefaultGitHubConnection).toHaveBeenCalledTimes(2);
    await act(async () => pending.resolve({ authorizationId: "b".repeat(32), status: "connected", connection }));
  });
  it("pauses transient poll failures until explicit retry and keeps the same attempt", async () => {
    vi.useFakeTimers();
    sessionStorage.setItem("rig-github-authorization", JSON.stringify(authorization()));
    vi.mocked(api.pollDefaultGitHubConnection).mockRejectedValueOnce(new Error("provider secret"));
    renderCard(); await tick(); await tick(5000); await tick(20000);
    expect(api.pollDefaultGitHubConnection).toHaveBeenCalledTimes(1);
    expect(document.body.textContent).not.toContain("provider secret");
    fireEvent.click(screen.getByRole("button", { name: "Retry authorization check" })); await tick();
    expect(api.pollDefaultGitHubConnection).toHaveBeenCalledTimes(2);
    expect(api.startDefaultGitHubConnection).not.toHaveBeenCalled();
  });
  it.each(["denied", "expired", "superseded", "failed"] as const)("stops a %s attempt and preserves the saved connection", async (status) => {
    vi.useFakeTimers();
    sessionStorage.setItem("rig-github-authorization", JSON.stringify(authorization()));
    vi.mocked(api.pollDefaultGitHubConnection).mockResolvedValue({ authorizationId: "b".repeat(32), status, connection });
    renderCard(); await tick(); await tick(5000); await tick(20000);
    expect(api.pollDefaultGitHubConnection).toHaveBeenCalledTimes(1);
    expect(screen.queryByText("ABCD-EFGH")).toBeNull();
    expect(screen.getByText("Connected as @octocat")).toBeTruthy();
    expect(screen.getByRole("alert")).toBeTruthy();
  });
  it.each(["authorization_identity_mismatch", "authorization_superseded", "authorization_failed"])("ends terminal %s errors and focuses reconnect instead of retrying a dead attempt", async (code) => {
    vi.useFakeTimers();
    vi.mocked(api.defaultSourceConnection).mockResolvedValue({ configured: true, connection: { ...connection, status: "access_lost" } });
    sessionStorage.setItem("rig-github-authorization", JSON.stringify(authorization()));
    vi.mocked(api.pollDefaultGitHubConnection).mockRejectedValue(new APIError({ status: 409, code, detail: "private provider detail" }));
    renderCard(); await tick();
    screen.getByRole("link", { name: "Authorize GitHub (opens in a new tab)" }).focus();
    await tick(5000); await tick(20000);
    expect(api.pollDefaultGitHubConnection).toHaveBeenCalledTimes(1);
    expect(sessionStorage.getItem("rig-github-authorization")).toBeNull();
    expect(screen.queryByRole("button", { name: "Retry authorization check" })).toBeNull();
    expect(document.activeElement).toBe(screen.getByRole("button", { name: "Reconnect" }));
    expect(document.body.textContent).not.toContain("private provider detail");
    if (code === "authorization_identity_mismatch") expect(screen.getByRole("alert").textContent).toContain("Sign in with the GitHub account already connected to Rig.");
  });
  it("ignores an authorization start that finishes after its card is unmounted", async () => {
    const started = deferred<Awaited<ReturnType<typeof api.startDefaultGitHubConnection>>>();
    vi.mocked(api.startDefaultGitHubConnection).mockReturnValueOnce(started.promise);
    vi.mocked(api.defaultSourceConnection).mockResolvedValue({ configured: true, connection: { ...connection, status: "access_lost" } });
    const view = renderCard(); await screen.findByText("Reconnect GitHub");
    fireEvent.click(screen.getByRole("button", { name: "Reconnect" }));
    view.unmount();
    await act(async () => started.resolve(authorization()));
    expect(sessionStorage.getItem("rig-github-authorization")).toBeNull();
    expect(api.pollDefaultGitHubConnection).not.toHaveBeenCalled();
  });
  it("requires the explicit disconnect action and restores focus when cancelled", async () => {
    vi.spyOn(api, "disconnectSourceConnection").mockResolvedValue();
    renderCard(); await screen.findByText("Connected as @octocat");
    const disconnect = screen.getByRole("button", { name: "Disconnect" });
    disconnect.focus(); fireEvent.click(disconnect);
    await screen.findByRole("dialog", { name: "Disconnect GitHub?" });
    fireEvent.click(screen.getByRole("button", { name: "Keep connected" }));
    expect(api.disconnectSourceConnection).not.toHaveBeenCalled();
    expect(document.activeElement).toBe(disconnect);
    fireEvent.click(disconnect);
    fireEvent.click(screen.getByRole("button", { name: "Disconnect GitHub" }));
    await waitFor(() => expect(api.disconnectSourceConnection).toHaveBeenCalledWith(connection.id));
  });

  it("keeps the pending disconnect dialog until refetch completes and then focuses enabled Connect", async () => {
    const refreshed = deferred<Awaited<ReturnType<typeof api.defaultSourceConnection>>>();
    vi.spyOn(api, "disconnectSourceConnection").mockResolvedValue();
    renderCard(); await screen.findByText("Connected as @octocat");
    vi.mocked(api.defaultSourceConnection).mockReturnValueOnce(refreshed.promise);
    fireEvent.click(screen.getByRole("button", { name: "Disconnect" }));
    fireEvent.click(screen.getByRole("button", { name: "Disconnect GitHub" }));
    await waitFor(() => expect(api.defaultSourceConnection).toHaveBeenCalledTimes(2));
    expect(screen.getByRole("dialog", { name: "Disconnect GitHub?" })).toBeTruthy();
    expect((screen.getByRole("button", { name: "Disconnecting…" }) as HTMLButtonElement).disabled).toBe(true);
    await act(async () => refreshed.resolve({ configured: true, connection: { ...connection, status: "disconnected" } }));
    await waitFor(() => {
      const connect = screen.getByRole("button", { name: "Connect GitHub" }) as HTMLButtonElement;
      expect(connect.disabled).toBe(false);
      expect(document.activeElement).toBe(connect);
    });
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("moves retry focus to a stable status while waiting and then to repository access", async () => {
    vi.useFakeTimers();
    sessionStorage.setItem("rig-github-authorization", JSON.stringify(authorization()));
    const polled = deferred<Awaited<ReturnType<typeof api.pollDefaultGitHubConnection>>>();
    vi.mocked(api.pollDefaultGitHubConnection).mockRejectedValueOnce(new Error("unavailable")).mockReturnValueOnce(polled.promise);
    renderCard(); await tick(); await tick(5000);
    const retry = screen.getByRole("button", { name: "Retry authorization check" });
    retry.focus(); fireEvent.click(retry); await tick();
    const status = screen.getByText("ABCD-EFGH").closest("[role='status']");
    expect(document.activeElement).toBe(status);
    await act(async () => polled.resolve({ authorizationId: "b".repeat(32), status: "connected", connection }));
    await tick();
    expect(document.activeElement).toBe(screen.getByRole("link", { name: /Manage repository access/ }));
  });

  it("renders a disconnect failure only in the open dialog", async () => {
    vi.spyOn(api, "disconnectSourceConnection").mockRejectedValue(new Error("unavailable"));
    renderCard(); await screen.findByText("Connected as @octocat");
    fireEvent.click(screen.getByRole("button", { name: "Disconnect" }));
    fireEvent.click(screen.getByRole("button", { name: "Disconnect GitHub" }));
    const alert = await screen.findByRole("alert");
    expect(alert.closest("[role='dialog']")).toBeTruthy();
    expect(document.querySelectorAll("[role='alert']")).toHaveLength(1);
  });
});

function PickerHarness() {
  const [selected, setSelected] = useState<ConnectedGitHubRepository | null>(null);
  return <><GitHubRepositoryPicker id="repository" value={selected} onChange={setSelected}/><output>{selected ? `${selected.installationId}/${selected.id}` : "No selection"}</output></>;
}
function renderPicker() { const client = new QueryClient({ defaultOptions: { queries: { retry: false } } }); return render(<QueryClientProvider client={client}><PickerHarness/></QueryClientProvider>); }
describe("GitHubRepositoryPicker", () => {
  it("selects repositories across accounts and carries the hidden installation identity", async () => {
    vi.mocked(api.defaultGitHubRepositories).mockResolvedValue({ ...page, totalCount: 2, items: [repository, { ...repository, id: 10, installationId: 8, owner: "octocat", name: "personal" }] });
    renderPicker(); await screen.findByRole("option", { name: "octocat/personal (private)" });
    expect(screen.queryByLabelText("GitHub App installation")).toBeNull();
    fireEvent.change(screen.getByLabelText("Repository"), { target: { value: `${connection.id}:8:10` } });
    expect(screen.getByText("8/10")).toBeTruthy();
  });
  it("submits keyboard search without submitting the surrounding application form", async () => {
    renderPicker(); await screen.findByRole("option", { name: "octo-org/web (private)" });
    const search = screen.getByLabelText("Search repositories");
    fireEvent.change(search, { target: { value: "web app" } });
    fireEvent.keyDown(search, { key: "Enter" });
    await waitFor(() => expect(api.defaultGitHubRepositories).toHaveBeenLastCalledWith("web app", 1, 30));
  });
  it("blocks Enter during an in-flight search while still preventing application submission", async () => {
    const results = deferred<typeof page>();
    vi.mocked(api.defaultGitHubRepositories).mockReturnValueOnce(results.promise);
    const changed = vi.fn();
    const submitted = vi.fn();
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(<QueryClientProvider client={client}><form onSubmit={submitted}><GitHubRepositoryPicker id="repository" value={repository} onChange={changed}/></form></QueryClientProvider>);
    const search = screen.getByLabelText("Search repositories");
    fireEvent.change(search, { target: { value: "second" } });
    const enter = new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true });
    fireEvent(search, enter);
    expect(enter.defaultPrevented).toBe(true);
    expect((screen.getByRole("button", { name: "Search" }) as HTMLButtonElement).disabled).toBe(true);
    expect(changed).not.toHaveBeenCalled();
    expect(submitted).not.toHaveBeenCalled();
    expect(api.defaultGitHubRepositories).toHaveBeenCalledTimes(1);
    await act(async () => results.resolve(page));
    await waitFor(() => expect((screen.getByRole("button", { name: "Search" }) as HTMLButtonElement).disabled).toBe(false));
    fireEvent.keyDown(search, { key: "Enter" });
    await waitFor(() => expect(api.defaultGitHubRepositories).toHaveBeenLastCalledWith("second", 1, 30));
    expect(changed).toHaveBeenCalledOnce();
    expect(submitted).not.toHaveBeenCalled();
  });
  it("preserves failure state and Retry instead of claiming no repositories exist", async () => {
    vi.mocked(api.defaultGitHubRepositories).mockRejectedValueOnce(new Error("provider credentials"));
    renderPicker(); await screen.findByRole("alert");
    expect(screen.queryByText("No repositories found")).toBeNull();
    expect(document.body.textContent).not.toContain("provider credentials");
    fireEvent.click(screen.getByRole("button", { name: "Retry repositories" }));
    await screen.findByRole("option", { name: "octo-org/web (private)" });
  });
  it("shows repository-access guidance for an empty result", async () => {
    vi.mocked(api.defaultGitHubRepositories).mockResolvedValue({ ...page, totalCount: 0, items: [] });
    renderPicker(); await screen.findByText("No repositories found");
    expect(screen.getByText(/Use Manage repository access/)).toBeTruthy();
    const manage = screen.getByRole("link", { name: "Manage repository access (opens in a new tab)" });
    expect(manage.getAttribute("href")).toBe("/connections");
    expect(manage.getAttribute("target")).toBe("_blank");
  });
  it("clears selection on pagination and preserves the focused next-page button while loading", async () => {
    const next = deferred<typeof page>();
    vi.mocked(api.defaultGitHubRepositories).mockResolvedValueOnce({ ...page, totalCount: 31 }).mockReturnValueOnce(next.promise);
    renderPicker(); await screen.findByRole("option", { name: "octo-org/web (private)" });
    fireEvent.change(screen.getByLabelText("Repository"), { target: { value: `${connection.id}:7:9` } });
    const button = screen.getByRole("button", { name: "Next repositories page" });
    button.focus(); fireEvent.click(button);
    expect(screen.getByText("No selection")).toBeTruthy();
    expect(button.getAttribute("aria-disabled")).toBe("true");
    expect(document.activeElement).toBe(button);
    await act(async () => next.resolve({ ...page, page: 2, totalCount: 31 }));
    expect(document.activeElement).toBe(button);
  });
});
