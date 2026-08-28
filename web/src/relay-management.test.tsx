import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { readFileSync } from "node:fs";
import { StrictMode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { APIError, api } from "./api";
import { githubAuthorizationURL, relayPollLimitReached, relayRetryDelay, RelayManagementPanel, RELAY_POLL_INTERVAL_MS, RELAY_POLL_MAX_ATTEMPTS, RELAY_POLL_MAX_DURATION_MS } from "./relay-management";

const connectionId = "a".repeat(32);
const bindingId = "11111111-1111-4111-8111-111111111111";
const enrollmentId = "22222222-2222-4222-8222-222222222222";
const rotationId = "33333333-3333-4333-8333-333333333333";
const controllerId = "44444444-4444-4444-8444-444444444444";
const timestamp = "2026-08-27T12:00:00Z";
const future = "2099-08-27T12:05:00Z";
const oauthState = "A".repeat(43);
const alternateOAuthState = "E".repeat(43);
const workerOAuthState = "I".repeat(43);
const hostileDetailFragments = ["gho_remoteTokenValue", "https://attacker.example/provider-body", bindingId, controllerId, rotationId, "provider-private-body"];
const hostileDetail = hostileDetailFragments.join(" | ");

function hostileAPIError(code = "unknown_remote_failure") {
  return new APIError({ status: 502, code, detail: hostileDetail });
}

function expectHostileDetailHidden() {
  const rendered = document.body.textContent ?? "";
  for (const fragment of hostileDetailFragments) expect(rendered).not.toContain(fragment);
}

function authorizationURL(repositoryId = 9, state = oauthState, overrides: Record<string, string> = {}) {
  const query = new URLSearchParams({
    client_id: "client",
    code_challenge: "A".repeat(43),
    code_challenge_method: "S256",
    redirect_uri: "https://relay.example/v1/github/callback",
    repository_id: String(repositoryId),
    state,
    ...overrides,
  });
  query.sort();
  return `https://github.com/login/oauth/authorize?${query.toString()}`;
}

function mutatedAuthorizationURL(mutate: (query: URLSearchParams) => void, repositoryId = 9) {
  const parsed = new URL(authorizationURL(repositoryId));
  mutate(parsed.searchParams);
  parsed.searchParams.sort();
  return parsed.href;
}

const relayStatus = {
  availability: "available", state: "ready", paused: false, outcome: "ready", diagnosticsUnavailable: false,
  pendingCommands: 0, activeLeases: 0, expiredLeases: 0, oldestPendingAgeSeconds: 0, observerDropped: 0,
  readModelAvailable: true, removableBindings: [], keyRotation: { inProgress: false },
};
const connection = {
  id: connectionId, provider: "github", status: "connected", providerLogin: "octocat", credentialGeneration: 1,
  connectedAt: timestamp, createdAt: timestamp, updatedAt: timestamp,
};
const installation = {
  id: 7, accountLogin: "octocat", accountType: "User", targetType: "User", repositorySelection: "selected", cachedAt: timestamp,
};
const repository = {
  id: 9, owner: "octocat", name: "service", defaultBranch: "main", private: true, archived: false, disabled: false,
};
const secondRepository = {
  id: 10, owner: "octocat", name: "worker", defaultBranch: "main", private: false, archived: false, disabled: false,
};

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: Error) => void;
  const promise = new Promise<T>((done, fail) => { resolve = done; reject = fail; });
  return { promise, resolve, reject };
}

function mockData() {
  vi.spyOn(api, "relayStatus").mockResolvedValue(relayStatus as never);
  vi.spyOn(api, "sourceConnections").mockResolvedValue({ items: [connection] } as never);
  vi.spyOn(api, "githubInstallations").mockResolvedValue({ page: 1, perPage: 30, totalCount: 1, items: [installation] } as never);
  vi.spyOn(api, "githubRepositories").mockResolvedValue({ page: 1, perPage: 30, totalCount: 1, items: [repository] } as never);
  vi.spyOn(api, "startRelayEnrollment").mockResolvedValue({ enrollmentId, authorizationUrl: authorizationURL(), status: "pending", expiresAt: future } as never);
  vi.spyOn(api, "pollRelayEnrollment").mockResolvedValue({ enrollmentId, status: "pending", createdAt: timestamp, expiresAt: future, updatedAt: timestamp } as never);
  vi.spyOn(api, "removeRelayBinding").mockResolvedValue({ bindingId, state: "removed", updatedAt: timestamp } as never);
  vi.spyOn(api, "startRelayKeyRotation").mockResolvedValue({ rotationId, state: "prepare", expiresAt: future } as never);
}

function renderPanel(
  role = "administrator",
  polling?: { intervalMs?: number; maxAttempts?: number; maxDurationMs?: number },
  strict = false,
) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const panel = <RelayManagementPanel role={role} polling={polling}/>;
  return render(<QueryClientProvider client={client}>{strict ? <StrictMode>{panel}</StrictMode> : panel}</QueryClientProvider>);
}

async function chooseRepository() {
  fireEvent.change(await screen.findByLabelText("GitHub connection"), { target: { value: connectionId } });
  await screen.findByRole("option", { name: "octocat (selected)" });
  fireEvent.change(screen.getByLabelText("GitHub App installation"), { target: { value: "7" } });
  await screen.findByRole("option", { name: "octocat/service (private)" });
  fireEvent.change(screen.getByLabelText("Repository"), { target: { value: "9" } });
}

async function startRotation() {
  const rotate = await screen.findByRole("button", { name: "Rotate controller key" }) as HTMLButtonElement;
  await waitFor(() => expect(rotate.disabled).toBe(false));
  fireEvent.click(rotate);
}

describe("RelayManagementPanel", () => {
  beforeEach(() => { vi.restoreAllMocks(); mockData(); });
  afterEach(() => { vi.useRealTimers(); cleanup(); });

  it("renders loading, empty, diagnostics, and administrator-safe controls without identifiers", async () => {
    const pending = deferred<typeof relayStatus>();
    vi.mocked(api.relayStatus).mockReturnValue(pending.promise as never);
    const view = renderPanel();
    expect(screen.getByText("Loading relay status")).not.toBeNull();
    await act(async () => pending.resolve({ ...relayStatus, diagnosticsUnavailable: true }));
    expect(await screen.findByText("Relay diagnostics unavailable")).not.toBeNull();
    expect(screen.getByText("No removable relay bindings.")).not.toBeNull();
    expect(screen.getByRole("button", { name: "Rotate controller key" })).not.toBeNull();
    expect(view.container.textContent).not.toContain(bindingId);
  });

  it.each([
    [{ ...relayStatus, availability: "initializing" }, "Relay is initializing"],
    [{ ...relayStatus, availability: "unavailable" }, "Relay unavailable"],
    [{ ...relayStatus, readModelAvailable: false }, "Relay management data unavailable"],
  ])("separates lifecycle and read-model state", async (response, expected) => {
    vi.mocked(api.relayStatus).mockResolvedValue(response as never);
    renderPanel();
    expect(await screen.findByText(expected)).not.toBeNull();
    expect((screen.getByRole("button", { name: "Rotate controller key" }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("surfaces paused delivery, dropped observations, and humanized durable states", async () => {
    vi.mocked(api.relayStatus).mockResolvedValue({ ...relayStatus, paused: true, state: "source_paused", outcome: "access_lost", observerDropped: 3 } as never);
    renderPanel();
    const paused = await screen.findByText("Relay delivery is paused");
    expect(paused.closest("[role='status']")?.classList.contains("warning")).toBe(true);
    expect(screen.getByText("source paused")).not.toBeNull();
    expect(screen.getByText("access lost")).not.toBeNull();
    expect(screen.getByText("Dropped observations").nextElementSibling?.textContent).toBe("3");
  });

  it("does not expose an already-expired authorization URL and can start again", async () => {
    vi.mocked(api.startRelayEnrollment)
      .mockResolvedValueOnce({ enrollmentId, authorizationUrl: authorizationURL(), status: "pending", expiresAt: "2000-01-01T00:00:00Z" } as never)
      .mockResolvedValueOnce({ enrollmentId, authorizationUrl: authorizationURL(9, alternateOAuthState), status: "pending", expiresAt: future } as never);
    renderPanel();
    await chooseRepository();
    fireEvent.click(screen.getByRole("button", { name: "Start relay authorization" }));
    expect((await screen.findByRole("alert")).textContent).toContain("expired before it could be opened");
    expect(screen.queryByRole("link", { name: /Open GitHub authorization/ })).toBeNull();
    expect((screen.getByRole("button", { name: "Start relay authorization" }) as HTMLButtonElement).disabled).toBe(true);
    fireEvent.click(screen.getByRole("button", { name: "Start again" }));
    expect((await screen.findByRole("link", { name: /Open GitHub authorization/ })).getAttribute("href")).toBe(authorizationURL(9, alternateOAuthState));
    expect(api.startRelayEnrollment).toHaveBeenCalledTimes(2);
  });

  it("ends an enrollment after the actual attempt ceiling and starts a replacement session", async () => {
    vi.mocked(api.startRelayEnrollment)
      .mockResolvedValueOnce({ enrollmentId, authorizationUrl: authorizationURL(), status: "pending", expiresAt: future } as never)
      .mockResolvedValueOnce({ enrollmentId: "44444444-4444-4444-8444-444444444444", authorizationUrl: authorizationURL(9, alternateOAuthState), status: "pending", expiresAt: future } as never);
    renderPanel("administrator", { intervalMs: 40, maxAttempts: 1, maxDurationMs: 1_000 });
    await chooseRepository();
    fireEvent.click(screen.getByRole("button", { name: "Start relay authorization" }));
    expect(await screen.findByRole("link", { name: /Open GitHub authorization/ })).not.toBeNull();
    const alert = await screen.findByRole("alert", {}, { timeout: 1_000 });
    expect(alert.textContent).toContain("polling limit");
    expect(api.pollRelayEnrollment).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole("link", { name: /Open GitHub authorization/ })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Start again" }));
    await waitFor(() => expect(api.startRelayEnrollment).toHaveBeenCalledTimes(2));
    expect((await screen.findByRole("link", { name: /Open GitHub authorization/ })).getAttribute("href")).toBe(authorizationURL(9, alternateOAuthState));
  });

  it("ends an enrollment when its actual duration window elapses before polling", async () => {
    renderPanel("administrator", { intervalMs: 30, maxAttempts: 99, maxDurationMs: 1 });
    await chooseRepository();
    fireEvent.click(screen.getByRole("button", { name: "Start relay authorization" }));
    const alert = await screen.findByRole("alert", {}, { timeout: 1_000 });
    expect(alert.textContent).toContain("polling limit");
    expect(api.pollRelayEnrollment).not.toHaveBeenCalled();
    expect(screen.queryByRole("link", { name: /Open GitHub authorization/ })).toBeNull();
    expect(screen.getByRole("button", { name: "Start again" })).not.toBeNull();
  });

  it("removes an active authorization link when the enrollment expires", async () => {
    vi.mocked(api.startRelayEnrollment).mockResolvedValue({
      enrollmentId,
      authorizationUrl: authorizationURL(),
      status: "pending",
      expiresAt: future,
    } as never);
    renderPanel("administrator", { intervalMs: 20, maxAttempts: 99, maxDurationMs: RELAY_POLL_MAX_DURATION_MS });
    await chooseRepository();
    fireEvent.click(screen.getByRole("button", { name: "Start relay authorization" }));
    expect(await screen.findByRole("link", { name: /Open GitHub authorization/ })).not.toBeNull();
    vi.spyOn(Date, "now").mockReturnValue(Date.parse(future) + 1);
    const alert = await screen.findByRole("alert", {}, { timeout: 1_000 });
    expect(alert.textContent).toContain("expired");
    expect(api.pollRelayEnrollment).not.toHaveBeenCalled();
    expect(screen.queryByRole("link", { name: /Open GitHub authorization/ })).toBeNull();
    expect(screen.getByRole("button", { name: "Start again" })).not.toBeNull();
  });

  it("removes the authorization link immediately when a pending poll reports expiry", async () => {
    vi.mocked(api.pollRelayEnrollment).mockResolvedValue({
      enrollmentId,
      status: "pending",
      createdAt: timestamp,
      expiresAt: "2000-01-01T00:00:00Z",
      updatedAt: timestamp,
    } as never);
    renderPanel("administrator", { intervalMs: 50 });
    await chooseRepository();
    fireEvent.click(screen.getByRole("button", { name: "Start relay authorization" }));
    expect(await screen.findByRole("link", { name: /Open GitHub authorization/ })).not.toBeNull();
    const alert = await screen.findByRole("alert", {}, { timeout: 1_000 });
    expect(alert.textContent).toContain("expired");
    expect(api.pollRelayEnrollment).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole("link", { name: /Open GitHub authorization/ })).toBeNull();
    expect(screen.getByRole("button", { name: "Start again" })).not.toBeNull();
  });

  it("rejects malformed status and invalid timestamps with a generic unsupported response", async () => {
    vi.mocked(api.relayStatus).mockResolvedValue({ ...relayStatus, removableBindings: [{ bindingId, connectionId, installationId: 7, repositoryId: 9, state: "authorized", updatedAt: "not-a-date" }] } as never);
    renderPanel();
    expect((await screen.findByRole("alert")).textContent).toContain("unsupported relay response");
    expect(screen.queryByText(connectionId)).toBeNull();
  });

  it.each([
    ["relay_unavailable", "The relay is unavailable. Check relay status and try again."],
    ["provider_unavailable", "GitHub is temporarily unavailable. Try again."],
    ["source_access_lost", "GitHub access was lost. Reconnect or reauthorize the source, refresh relay status, and try again."],
    ["relay_state_conflict", "Relay state changed before the operation completed. Refresh relay status and try again."],
  ])("maps known remote error code %s to fixed actionable feedback", async (code, expected) => {
    vi.mocked(api.relayStatus).mockRejectedValue(hostileAPIError(code));
    renderPanel();
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain(expected);
    expectHostileDetailHidden();
  });

  it("uses a generic status fallback for an unknown remote code without rendering its detail", async () => {
    vi.mocked(api.relayStatus).mockRejectedValue(hostileAPIError());
    renderPanel();
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("Relay status could not be loaded.");
    expect(alert.textContent).not.toContain("unknown_remote_failure");
    expectHostileDetailHidden();
  });

  it.each([
    ["connections", "GitHub connections could not be loaded."],
    ["installations", "GitHub App installations could not be loaded."],
    ["repositories", "Repositories could not be loaded."],
  ])("does not render hostile remote detail from the %s query", async (operation, expected) => {
    if (operation === "connections") vi.mocked(api.sourceConnections).mockRejectedValue(hostileAPIError());
    if (operation === "installations") vi.mocked(api.githubInstallations).mockRejectedValue(hostileAPIError());
    if (operation === "repositories") vi.mocked(api.githubRepositories).mockRejectedValue(hostileAPIError());
    renderPanel();
    if (operation !== "connections") {
      fireEvent.change(await screen.findByLabelText("GitHub connection"), { target: { value: connectionId } });
    }
    if (operation === "repositories") {
      await screen.findByRole("option", { name: "octocat (selected)" });
      fireEvent.change(screen.getByLabelText("GitHub App installation"), { target: { value: "7" } });
    }
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain(expected);
    expectHostileDetailHidden();
  });

  it("paginates installations and resets dependent selectors", async () => {
    vi.mocked(api.githubInstallations).mockImplementation(async (_connection, page) => ({ page, perPage: 30, totalCount: 31, items: [{ ...installation, id: page === 1 ? 7 : 8 }] }) as never);
    renderPanel();
    fireEvent.change(await screen.findByLabelText("GitHub connection"), { target: { value: connectionId } });
    await screen.findByRole("option", { name: "octocat (selected)" });
    const installationSelect = screen.getByLabelText("GitHub App installation") as HTMLSelectElement;
    fireEvent.change(installationSelect, { target: { value: "7" } });
    await screen.findByLabelText("Repository");
    fireEvent.click(within(screen.getByRole("navigation", { name: "installations pagination" })).getByRole("button", { name: "Next" }));
    await waitFor(() => expect(api.githubInstallations).toHaveBeenLastCalledWith(connectionId, 2, 30));
    await waitFor(() => expect(screen.getByRole("status", { name: "" }).textContent).toContain("1 GitHub App installation is available on page 2"));
    expect(document.querySelectorAll(".relay-source-status")).toHaveLength(1);
    expect((screen.getByLabelText("GitHub App installation") as HTMLSelectElement).value).toBe("");
    expect(screen.queryByLabelText("Repository")).toBeNull();
  });

  it("uses one scoped source announcement and exposes source failures as alerts", async () => {
    vi.mocked(api.githubInstallations).mockRejectedValue(new Error("installations offline"));
    renderPanel();
    const sourceRegion = await screen.findByText(/connected GitHub source is available/);
    expect(sourceRegion.getAttribute("role")).toBe("status");
    expect(sourceRegion.getAttribute("aria-live")).toBe("polite");
    expect(document.querySelectorAll(".relay-source-status")).toHaveLength(1);
    fireEvent.change(screen.getByLabelText("GitHub connection"), { target: { value: connectionId } });
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("GitHub App installations could not be loaded.");
    expect(alert.textContent).not.toContain("installations offline");
    expect(document.querySelectorAll(".relay-source-status")).toHaveLength(1);
  });

  it("keeps source loading and empty results in the same polite region", async () => {
    const sources = deferred<unknown>();
    vi.mocked(api.sourceConnections).mockReturnValue(sources.promise as never);
    renderPanel();
    const region = await screen.findByText("Loading connected GitHub sources…");
    expect(region.classList.contains("relay-source-status")).toBe(true);
    expect(region.getAttribute("aria-live")).toBe("polite");
    expect(document.querySelectorAll(".relay-source-status")).toHaveLength(1);
    await act(async () => sources.resolve({ items: [] }));
    await waitFor(() => expect(region.textContent).toContain("No connected GitHub sources are available."));
    expect(document.querySelectorAll(".relay-source-status")).toHaveLength(1);
  });

  it("starts enrollment only for an exact source and exposes only a canonical HTTPS link", async () => {
    renderPanel();
    await chooseRepository();
    fireEvent.click(screen.getByRole("button", { name: "Start relay authorization" }));
    await waitFor(() => expect(api.startRelayEnrollment).toHaveBeenCalledWith({ connectionId, installationId: 7, repositoryId: 9 }));
    const link = await screen.findByRole("link", { name: /Open GitHub authorization/ });
    expect(link.getAttribute("href")).toBe(authorizationURL());
    expect(link.getAttribute("target")).toBe("_blank");
    expect(link.getAttribute("rel")).toBe("noopener noreferrer");
    expect(screen.getByText(/Reloading this page cannot rediscover/)).not.toBeNull();
    expect(document.body.textContent).not.toContain(enrollmentId);
  });

  it("keeps request fences usable after the development StrictMode effect cycle", async () => {
    renderPanel("administrator", { intervalMs: 500 }, true);
    await chooseRepository();
    fireEvent.click(screen.getByRole("button", { name: "Start relay authorization" }));
    expect(await screen.findByRole("link", { name: /Open GitHub authorization/ })).not.toBeNull();
    expect(api.startRelayEnrollment).toHaveBeenCalledTimes(1);
  });

  it("discards a delayed enrollment start when its selected repository changes", async () => {
    const firstStart = deferred<unknown>();
    vi.mocked(api.githubRepositories).mockResolvedValue({ page: 1, perPage: 30, totalCount: 2, items: [repository, secondRepository] } as never);
    vi.mocked(api.startRelayEnrollment)
      .mockReturnValueOnce(firstStart.promise as never)
      .mockResolvedValueOnce({
        enrollmentId: "44444444-4444-4444-8444-444444444444",
        authorizationUrl: authorizationURL(10, workerOAuthState),
        status: "pending",
        expiresAt: future,
      } as never);
    renderPanel("administrator", { intervalMs: 500 });
    await chooseRepository();
    fireEvent.click(screen.getByRole("button", { name: "Start relay authorization" }));
    await waitFor(() => expect(api.startRelayEnrollment).toHaveBeenCalledTimes(1));

    fireEvent.change(screen.getByLabelText("Repository"), { target: { value: "10" } });
    await waitFor(() => expect((screen.getByRole("button", { name: "Start relay authorization" }) as HTMLButtonElement).disabled).toBe(false));
    fireEvent.click(screen.getByRole("button", { name: "Start relay authorization" }));
    const currentLink = await screen.findByRole("link", { name: /Open GitHub authorization/ });
    expect(currentLink.getAttribute("href")).toBe(authorizationURL(10, workerOAuthState));
    expect(screen.getByText("octocat/worker (10)")).not.toBeNull();
    expect((screen.getByLabelText("Repository") as HTMLSelectElement).disabled).toBe(true);

    await act(async () => firstStart.resolve({
      enrollmentId,
      authorizationUrl: authorizationURL(),
      status: "pending",
      expiresAt: future,
    }));
    expect(screen.getByRole("link", { name: /Open GitHub authorization/ }).getAttribute("href")).toBe(authorizationURL(10, workerOAuthState));
    expect(screen.queryByText("octocat/service (9)")).toBeNull();
  });

  it("rejects non-canonical enrollment URLs without rendering a link", async () => {
    vi.mocked(api.startRelayEnrollment).mockResolvedValue({ enrollmentId, authorizationUrl: authorizationURL().replace("https://", "http://"), status: "pending", expiresAt: future } as never);
    renderPanel();
    await chooseRepository();
    fireEvent.click(screen.getByRole("button", { name: "Start relay authorization" }));
    expect((await screen.findByRole("alert")).textContent).toContain("unsupported relay response");
    expect(screen.queryByRole("link", { name: /Open GitHub authorization/ })).toBeNull();
  });

  it("never renders a hostile relay authorization origin or exposes it in recovery feedback", async () => {
    vi.mocked(api.startRelayEnrollment).mockResolvedValue({ enrollmentId, authorizationUrl: "https://attacker.example/login/oauth/authorize?state=safe", status: "pending", expiresAt: future } as never);
    renderPanel();
    await chooseRepository();
    fireEvent.click(screen.getByRole("button", { name: "Start relay authorization" }));
    expect((await screen.findByRole("alert")).textContent).toContain("unsupported relay response");
    expect(screen.queryByRole("link", { name: /Open GitHub authorization/ })).toBeNull();
    expect(document.body.textContent).not.toContain("attacker.example");
    expect(screen.getByRole("button", { name: "Start again" })).not.toBeNull();
  });

  it.each([
    ["mismatched repository", authorizationURL(10)],
    ["added scope", mutatedAuthorizationURL((query) => query.set("scope", "repo"))],
    ["duplicate client", mutatedAuthorizationURL((query) => query.append("client_id", "client"))],
    ["malformed challenge", authorizationURL(9, oauthState, { code_challenge: "short" })],
  ])("never renders or leaks a GitHub authorization URL with %s", async (_case, hostileURL) => {
    vi.mocked(api.startRelayEnrollment).mockResolvedValue({ enrollmentId, authorizationUrl: hostileURL, status: "pending", expiresAt: future } as never);
    renderPanel();
    await chooseRepository();
    fireEvent.click(screen.getByRole("button", { name: "Start relay authorization" }));
    expect((await screen.findByRole("alert")).textContent).toContain("unsupported relay response");
    expect(screen.queryByRole("link", { name: /Open GitHub authorization/ })).toBeNull();
    expect(document.body.textContent).not.toContain(hostileURL);
  });

  it("polls a pending enrollment to a terminal authorized state and refreshes durable status", async () => {
    vi.mocked(api.pollRelayEnrollment).mockResolvedValue({ enrollmentId, bindingId, status: "authorized", createdAt: timestamp, expiresAt: future, updatedAt: timestamp, completedAt: timestamp } as never);
    renderPanel("administrator", { intervalMs: 10 });
    await chooseRepository();
    fireEvent.click(screen.getByRole("button", { name: "Start relay authorization" }));
    await waitFor(() => expect(api.pollRelayEnrollment).toHaveBeenCalledWith(enrollmentId));
    const notice = (await screen.findByText("Relay binding authorized")).closest("[role='status']")!;
    expect(notice.classList.contains("success")).toBe(true);
    expect(notice.textContent).toContain("Repository event delivery is authorized.");
    expect(api.relayStatus).toHaveBeenCalledTimes(2);
  });

  it.each([
    ["denied", "GitHub authorization denied", "Confirm the account and repository access, then start again."],
    ["expired", "GitHub authorization expired", "Start again to create a new authorization request."],
  ])("renders terminal %s authorization as an actionable warning, never success", async (terminal, title, guidance) => {
    vi.mocked(api.pollRelayEnrollment).mockResolvedValue({ enrollmentId, status: terminal, createdAt: timestamp, expiresAt: future, updatedAt: timestamp, completedAt: timestamp } as never);
    renderPanel("administrator", { intervalMs: 10 });
    await chooseRepository();
    fireEvent.click(screen.getByRole("button", { name: "Start relay authorization" }));
    const notice = (await screen.findByText(title)).closest<HTMLElement>("[role='status']")!;
    expect(notice.classList.contains("warning")).toBe(true);
    expect(notice.classList.contains("success")).toBe(false);
    expect(notice.textContent).toContain(guidance);
    expect(within(notice).getByRole("button", { name: "Start again" })).not.toBeNull();
  });

  it("renders terminal failed authorization as a focused generic alert with a retry action", async () => {
    vi.mocked(api.pollRelayEnrollment).mockResolvedValue({ enrollmentId, status: "failed", createdAt: timestamp, expiresAt: future, updatedAt: timestamp, completedAt: timestamp } as never);
    renderPanel("administrator", { intervalMs: 10 });
    await chooseRepository();
    fireEvent.click(screen.getByRole("button", { name: "Start relay authorization" }));
    const alert = await screen.findByRole("alert");
    expect(alert.classList.contains("danger")).toBe(true);
    expect(alert.classList.contains("success")).toBe(false);
    expect(alert.textContent).toContain("Rig could not complete relay authorization.");
    expect(within(alert).getByRole("button", { name: "Start again" })).not.toBeNull();
    expect(alert.textContent).not.toContain(enrollmentId);
    expect(alert.textContent).not.toContain(bindingId);
    await waitFor(() => expect(document.activeElement).toBe(alert));
  });

  it("stops enrollment polling on error and resumes only after explicit retry", async () => {
    vi.mocked(api.pollRelayEnrollment).mockRejectedValue(new Error("poll offline"));
    renderPanel();
    await chooseRepository();
    fireEvent.click(screen.getByRole("button", { name: "Start relay authorization" }));
    const alert = await screen.findByRole("alert", {}, { timeout: RELAY_POLL_INTERVAL_MS + 2_000 });
    expect(alert.textContent).toContain("Could not check relay authorization.");
    expect(alert.textContent).not.toContain("poll offline");
    const calls = vi.mocked(api.pollRelayEnrollment).mock.calls.length;
    vi.useFakeTimers();
    await act(async () => { await vi.advanceTimersByTimeAsync(RELAY_POLL_INTERVAL_MS * 3); });
    expect(api.pollRelayEnrollment).toHaveBeenCalledTimes(calls);
    fireEvent.click(screen.getByRole("button", { name: "Resume authorization check" }));
    await act(async () => { await vi.advanceTimersByTimeAsync(RELAY_POLL_INTERVAL_MS); });
    await act(async () => { await Promise.resolve(); });
    expect(api.pollRelayEnrollment).toHaveBeenCalledTimes(calls + 1);
  });

  it.each([
    ["enrollment start", "Could not start relay authorization."],
    ["enrollment poll", "Could not check relay authorization."],
    ["binding removal", "Could not remove the relay binding."],
    ["key rotation", "Could not start controller key rotation."],
  ])("does not render hostile remote detail from %s", async (operation, expected) => {
    if (operation === "enrollment start") {
      vi.mocked(api.startRelayEnrollment).mockRejectedValue(hostileAPIError());
      renderPanel();
      await chooseRepository();
      fireEvent.click(screen.getByRole("button", { name: "Start relay authorization" }));
    } else if (operation === "enrollment poll") {
      vi.mocked(api.pollRelayEnrollment).mockRejectedValue(hostileAPIError());
      renderPanel("administrator", { intervalMs: 10 });
      await chooseRepository();
      fireEvent.click(screen.getByRole("button", { name: "Start relay authorization" }));
    } else if (operation === "binding removal") {
      vi.mocked(api.relayStatus).mockResolvedValue({ ...relayStatus, removableBindings: [{ bindingId, connectionId, installationId: 7, repositoryId: 9, state: "authorized", updatedAt: timestamp }] } as never);
      vi.mocked(api.removeRelayBinding).mockRejectedValue(hostileAPIError());
      renderPanel();
      fireEvent.click(await screen.findByRole("button", { name: "Remove binding" }));
      fireEvent.click(within(await screen.findByRole("dialog")).getByRole("button", { name: "Remove binding" }));
    } else {
      vi.mocked(api.startRelayKeyRotation).mockRejectedValue(hostileAPIError());
      renderPanel();
      await startRotation();
    }
    const alert = await screen.findByRole("alert", {}, { timeout: 1_000 });
    expect(alert.textContent).toContain(expected);
    expect(alert.textContent).not.toContain("unknown_remote_failure");
    expectHostileDetailHidden();
  });

  it("shows exact binding scope, hides the binding id, and restores focus after Escape", async () => {
    vi.mocked(api.relayStatus).mockResolvedValue({ ...relayStatus, removableBindings: [{ bindingId, connectionId, installationId: 7, repositoryId: 9, state: "authorized", updatedAt: timestamp }] } as never);
    const view = renderPanel();
    const open = await screen.findByRole("button", { name: "Remove binding" });
    const article = screen.getByRole("article", { name: "Repository 9" });
    expect(article.getAttribute("aria-labelledby")).toBe(within(article).getByRole("heading", { name: "Repository 9" }).id);
    open.focus();
    fireEvent.click(open);
    const dialog = await screen.findByRole("dialog", { name: "Remove relay binding" });
    const description = document.getElementById(dialog.getAttribute("aria-describedby")!);
    expect(description?.textContent).toBe("This stops relay delivery for this repository. Existing applications and releases are unchanged.");
    expect(screen.getAllByText("This stops relay delivery for this repository. Existing applications and releases are unchanged.")).toHaveLength(1);
    expect(description?.querySelector("button, dl, [role='alert']")).toBeNull();
    expect(within(dialog).getByText(connectionId)).not.toBeNull();
    expect(within(dialog).getAllByText("7").length).toBeGreaterThan(0);
    expect(within(dialog).getAllByText("9").length).toBeGreaterThan(0);
    expect(view.container.textContent).not.toContain(bindingId);
    fireEvent.keyDown(document, { key: "Escape" });
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(document.activeElement).toBe(open);
  });

  it("keeps approved owner-scoped identifiers visible and gives an access-lost binding a recovery path", async () => {
    vi.mocked(api.relayStatus).mockResolvedValue({ ...relayStatus, removableBindings: [{ bindingId, connectionId, installationId: 7, repositoryId: 9, state: "access_lost", updatedAt: timestamp }] } as never);
    const view = renderPanel();
    const article = await screen.findByRole("article", { name: "Repository 9" });
    expect(article.textContent).toContain(connectionId);
    expect(article.textContent).toContain("Installation7");
    expect(article.textContent).toContain("Repository9");
    expect(article.textContent).toContain("Reconnect or reauthorize the GitHub source, then refresh relay status before retrying.");
    expect(view.container.textContent).not.toContain(bindingId);
  });

  it("keeps a failed removal dialog open, focuses its error, and prevents duplicate submission", async () => {
    const removal = deferred<never>();
    vi.mocked(api.relayStatus).mockResolvedValue({ ...relayStatus, removableBindings: [{ bindingId, connectionId, installationId: 7, repositoryId: 9, state: "authorized", updatedAt: timestamp }] } as never);
    vi.mocked(api.removeRelayBinding).mockReturnValue(removal.promise);
    renderPanel();
    fireEvent.click(await screen.findByRole("button", { name: "Remove binding" }));
    const submit = within(await screen.findByRole("dialog")).getByRole("button", { name: "Remove binding" });
    fireEvent.click(submit); fireEvent.click(submit);
    expect(api.removeRelayBinding).toHaveBeenCalledTimes(1);
    fireEvent.keyDown(document, { key: "Escape" });
    expect(screen.getByRole("dialog")).not.toBeNull();
    await act(async () => removal.reject(new Error("removal refused")));
    const alert = await within(screen.getByRole("dialog")).findByRole("alert");
    await waitFor(() => expect(document.activeElement).toBe(alert));
  });

  it("does not let enrollment selection changes discard a successful removal refresh", async () => {
    const removal = deferred<unknown>();
    vi.mocked(api.relayStatus).mockResolvedValue({ ...relayStatus, removableBindings: [{ bindingId, connectionId, installationId: 7, repositoryId: 9, state: "authorized", updatedAt: timestamp }] } as never);
    vi.mocked(api.removeRelayBinding).mockReturnValue(removal.promise as never);
    renderPanel();
    await chooseRepository();
    fireEvent.click(screen.getByRole("button", { name: "Remove binding" }));
    fireEvent.click(within(await screen.findByRole("dialog")).getByRole("button", { name: "Remove binding" }));
    fireEvent.change(screen.getByLabelText("Repository"), { target: { value: "" } });
    await act(async () => removal.resolve({ bindingId, state: "removed", updatedAt: timestamp }));
    await waitFor(() => expect(api.relayStatus).toHaveBeenCalledTimes(2));
    expect(screen.queryByRole("dialog")).toBeNull();
    const status = screen.getAllByRole("status").filter((node) => node.textContent?.includes("Relay binding removed."));
    expect(status).toHaveLength(1);
    await waitFor(() => expect(document.activeElement).toBe(status[0]));
  });

  it("hides rotation for non-admins and prevents duplicate administrator starts", async () => {
    const rotation = deferred<never>();
    vi.mocked(api.startRelayKeyRotation).mockReturnValue(rotation.promise);
    const first = renderPanel("viewer");
    await screen.findByText("No removable relay bindings.");
    expect(screen.queryByRole("button", { name: "Rotate controller key" })).toBeNull();
    first.unmount();
    renderPanel();
    const rotate = await screen.findByRole("button", { name: "Rotate controller key" });
    expect(document.getElementById(rotate.getAttribute("aria-describedby")!)?.textContent).toBe("Rotation changes the relay authentication key without exposing key material.");
    await waitFor(() => expect((rotate as HTMLButtonElement).disabled).toBe(false));
    fireEvent.click(rotate); fireEvent.click(rotate);
    expect(api.startRelayKeyRotation).toHaveBeenCalledTimes(1);
  });

  it("keeps a pending rotation isolated from enrollment selection changes", async () => {
    const rotation = deferred<unknown>();
    vi.mocked(api.startRelayKeyRotation).mockReturnValue(rotation.promise as never);
    renderPanel();
    await chooseRepository();
    const rotate = screen.getByRole("button", { name: "Rotate controller key" });
    fireEvent.click(rotate);
    fireEvent.change(screen.getByLabelText("Repository"), { target: { value: "" } });
    fireEvent.click(rotate);
    expect(api.startRelayKeyRotation).toHaveBeenCalledTimes(1);
    await act(async () => rotation.resolve({ rotationId, state: "prepare", expiresAt: future }));
    await waitFor(() => expect(api.relayStatus).toHaveBeenCalledTimes(2));
    expect(screen.getAllByRole("status").filter((node) => node.textContent?.includes("Controller key rotation is in progress (prepare)."))).toHaveLength(1);
  });

  it.each(["prepare", "propose", "confirm", "new_key_auth", "finalize"])("maps live rotation POST state %s to neutral progress", async (state) => {
    vi.mocked(api.startRelayKeyRotation).mockResolvedValue({ rotationId, state, expiresAt: future } as never);
    renderPanel("administrator", { intervalMs: 500 });
    await startRotation();
    const status = await screen.findByText(`Controller key rotation is in progress (${state.replaceAll("_", " ")}).`);
    const callout = status.closest("[role='status']")!;
    expect(callout.classList.contains("info")).toBe(true);
    expect(callout.classList.contains("success")).toBe(false);
    expect(document.body.textContent).not.toContain(rotationId);
    await waitFor(() => expect(api.relayStatus).toHaveBeenCalledTimes(2));
  });

  it("maps an immediately completed rotation POST to success without claiming it is still active", async () => {
    vi.mocked(api.startRelayKeyRotation).mockResolvedValue({ rotationId, state: "completed", expiresAt: future } as never);
    renderPanel();
    await startRotation();
    const status = (await screen.findByText("Controller key rotation completed.")).closest("[role='status']")!;
    expect(status.classList.contains("success")).toBe(true);
    expect(status.textContent).not.toContain("in progress");
    expect(document.body.textContent).not.toContain(rotationId);
    await waitFor(() => expect(api.relayStatus).toHaveBeenCalledTimes(2));
  });

  it("maps a failed rotation POST to a focused alert without a started claim", async () => {
    vi.mocked(api.startRelayKeyRotation).mockResolvedValue({ rotationId, state: "failed", expiresAt: future } as never);
    renderPanel();
    await startRotation();
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("Controller key rotation failed.");
    expect(alert.textContent).not.toContain("started");
    expect(document.body.textContent).not.toContain(rotationId);
    await waitFor(() => expect(document.activeElement).toBe(alert));
    await waitFor(() => expect(api.relayStatus).toHaveBeenCalledTimes(2));
  });

  it("disables a duplicate rotation while the minimized read model reports progress", async () => {
    vi.mocked(api.relayStatus).mockResolvedValue({ ...relayStatus, keyRotation: { inProgress: true, state: "new_key_auth", expiresAt: future, updatedAt: timestamp } } as never);
    renderPanel();
    const rotate = await screen.findByRole("button", { name: "Rotation in progress" }) as HTMLButtonElement;
    expect(rotate.disabled).toBe(true);
    expect(screen.getAllByText("new key auth")).toHaveLength(2);
    expect(document.body.textContent).not.toContain(rotationId);
  });

  it("does not poll status while relay state is idle and stops timers on unmount", async () => {
    vi.useFakeTimers();
    const view = renderPanel();
    await act(async () => { await vi.runOnlyPendingTimersAsync(); });
    expect(api.relayStatus).toHaveBeenCalledTimes(1);
    view.unmount();
    await act(async () => { await vi.advanceTimersByTimeAsync(RELAY_POLL_INTERVAL_MS * 5); });
    expect(api.relayStatus).toHaveBeenCalledTimes(1);
  });

  it("seeds initial idle and terminal durable state and leaves unchanged refreshes silent", async () => {
    vi.mocked(api.relayStatus).mockResolvedValue({
      ...relayStatus,
      removableBindings: [{ bindingId, connectionId, installationId: 7, repositoryId: 9, state: "authorized", updatedAt: timestamp }],
      keyRotation: { inProgress: false, state: "finalize", expiresAt: future, updatedAt: timestamp },
    } as never);
    renderPanel();
    await screen.findByRole("button", { name: "Remove binding" });
    expect(screen.queryAllByRole("status").filter((node) => node.textContent?.includes("completed."))).toHaveLength(0);
    fireEvent.click(screen.getByRole("button", { name: "Refresh relay status" }));
    await waitFor(() => expect(api.relayStatus).toHaveBeenCalledTimes(2));
    expect(screen.queryAllByRole("status").filter((node) => node.textContent?.includes("completed."))).toHaveLength(0);
  });

  it("leaves an unchanged active durable state silent", async () => {
    vi.mocked(api.relayStatus).mockResolvedValue({
      ...relayStatus,
      removableBindings: [{ bindingId, connectionId, installationId: 7, repositoryId: 9, state: "removal_pending", updatedAt: timestamp }],
      keyRotation: { inProgress: true, state: "finalize", expiresAt: future, updatedAt: timestamp },
    } as never);
    renderPanel();
    await screen.findByRole("button", { name: "Rotation in progress" });
    fireEvent.click(screen.getByRole("button", { name: "Refresh relay status" }));
    await waitFor(() => expect(api.relayStatus).toHaveBeenCalledTimes(2));
    expect(screen.queryAllByRole("status").filter((node) => node.textContent?.includes("completed."))).toHaveLength(0);
  });

  it("announces an observed key rotation stop neutrally exactly once", async () => {
    const terminalStatus = { ...relayStatus, keyRotation: { inProgress: false, state: "finalize", expiresAt: future, updatedAt: timestamp } };
    vi.mocked(api.relayStatus)
      .mockResolvedValueOnce({ ...relayStatus, keyRotation: { inProgress: true, state: "finalize", expiresAt: future, updatedAt: timestamp } } as never)
      .mockResolvedValueOnce(terminalStatus as never)
      .mockResolvedValue({ ...terminalStatus, outcome: "reconciled" } as never);
    renderPanel();
    await screen.findByRole("button", { name: "Rotation in progress" });
    expect(screen.queryByText("Controller key rotation is no longer in progress.")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Refresh relay status" }));
    const transition = await screen.findByText("Controller key rotation is no longer in progress.");
    expect(transition.closest("[role='status']")?.classList.contains("info")).toBe(true);
    fireEvent.click(screen.getByRole("button", { name: "Refresh relay status" }));
    await waitFor(() => expect(api.relayStatus).toHaveBeenCalledTimes(3));
    expect(screen.getAllByRole("status").filter((node) => node.textContent?.includes("Controller key rotation is no longer in progress."))).toHaveLength(1);
  });

  it.each([
    ["disappears", [], "Relay binding removal completed.", "success"],
    ["loses access", [{ bindingId, connectionId, installationId: 7, repositoryId: 9, state: "access_lost", updatedAt: timestamp }], "Relay binding removal is no longer pending because GitHub access was lost. Reconnect or reauthorize the source, then refresh relay status before retrying.", "info"],
    ["returns authorized", [{ bindingId, connectionId, installationId: 7, repositoryId: 9, state: "authorized", updatedAt: timestamp }], "Relay binding removal is no longer pending; its authoritative state is authorized. Refresh relay status before retrying the removal.", "info"],
  ])("announces truthfully once when an observed removal-pending binding %s", async (_case, terminalBindings, expected, tone) => {
    const pendingBinding = { bindingId, connectionId, installationId: 7, repositoryId: 9, state: "removal_pending", updatedAt: timestamp };
    const terminalStatus = { ...relayStatus, removableBindings: terminalBindings };
    vi.mocked(api.relayStatus)
      .mockResolvedValueOnce({ ...relayStatus, removableBindings: [pendingBinding] } as never)
      .mockResolvedValueOnce(terminalStatus as never)
      .mockResolvedValue({ ...terminalStatus, outcome: "reconciled" } as never);
    renderPanel();
    await screen.findByRole("button", { name: "Remove binding" });
    expect(screen.queryByText(expected)).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Refresh relay status" }));
    const transition = await screen.findByText(expected);
    expect(transition.closest("[role='status']")?.classList.contains(tone)).toBe(true);
    if (tone === "info") expect(transition.closest("[role='status']")?.classList.contains("success")).toBe(false);
    fireEvent.click(screen.getByRole("button", { name: "Refresh relay status" }));
    await waitFor(() => expect(api.relayStatus).toHaveBeenCalledTimes(3));
    expect(screen.getAllByRole("status").filter((node) => node.textContent?.includes(expected))).toHaveLength(1);
  });

  it.each([
    ["initialization", { ...relayStatus, availability: "initializing" }, "The bounded status-check window ended while the relay was still initializing."],
    ["pending removal", { ...relayStatus, removableBindings: [{ bindingId, connectionId, installationId: 7, repositoryId: 9, state: "removal_pending", updatedAt: timestamp }] }, "The bounded status-check window ended while a relay binding removal remained pending."],
    ["key rotation", { ...relayStatus, keyRotation: { inProgress: true, state: "finalize", expiresAt: future, updatedAt: timestamp } }, "The bounded status-check window ended while controller key rotation remained in progress."],
  ])("states the active %s reason when bounded status checks stop and resume", async (_case, activeStatus, expected) => {
    vi.mocked(api.relayStatus).mockResolvedValue(activeStatus as never);
    renderPanel("administrator", { intervalMs: 10, maxAttempts: 1, maxDurationMs: 1_000 });
    const resume = await screen.findByRole("button", { name: "Resume status checks" }, { timeout: 1_000 });
    expect(resume.closest("[role='status']")?.textContent).toContain(expected);
    expect(api.relayStatus).toHaveBeenCalledTimes(2);
    fireEvent.click(resume);
    await waitFor(() => expect(api.relayStatus).toHaveBeenCalledTimes(3));
    expect(await screen.findByRole("button", { name: "Resume status checks" })).not.toBeNull();
  });

  it("uses a non-sensitive fallback when a requested relay operation exhausts status checks before the read model reflects it", async () => {
    vi.mocked(api.relayStatus).mockResolvedValue({ ...relayStatus, removableBindings: [{ bindingId, connectionId, installationId: 7, repositoryId: 9, state: "authorized", updatedAt: timestamp }] } as never);
    vi.mocked(api.removeRelayBinding).mockResolvedValue({ bindingId, state: "removal_pending", updatedAt: timestamp } as never);
    renderPanel("administrator", { intervalMs: 10, maxAttempts: 0, maxDurationMs: 1_000 });
    fireEvent.click(await screen.findByRole("button", { name: "Remove binding" }));
    fireEvent.click(within(await screen.findByRole("dialog")).getByRole("button", { name: "Remove binding" }));
    const resume = await screen.findByRole("button", { name: "Resume status checks" }, { timeout: 1_000 });
    expect(resume.closest("[role='status']")?.textContent).toContain("The bounded status-check window ended while an active relay operation still needed status updates.");
    expect(resume.closest("[role='status']")?.textContent).not.toContain(bindingId);
  });

  it("stops actual lifecycle polling at its ceiling and resumes in a new bounded window", async () => {
    vi.mocked(api.relayStatus).mockResolvedValue({ ...relayStatus, availability: "initializing" } as never);
    renderPanel("administrator", { intervalMs: 10, maxAttempts: 2, maxDurationMs: 1_000 });
    const resume = await screen.findByRole("button", { name: "Resume status checks" }, { timeout: 1_000 });
    expect(api.relayStatus).toHaveBeenCalledTimes(3);
    await new Promise((resolve) => window.setTimeout(resolve, 40));
    expect(api.relayStatus).toHaveBeenCalledTimes(3);
    fireEvent.click(resume);
    await waitFor(() => expect(api.relayStatus).toHaveBeenCalledTimes(5));
    expect(await screen.findByRole("button", { name: "Resume status checks" })).not.toBeNull();
  });
});

describe("githubAuthorizationURL", () => {
  it("accepts only the exact target-bound producer query", () => {
    const canonical = authorizationURL();
    expect(githubAuthorizationURL(canonical, 9)).toBe(canonical);
    for (const hostile of [
      "https://github.com/login/oauth/authorize",
      "http://github.com/login/oauth/authorize",
      "https://attacker.example/login/oauth/authorize",
      "https://github.com.attacker.example/login/oauth/authorize",
      "https://api.github.com/login/oauth/authorize",
      "https://github.com:443/login/oauth/authorize",
      "https://user:pass@github.com/login/oauth/authorize",
      "https://github.com/login/oauth/authorize#fragment",
      "https://github.com/login/oauth/access_token",
      "https://github.com/login/oauth/authorize/",
      "https://github.com/login/oauth/%61uthorize",
      "https://github.com/login/oauth/authorize?",
      "https://github.com/login/oauth/authorize?state=%73afe",
      "https://github.com/login/oauth/authorize?state=safe&client_id=abc",
      "HTTPS://github.com/login/oauth/authorize",
      "https://GITHUB.com/login/oauth/authorize",
      authorizationURL(10),
      authorizationURL(9, oauthState, { redirect_uri: "http://relay.example/v1/github/callback" }),
      authorizationURL(9, oauthState, { redirect_uri: "https://user@relay.example/v1/github/callback" }),
      authorizationURL(9, oauthState, { redirect_uri: "https://relay.example/v1/github/callback#fragment" }),
      authorizationURL(9, oauthState, { redirect_uri: "https://relay.example/v1/github/%63allback" }),
      authorizationURL(9, oauthState, { code_challenge_method: "plain" }),
      authorizationURL(9, "short"),
      authorizationURL(9, oauthState, { code_challenge: `${"A".repeat(42)}B` }),
      mutatedAuthorizationURL((query) => query.set("scope", "repo")),
      mutatedAuthorizationURL((query) => query.set("extra", "value")),
      mutatedAuthorizationURL((query) => query.append("client_id", "client")),
    ]) expect(githubAuthorizationURL(hostile, 9)).toBeNull();
  });

  it("keeps relay controls responsive, focus-visible, coarse-pointer sized, and reduced-motion safe", () => {
    const css = readFileSync("src/styles.css", "utf8");
    expect(css).toContain(".relay-enrollment-status a:focus-visible");
    expect(css).toContain(".deployment-dialog:focus-visible, .callout[tabindex=\"-1\"]:focus-visible");
    expect(css).toContain(".deployment-dialog { width: min(100%, 520px); max-height: min(90vh, 720px); overflow-y: auto;");
    expect(css).toContain("@media (max-width: 820px)");
    expect(css).toContain("@media (max-width: 560px)");
    expect(css).toContain(".relay-panel .button, .relay-panel select, .deployment-dialog .button { min-height: 44px; }");
    expect(css).toContain(".relay-enrollment-status a { display: inline-flex; align-items: center; min-height: 44px; }");
    expect(css).toContain("@media (prefers-reduced-motion: reduce)");
  });

  it("bounds Retry-After delays and both polling ceilings", () => {
    expect(relayRetryDelay(new APIError({ status: 429, code: "poll_too_soon", detail: "wait", retryAfterSeconds: 1 }))).toBe(RELAY_POLL_INTERVAL_MS);
    expect(relayRetryDelay(new APIError({ status: 429, code: "poll_too_soon", detail: "wait", retryAfterSeconds: 90 }))).toBe(30_000);
    expect(RELAY_POLL_MAX_ATTEMPTS).toBe(Math.ceil(RELAY_POLL_MAX_DURATION_MS / RELAY_POLL_INTERVAL_MS));
    expect(relayPollLimitReached(RELAY_POLL_MAX_ATTEMPTS, Date.now())).toBe(true);
    expect(relayPollLimitReached(0, 1_000, 1_000 + RELAY_POLL_MAX_DURATION_MS)).toBe(true);
    expect(relayPollLimitReached(0, 1_000, 1_001)).toBe(false);
  });
});
