import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { APIError, api, type InspectResponse, type SourceConnection } from "./api";
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
const otherConnection = {
  ...connection,
  id: "fedcba9876543210fedcba9876543210",
  providerLogin: "rig-backup",
};

function inspectionFixture(value: Omit<InspectResponse, "analysis">): InspectResponse {
  const resolvedDigest = value.resolvedSha || "a".repeat(64);
  return {
    ...value,
    analysis: {
      source: value.source,
      resolvedDigest,
      schemaVersion: "2",
      structuralFingerprint: "b".repeat(64),
      candidates: [],
      findings: [],
    },
  };
}

function pendingConnection(overrides: Partial<SourceConnection> = {}): SourceConnection {
  return {
    ...connection,
    status: "pending",
    providerLogin: "",
    pendingExpiresAt: new Date(Date.now() + 60_000).toISOString(),
    nextPollAt: new Date(Date.now() + 5_000).toISOString(),
    installUrl: "https://github.com/apps/rig/installations/new",
    ...overrides,
  };
}

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
  vi.spyOn(api, "acceptDeploymentPlan").mockResolvedValue({
    revisionId: "11111111-1111-4111-8111-111111111111",
    revisionNumber: 1,
    canonicalDigest: "f".repeat(64),
    strategy: "generated_node",
    state: "accepted",
    source: { provider: "local", repositoryId: 0, resolvedDigest: "a".repeat(64) },
    detector: { name: "projectanalysis", version: "2", sourceStructuralFingerprint: "b".repeat(64) },
    components: [],
    fieldProvenance: [],
    migration: { present: false },
  });
  vi.spyOn(api, "deploymentPlan");
  vi.spyOn(api, "approveDeploymentPlanMigration");
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, reject, resolve };
}

async function selectConnectedGitHub() {
  fireEvent.click(screen.getByLabelText(/^github repository$/i));
  const connectionSelect = await screen.findByLabelText(/^github connection$/i);
  await screen.findByRole("option", { name: /@rig-admin/i });
  fireEvent.change(connectionSelect, { target: { value: connection.id } });
}

async function selectPendingGitHub(connectionId = connection.id) {
  fireEvent.click(screen.getByLabelText(/^github repository$/i));
  const connectionSelect = await screen.findByLabelText(/^github connection$/i);
  await screen.findAllByRole("option", { name: /github connection \(pending\)/i });
  connectionSelect.focus();
  fireEvent.change(connectionSelect, { target: { value: connectionId } });
}

async function flushAsyncWork() {
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
  });
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
  fireEvent.click(screen.getByRole("button", { name: /analyze project/i }));
  const composeFile = await screen.findByLabelText(/^compose file$/i);
  fireEvent.change(composeFile, { target: { value: "compose.yaml" } });
  fireEvent.click(screen.getByRole("button", { name: /inspect selected compose file/i }));
  await screen.findByText(/source inspection completed/i);
}

function mockCleanInspection() {
  vi.mocked(api.inspect)
    .mockResolvedValueOnce(inspectionFixture({ source: { type: "github" }, resolvedSha: "abc123", composeCandidates: ["compose.yaml"], services: [], findings: [] }))
    .mockResolvedValueOnce(inspectionFixture({ source: { type: "github", composePath: "compose.yaml" }, resolvedSha: "abc123", composeCandidates: ["compose.yaml"], services: [{ name: "web" }], findings: [] }));
}

function mockLocalComposeInspection() {
  vi.mocked(api.inspect).mockResolvedValueOnce(inspectionFixture({ source: { type: "local", path: "C:/projects/local" }, composeCandidates: ["compose.yaml"], services: [{ name: "web" }], findings: [] }));
}

function generatedInspection(source: InspectResponse["source"] = { type: "local", path: "C:/projects/generated" }): InspectResponse {
  return {
    source,
    resolvedSha: source.type === "github" ? "a".repeat(40) : undefined,
    composeCandidates: [],
    services: [],
    findings: [],
    analysis: {
      source,
      resolvedDigest: source.type === "github" ? "a".repeat(40) : "a".repeat(64),
      schemaVersion: "2",
      structuralFingerprint: "b".repeat(64),
      findings: [],
      candidates: [{
        id: "candidate-web",
        origin: "inferred",
        status: "ready",
        kind: "javascript",
        rootDirectory: ".",
        configPath: "package.json",
        digest: "c".repeat(64),
        packageManager: { present: true, name: "npm", version: "11", lockfile: "package-lock.json", evidence: [] },
        nodeVersion: { present: true, value: "24", evidence: [] },
        install: { present: true, command: "npm ci", evidence: [] },
        components: [{ id: "web-12345678", origin: "inferred", name: "web", kind: "server", framework: "express", rootDirectory: ".", staticOutputDirectory: "", migrationFingerprint: "", build: { present: true, command: "npm run build", evidence: [] }, run: { present: true, command: "npm start", evidence: [] }, internalPort: { present: true, value: "3000", evidence: [] }, healthProbe: { present: true, path: "/health", evidence: [] }, evidence: [], findings: [] }],
        evidence: [],
        findings: [],
        missingFields: [],
        advancedInputs: [],
      }],
    },
  };
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
    mockLocalComposeInspection();
    const { onCreated } = renderWizard();
    fireEvent.change(screen.getByLabelText(/application name/i), { target: { value: "Local app" } });
    fireEvent.change(screen.getByLabelText(/local source path/i), { target: { value: "C:/projects/local" } });
    fireEvent.click(screen.getByRole("button", { name: /analyze project/i }));
    await screen.findByText(/source inspection completed/i);
    fireEvent.click(screen.getByRole("button", { name: /save application/i }));

    await waitFor(() => expect(api.createApp).toHaveBeenCalledWith({ name: "Local app", description: "", sourcePath: "C:/projects/local" }, expect.anything()));
    expect(onCreated).toHaveBeenCalledWith("app-1");
  });

  it("analyzes, reviews, and accepts a generated local application before saving it", async () => {
    vi.mocked(api.inspect).mockResolvedValueOnce(generatedInspection());
    const { onCreated } = renderWizard();
    fireEvent.change(screen.getByLabelText(/application name/i), { target: { value: "Generated app" } });
    fireEvent.change(screen.getByLabelText(/local source path/i), { target: { value: "C:/projects/generated" } });
    fireEvent.click(screen.getByRole("button", { name: /analyze project/i }));

    expect(await screen.findByRole("heading", { name: /how rig will run this app/i })).toBeTruthy();
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole("heading", { name: /how rig will run this app/i })));
    fireEvent.change(screen.getByRole("textbox", { name: "Run command (required)" }), { target: { value: "node server.js && echo ${READY} $()" } });
    fireEvent.click(screen.getByRole("button", { name: /accept setup/i }));

    await waitFor(() => expect(api.createApp).toHaveBeenCalledWith({ name: "Generated app", description: "", sourcePath: "C:/projects/generated" }));
    expect(api.acceptDeploymentPlan).toHaveBeenCalledWith("app-1", expect.objectContaining({
      candidateId: "candidate-web",
      expectedCandidateDigest: "c".repeat(64),
      expectedSourceStructuralFingerprint: "b".repeat(64),
      components: [expect.objectContaining({ runCommand: "node server.js && echo ${READY} $()" })],
    }));
    expect(await screen.findByRole("heading", { name: /setup accepted/i })).toBeTruthy();
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole("heading", { name: /setup accepted/i })));
    expect(screen.getByText(/analysis did not execute repository code/i)).toBeTruthy();
    expect(screen.queryByText(/runtime milestone/i)).toBeNull();
    expect(onCreated).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: /open application/i }));
    expect(onCreated).toHaveBeenCalledWith("app-1");
  });

  it("returns focus to application validation when plan acceptance is missing required details", async () => {
    vi.mocked(api.inspect).mockResolvedValueOnce(generatedInspection());
    renderWizard();
    fireEvent.change(screen.getByLabelText(/local source path/i), { target: { value: "C:/projects/generated" } });
    fireEvent.click(screen.getByRole("button", { name: /analyze project/i }));

    await screen.findByRole("heading", { name: /how rig will run this app/i });
    fireEvent.click(screen.getByRole("button", { name: /accept setup/i }));

    const summary = await screen.findByText("Check the highlighted fields.");
    await waitFor(() => expect(document.activeElement).toBe(summary));
    expect(screen.getByLabelText(/application name/i).getAttribute("aria-invalid")).toBe("true");
  });

  it("reuses its draft application when plan acceptance must be retried", async () => {
    vi.mocked(api.inspect).mockResolvedValueOnce(generatedInspection());
    vi.mocked(api.acceptDeploymentPlan)
      .mockRejectedValueOnce(new APIError({ status: 409, code: "deployment_plan_review_required", detail: "Project structure changed." }))
      .mockResolvedValueOnce({
        revisionId: "11111111-1111-4111-8111-111111111111", revisionNumber: 1, canonicalDigest: "f".repeat(64), strategy: "generated_node", state: "accepted",
        source: { provider: "local", repositoryId: 0, resolvedDigest: "a".repeat(64) }, detector: { name: "projectanalysis", version: "2", sourceStructuralFingerprint: "b".repeat(64) }, components: [], fieldProvenance: [], migration: { present: false },
      });
    renderWizard();
    fireEvent.change(screen.getByLabelText(/application name/i), { target: { value: "Generated app" } });
    fireEvent.change(screen.getByLabelText(/local source path/i), { target: { value: "C:/projects/generated" } });
    fireEvent.click(screen.getByRole("button", { name: /analyze project/i }));
    await screen.findByRole("heading", { name: /how rig will run this app/i });
    fireEvent.click(screen.getByRole("button", { name: /accept setup/i }));
    expect(await screen.findByText(/project setup changed while you were reviewing it/i)).toBeTruthy();
    expect(screen.getByRole("button", { name: /back to source/i }).hasAttribute("disabled")).toBe(true);
    expect(screen.getByRole("button", { name: /open saved draft/i })).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: /accept setup/i }));
    await screen.findByRole("heading", { name: /setup accepted/i });
    expect(api.createApp).toHaveBeenCalledTimes(1);
    expect(api.acceptDeploymentPlan).toHaveBeenCalledTimes(2);
  });

  it("announces migration approval and restores focus to the ready heading", async () => {
    const pendingRevision = {
      revisionId: "11111111-1111-4111-8111-111111111111",
      revisionNumber: 1,
      canonicalDigest: "f".repeat(64),
      strategy: "generated_node",
      state: "accepted",
      source: { provider: "local", repositoryId: 0, resolvedDigest: "a".repeat(64) },
      detector: { name: "projectanalysis", version: "2", sourceStructuralFingerprint: "b".repeat(64) },
      components: [],
      fieldProvenance: [],
      migration: { present: true, approvalStatus: "pending" },
    };
    vi.mocked(api.inspect).mockResolvedValueOnce(generatedInspection());
    vi.mocked(api.acceptDeploymentPlan).mockResolvedValueOnce(pendingRevision as never);
    vi.mocked(api.approveDeploymentPlanMigration).mockResolvedValueOnce({
      ...pendingRevision,
      migration: { present: true, approvalStatus: "approved" },
    } as never);
    renderWizard();
    fireEvent.change(screen.getByLabelText(/application name/i), { target: { value: "Generated app" } });
    fireEvent.change(screen.getByLabelText(/local source path/i), { target: { value: "C:/projects/generated" } });
    fireEvent.click(screen.getByRole("button", { name: /analyze project/i }));
    await screen.findByRole("heading", { name: /how rig will run this app/i });
    fireEvent.click(screen.getByRole("button", { name: /accept setup/i }));

    const readyHeading = await screen.findByRole("heading", { name: /setup accepted/i });
    const approval = screen.getByRole("button", { name: /approve migration before the next deployment/i });
    expect(screen.getByRole("status").textContent).toContain("Database migration approval is required before deployment.");
    approval.focus();
    fireEvent.click(approval);

    await screen.findByText("The migration is approved for this plan revision.");
    expect(screen.getByRole("status").textContent).toBe("Database migration approved. Deployment setup is ready.");
    await waitFor(() => expect(document.activeElement).toBe(readyHeading));
  });

  it("locks navigation and alternate strategies as soon as a generated draft is created", async () => {
    const response = deferred<Awaited<ReturnType<typeof api.acceptDeploymentPlan>>>();
    const detected = generatedInspection();
    detected.composeCandidates = ["compose.yaml"];
    vi.mocked(api.inspect).mockResolvedValueOnce(detected);
    vi.mocked(api.acceptDeploymentPlan).mockReturnValueOnce(response.promise);
    renderWizard();
    fireEvent.change(screen.getByLabelText(/application name/i), { target: { value: "Generated app" } });
    fireEvent.change(screen.getByLabelText(/local source path/i), { target: { value: "C:/projects/generated" } });
    fireEvent.click(screen.getByRole("button", { name: /analyze project/i }));
    await screen.findByRole("heading", { name: /how rig will run this app/i });
    fireEvent.click(screen.getByRole("button", { name: /accept setup/i }));

    await screen.findByText(/application draft saved/i);
    expect(screen.getByRole("button", { name: /back to source/i }).hasAttribute("disabled")).toBe(true);
    expect(screen.queryByRole("button", { name: /use existing compose setup/i })).toBeNull();

    await act(async () => response.resolve({
      revisionId: "11111111-1111-4111-8111-111111111111", revisionNumber: 1, canonicalDigest: "f".repeat(64), strategy: "generated_node", state: "accepted",
      source: { provider: "local", repositoryId: 0, resolvedDigest: "a".repeat(64) }, detector: { name: "projectanalysis", version: "2", sourceStructuralFingerprint: "b".repeat(64) }, components: [], fieldProvenance: [], migration: { present: false },
    }));
    await screen.findByRole("heading", { name: /setup accepted/i });
    expect(api.createApp).toHaveBeenCalledTimes(1);
  });

  it("adopts the server plan when another session wins the acceptance race", async () => {
    vi.mocked(api.inspect).mockResolvedValueOnce(generatedInspection());
    vi.mocked(api.acceptDeploymentPlan).mockRejectedValueOnce(new APIError({ status: 409, code: "deployment_plan_conflict", detail: "Revision changed." }));
    vi.mocked(api.deploymentPlan).mockResolvedValueOnce({
      revisionId: "22222222-2222-4222-8222-222222222222", revisionNumber: 2, canonicalDigest: "e".repeat(64), strategy: "generated_node", state: "accepted",
      source: { provider: "local", repositoryId: 0, resolvedDigest: "a".repeat(64) }, detector: { name: "projectanalysis", version: "2", sourceStructuralFingerprint: "b".repeat(64) }, components: [], fieldProvenance: [], migration: { present: false },
    });
    renderWizard();
    fireEvent.change(screen.getByLabelText(/application name/i), { target: { value: "Generated app" } });
    fireEvent.change(screen.getByLabelText(/local source path/i), { target: { value: "C:/projects/generated" } });
    fireEvent.click(screen.getByRole("button", { name: /analyze project/i }));
    await screen.findByRole("heading", { name: /how rig will run this app/i });
    fireEvent.click(screen.getByRole("button", { name: /accept setup/i }));

    expect(await screen.findByRole("heading", { name: /setup accepted/i })).toBeTruthy();
    expect(screen.getByText(/revision 2/i)).toBeTruthy();
    expect(api.deploymentPlan).toHaveBeenCalledWith("app-1");
    expect(api.createApp).toHaveBeenCalledTimes(1);
  });

  it("focuses the error summary and links name and local path validation messages", async () => {
    renderWizard();
    const form = screen.getByText("Application source").closest("form");
    if (!form) throw new Error("Expected source wizard form");
    fireEvent.submit(form);

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
    const form = screen.getByText("Application source").closest("form");
    if (!form) throw new Error("Expected source wizard form");
    fireEvent.submit(form);

    expect(await screen.findByText(/description must be 300 characters or fewer/i)).toBeTruthy();
    expect(description.getAttribute("aria-invalid")).toBe("true");
    expect(description.getAttribute("aria-describedby")).toBe("wizard-description-error");
    fireEvent.change(description, { target: { value: "Short description" } });
    expect(description.getAttribute("aria-invalid")).toBe("false");
    expect(description.getAttribute("aria-describedby")).toBeNull();
    expect(screen.queryByText(/description must be 300 characters or fewer/i)).toBeNull();
  });

  it("focuses the error summary after a create failure", async () => {
    mockLocalComposeInspection();
    vi.mocked(api.createApp).mockRejectedValueOnce(new APIError({ status: 503, code: "provider_unavailable", detail: "Application storage is temporarily unavailable." }));
    renderWizard();
    fireEvent.change(screen.getByLabelText(/application name/i), { target: { value: "Local app" } });
    fireEvent.change(screen.getByLabelText(/local source path/i), { target: { value: "C:/projects/local" } });
    fireEvent.click(screen.getByRole("button", { name: /analyze project/i }));
    await screen.findByText(/source inspection completed/i);
    fireEvent.click(screen.getByRole("button", { name: /save application/i }));

    const summary = await screen.findByText(/application storage is temporarily unavailable/i);
    await waitFor(() => expect(document.activeElement).toBe(summary));
  });

  it("shows local inspection failures in context and clears them when the source changes", async () => {
    vi.mocked(api.inspect).mockRejectedValueOnce(new APIError({ status: 422, code: "invalid_source", detail: "The selected folder could not be inspected." }));
    renderWizard();
    const localPath = screen.getByLabelText(/local source path/i);
    fireEvent.change(localPath, { target: { value: "C:/projects/broken" } });
    fireEvent.click(screen.getByRole("button", { name: /analyze project/i }));
    expect(await screen.findByText(/selected folder could not be inspected/i)).toBeTruthy();

    fireEvent.change(localPath, { target: { value: "C:/projects/fixed" } });
    expect(screen.queryByText(/selected folder could not be inspected/i)).toBeNull();
    fireEvent.click(screen.getByLabelText(/^github repository$/i));
    fireEvent.click(screen.getByLabelText(/^local folder$/i));
    expect(screen.queryByText(/selected folder could not be inspected/i)).toBeNull();
  });

  it.each(["success", "error"] as const)("ignores a stale local inspection %s after the path changes", async (outcome) => {
    const inspectionResult = deferred<Awaited<ReturnType<typeof api.inspect>>>();
    vi.mocked(api.inspect).mockReturnValueOnce(inspectionResult.promise);
    renderWizard();
    const localPath = screen.getByLabelText(/local source path/i);
    fireEvent.change(localPath, { target: { value: "C:/projects/first" } });
    fireEvent.click(screen.getByRole("button", { name: /analyze project/i }));
    fireEvent.change(localPath, { target: { value: "C:/projects/second" } });

    await act(async () => {
      if (outcome === "success") {
        inspectionResult.resolve(inspectionFixture({ source: { type: "local", path: "C:/projects/first" }, composeCandidates: ["compose.yaml"], services: [{ name: "stale" }], findings: [] }));
      } else {
        inspectionResult.reject(new APIError({ status: 422, code: "invalid_source", detail: "The stale local path failed." }));
      }
    });

    expect(screen.queryByText(/source inspection completed/i)).toBeNull();
    expect(screen.queryByText(/stale local path failed/i)).toBeNull();
    expect((localPath as HTMLInputElement).value).toBe("C:/projects/second");
  });

  it("shows the capability-disabled state without calling provider endpoints", async () => {
    vi.restoreAllMocks();
    mockCommon(false);
    renderWizard();
    fireEvent.click(screen.getByLabelText(/github repository/i));

    expect(await screen.findByText(/^github connections are disabled$/i, { selector: "strong" })).toBeTruthy();
    expect(screen.getByText(/^the administrator disabled github connections on this controller\.$/i)).toBeTruthy();
    expect(api.sourceConnections).not.toHaveBeenCalled();
  });

  it("announces capability checking and confirmed disabled in one persistent region", async () => {
    const capabilityResult = deferred<Awaited<ReturnType<typeof api.status>>>();
    vi.mocked(api.status).mockReturnValueOnce(capabilityResult.promise);
    renderWizard();
    fireEvent.click(screen.getByLabelText(/^github repository$/i));

    const capabilityStatus = screen.getByText(/^checking github connection capability\.$/i, { selector: ".capability-status" });
    expect(capabilityStatus.getAttribute("aria-live")).toBe("polite");
    await act(async () => capabilityResult.resolve({ capabilities: { githubConnections: false } } as never));
    expect(await screen.findByText(/^github connections are disabled\.$/i, { selector: ".capability-status" })).toBe(capabilityStatus);
  });

  it("announces a capability error after checking", async () => {
    const capabilityResult = deferred<Awaited<ReturnType<typeof api.status>>>();
    vi.mocked(api.status).mockReturnValueOnce(capabilityResult.promise);
    renderWizard();
    fireEvent.click(screen.getByLabelText(/^github repository$/i));

    const capabilityStatus = screen.getByText(/^checking github connection capability\.$/i, { selector: ".capability-status" });
    await act(async () => capabilityResult.reject(new APIError({ status: 503, code: "provider_unavailable", detail: "Controller status is temporarily unavailable." })));
    expect(await screen.findByText(/^github connection capability check failed\.$/i, { selector: ".capability-status" })).toBe(capabilityStatus);
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

    const summary = await screen.findByText(/choose and inspect a compose file before saving/i);
    await waitFor(() => expect(document.activeElement).toBe(summary));
  });

  it("distinguishes a capability error from disabled and retries it", async () => {
    vi.mocked(api.status)
      .mockRejectedValueOnce(new APIError({ status: 503, code: "provider_unavailable", detail: "Controller status is temporarily unavailable." }))
      .mockResolvedValueOnce({ capabilities: { githubConnections: true } } as never);
    renderWizard();
    fireEvent.click(screen.getByLabelText(/^github repository$/i));

    expect(await screen.findByText(/controller status is temporarily unavailable/i)).toBeTruthy();
    expect(screen.queryByText(/github connections are disabled/i)).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: /retry capability check/i }));
    expect(await screen.findByLabelText(/^github connection$/i)).toBeTruthy();
  });

  it("keeps a stable disabled connection control while loading", async () => {
    let resolveConnections: ((value: { items: Array<typeof connection> }) => void) | undefined;
    vi.mocked(api.sourceConnections).mockImplementationOnce(() => new Promise((resolve) => { resolveConnections = resolve; }));
    renderWizard();
    fireEvent.click(screen.getByLabelText(/^github repository$/i));

    const select = await screen.findByLabelText(/^github connection$/i) as HTMLSelectElement;
    const connectionStatus = screen.getByText(/^loading github connections\.$/i).closest("[role='status']");
    expect(select.disabled).toBe(true);
    expect(screen.getByRole("option", { name: /loading connections/i })).toBeTruthy();
    resolveConnections?.({ items: [connection] });
    await waitFor(() => expect(select.disabled).toBe(false));
    expect(screen.getByText(/^github connections loaded\./i).closest("[role='status']")).toBe(connectionStatus);
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

  it("announces installation, repository, and branch loading and results in persistent regions", async () => {
    const installationsResult = deferred<Awaited<ReturnType<typeof api.githubInstallations>>>();
    const repositoriesResult = deferred<Awaited<ReturnType<typeof api.githubRepositories>>>();
    const branchesResult = deferred<Awaited<ReturnType<typeof api.githubBranches>>>();
    vi.mocked(api.githubInstallations).mockReturnValueOnce(installationsResult.promise);
    vi.mocked(api.githubRepositories).mockReturnValueOnce(repositoriesResult.promise);
    vi.mocked(api.githubBranches).mockReturnValueOnce(branchesResult.promise);
    renderWizard();
    await selectConnectedGitHub();

    const installationStatus = screen.getByText(/^loading github app installations page 1\.$/i);
    expect(installationStatus.getAttribute("aria-live")).toBe("polite");
    expect(screen.getAllByText(/^loading github app installations page 1\.$/i)).toHaveLength(1);
    await act(async () => installationsResult.resolve({
      page: 1,
      perPage: 30,
      totalCount: 1,
      items: [{ id: 10, accountLogin: "octo-org", accountType: "Organization", targetType: "Organization", repositorySelection: "selected", cachedAt: "2026-01-01T00:00:00Z" }],
    }));
    expect(await screen.findByText(/^github app installations page 1 loaded\. 1 result\.$/i)).toBe(installationStatus);

    await selectInstallation();
    const repositoryStatus = screen.getByText(/^loading repositories page 1\.$/i);
    expect(screen.getAllByText(/^loading repositories page 1\.$/i)).toHaveLength(1);
    await act(async () => repositoriesResult.resolve({
      page: 1,
      perPage: 30,
      totalCount: 1,
      items: [{ id: 20, owner: "octo-org", name: "web", defaultBranch: "main", private: true, archived: false, disabled: false }],
    }));
    expect(await screen.findByText(/^repositories page 1 loaded\. 1 result\.$/i)).toBe(repositoryStatus);

    await selectRepository();
    const branchStatus = screen.getByText(/^loading branches page 1\.$/i);
    expect(screen.getAllByText(/^loading branches page 1\.$/i)).toHaveLength(1);
    await act(async () => branchesResult.resolve({ page: 1, perPage: 30, items: [{ name: "main", sha: "abc123", protected: true }] }));
    expect(await screen.findByText(/^branches page 1 loaded\. 1 result\.$/i)).toBe(branchStatus);
  });

  it("uses one persistent atomic live region from device instructions through connection", async () => {
    vi.mocked(api.sourceConnections).mockResolvedValue({ items: [{ ...connection, id: "new-connection" }] });
    vi.spyOn(api, "startGitHubConnection").mockResolvedValue({ connectionId: "new-connection", userCode: "ABCD-EFGH", verificationUri: "https://github.com/login/device", installUrl: "https://github.com/apps/rig/installations/new", expiresAt: "2099-01-01T00:00:00Z", pollIntervalSeconds: 1 });
    vi.spyOn(api, "pollGitHubConnection").mockResolvedValue({ ...connection, id: "new-connection", status: "connected" });
    vi.mocked(api.githubInstallations)
      .mockResolvedValueOnce({ page: 1, perPage: 30, totalCount: 0, items: [] })
      .mockResolvedValueOnce({ page: 1, perPage: 30, totalCount: 1, items: [{ id: 10, accountLogin: "octo-org", accountType: "Organization", targetType: "Organization", repositorySelection: "selected", cachedAt: "2026-01-01T00:00:00Z" }] });
    renderWizard();
    fireEvent.click(screen.getByLabelText(/^github repository$/i));
    await screen.findByLabelText(/^github connection$/i);
    vi.useFakeTimers();
    fireEvent.click(screen.getByRole("button", { name: /sign in to github/i }));
    await vi.advanceTimersByTimeAsync(0);

    const code = screen.getByText("ABCD-EFGH");
    const liveRegion = code.closest("[role='status']");
    expect(liveRegion?.getAttribute("aria-live")).toBe("polite");
    expect(liveRegion?.getAttribute("aria-atomic")).toBe("true");
    expect(document.querySelectorAll(".connection-status[role='status']")).toHaveLength(1);
    const signIn = screen.getByRole("link", { name: /sign in to github \(opens in a new tab\)/i });
    expect(signIn.getAttribute("href")).toBe("https://github.com/login/device");
    expect(signIn.getAttribute("rel")).toBe("noreferrer");
    expect(screen.queryByRole("link", { name: /install or configure repository access/i })).toBeNull();

    await vi.advanceTimersByTimeAsync(1000);
    expect(api.pollGitHubConnection).toHaveBeenCalledTimes(1);
    await vi.runOnlyPendingTimersAsync();
    vi.useRealTimers();
    const connectedRegion = await screen.findByText(/step 1 complete: signed in to github/i).then((element) => element.closest("[role='status']"));
    expect(connectedRegion).toBe(liveRegion);
    expect(screen.queryByText("ABCD-EFGH")).toBeNull();
    const install = screen.getByRole("link", { name: /install or configure repository access \(opens in a new tab\)/i });
    expect(install.getAttribute("href")).toBe("https://github.com/apps/rig/installations/new");
    expect(install.getAttribute("target")).toBe("_blank");
    expect(screen.getByText(/choose the personal account or organization that owns the repository/i)).toBeTruthy();
    expect(document.getElementById("github-save-help")?.textContent).toMatch(/install or configure repository access, then choose the github app installation/i);
    expect(document.querySelectorAll(".connection-status[role='status']")).toHaveLength(1);

    fireEvent.click(screen.getByRole("button", { name: /retry github app installation/i }));
    await screen.findByRole("option", { name: /octo-org/i });
    await waitFor(() => expect(screen.queryByRole("link", { name: /install or configure repository access/i })).toBeNull());
    expect(screen.getByText(/connection status: connected/i).closest("[role='status']")).toBe(liveRegion);

  });

  it("resumes a pending authorization after reload, honors nextPollAt, and continues to installation", async () => {
    const pending = pendingConnection({ nextPollAt: new Date(Date.now() + 30_000).toISOString() });
    const connected = { ...connection, installUrl: pending.installUrl };
    vi.mocked(api.sourceConnections).mockResolvedValueOnce({ items: [pending] }).mockResolvedValue({ items: [connected] });
    vi.spyOn(api, "pollGitHubConnection").mockResolvedValue(connected);
    vi.mocked(api.githubInstallations).mockResolvedValue({ page: 1, perPage: 30, totalCount: 0, items: [] });
    renderWizard();
    await selectPendingGitHub();

    const resume = screen.getByRole("button", { name: /resume authorization check/i });
    expect(resume).toBeTruthy();
    expect(document.activeElement).toBe(screen.getByLabelText(/^github connection$/i));
    expect(screen.getByText(/resume authorization check is now available/i)).toBeTruthy();
    expect(document.body.textContent).not.toMatch(/USER-CODE|device-code-sentinel|verificationUri/i);
    vi.useFakeTimers();
    resume.focus();
    fireEvent.click(resume);
    expect(screen.getByRole("button", { name: /checking authorization/i }).getAttribute("aria-disabled")).toBe("true");
    await vi.advanceTimersByTimeAsync(29_000);
    expect(api.pollGitHubConnection).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(1_000);
    expect(api.pollGitHubConnection).toHaveBeenCalledWith(connection.id);
    await vi.advanceTimersByTimeAsync(0);
    await flushAsyncWork();

    expect(screen.getByText(/step 1 complete: signed in to github/i)).toBeTruthy();
    const install = screen.getByRole("link", { name: /install or configure repository access/i });
    expect(install.getAttribute("href")).toBe(pending.installUrl);
    expect(document.activeElement).toBe(install);
    expect(document.body.textContent).not.toMatch(/USER-CODE|device-code-sentinel|verificationUri/i);
  });

  it("uses Retry-After when a resumed authorization is polled too soon", async () => {
    const pending = pendingConnection({ nextPollAt: new Date(Date.now() - 1000).toISOString() });
    vi.mocked(api.sourceConnections).mockResolvedValue({ items: [pending] });
    vi.spyOn(api, "pollGitHubConnection")
      .mockRejectedValueOnce(new APIError({ status: 429, code: "poll_too_soon", detail: "Try again shortly.", retryAfterSeconds: 9 }))
      .mockResolvedValueOnce({ ...connection, installUrl: pending.installUrl });
    renderWizard();
    await selectPendingGitHub();
    vi.useFakeTimers();
    const initialResume = screen.getByRole("button", { name: /resume authorization check/i });
    initialResume.focus();
    fireEvent.click(initialResume);
    await vi.advanceTimersByTimeAsync(0);
    expect(api.pollGitHubConnection).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(8_999);
    expect(api.pollGitHubConnection).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(1);
    expect(api.pollGitHubConnection).toHaveBeenCalledTimes(2);
  });

  it("caps Retry-After at expiry and performs only one terminal reconciliation", async () => {
    const pending = pendingConnection({ pendingExpiresAt: new Date(Date.now() + 30_000).toISOString(), nextPollAt: new Date(Date.now() - 1000).toISOString() });
    vi.mocked(api.sourceConnections).mockResolvedValue({ items: [pending] });
    vi.spyOn(api, "pollGitHubConnection")
      .mockRejectedValueOnce(new APIError({ status: 429, code: "poll_too_soon", detail: "Try again shortly.", retryAfterSeconds: 120 }))
      .mockRejectedValueOnce(new APIError({ status: 410, code: "authorization_expired", detail: "GitHub authorization expired." }));
    renderWizard();
    await selectPendingGitHub();
    vi.useFakeTimers();
    fireEvent.click(screen.getByRole("button", { name: /resume authorization check/i }));
    await vi.advanceTimersByTimeAsync(0);
    expect(api.pollGitHubConnection).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(29_000);
    expect(api.pollGitHubConnection).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(1_000);
    await flushAsyncWork();
    await vi.advanceTimersByTimeAsync(0);
    await flushAsyncWork();
    expect(api.pollGitHubConnection).toHaveBeenCalledTimes(2);
    await vi.advanceTimersByTimeAsync(120_000);
    expect(api.pollGitHubConnection).toHaveBeenCalledTimes(2);
    expect(screen.queryByRole("button", { name: /resume authorization check/i })).toBeNull();
  });

  it("pauses a resumed authorization after a transient error and allows manual retry", async () => {
    const pending = pendingConnection({ nextPollAt: new Date(Date.now() - 1000).toISOString() });
    vi.mocked(api.sourceConnections).mockResolvedValue({ items: [pending] });
    vi.spyOn(api, "pollGitHubConnection")
      .mockRejectedValueOnce(new APIError({ status: 503, code: "provider_unavailable", detail: "GitHub is temporarily unavailable." }))
      .mockResolvedValueOnce({ ...connection, installUrl: pending.installUrl });
    renderWizard();
    await selectPendingGitHub();
    vi.useFakeTimers();
    const initialResume = screen.getByRole("button", { name: /resume authorization check/i });
    initialResume.focus();
    fireEvent.click(initialResume);
    await vi.advanceTimersByTimeAsync(0);
    await flushAsyncWork();

    expect(screen.getByText(/select resume authorization check to try again/i)).toBeTruthy();
    const retry = screen.getByRole("button", { name: /resume authorization check/i });
    expect(document.activeElement).toBe(retry);
    fireEvent.click(retry);
    await vi.advanceTimersByTimeAsync(0);
    await flushAsyncWork();
    expect(api.pollGitHubConnection).toHaveBeenCalledTimes(2);
    expect(screen.getByText(/step 1 complete: signed in to github/i)).toBeTruthy();
  });

  it.each([
    ["authorization_denied", "denied"],
    ["identity_already_connected", "access_lost"],
    ["invalid_connection_state", "access_lost"],
  ] as const)("stops a resumed authorization on %s", async (code, status) => {
    const pending = pendingConnection({ nextPollAt: new Date(Date.now() - 1000).toISOString() });
    vi.mocked(api.sourceConnections)
      .mockResolvedValueOnce({ items: [pending] })
      .mockResolvedValue({ items: [{ ...pending, status }] });
    vi.spyOn(api, "pollGitHubConnection").mockRejectedValue(new APIError({ status: code === "identity_already_connected" ? 409 : 400, code, detail: "Authorization cannot continue." }));
    renderWizard();
    await selectPendingGitHub();
    vi.useFakeTimers();
    fireEvent.click(screen.getByRole("button", { name: /resume authorization check/i }));
    await vi.advanceTimersByTimeAsync(0);
    await flushAsyncWork();

    expect(screen.getByText(new RegExp(`connection status: ${status.replace("_", " ")}`, "i"))).toBeTruthy();
    expect(screen.queryByRole("button", { name: /resume authorization check/i })).toBeNull();
    expect(document.activeElement).toBe(screen.getByLabelText(/^github connection$/i));
    expect(api.pollGitHubConnection).toHaveBeenCalledTimes(1);
  });

  it("reconciles an already-expired raw pending connection exactly once without offering Resume", async () => {
    const pending = pendingConnection({ pendingExpiresAt: new Date(Date.now() - 1000).toISOString(), nextPollAt: new Date(Date.now() - 2000).toISOString() });
    vi.mocked(api.sourceConnections).mockResolvedValue({ items: [pending] });
    const poll = vi.spyOn(api, "pollGitHubConnection").mockRejectedValue(new APIError({ status: 410, code: "authorization_expired", detail: "GitHub authorization expired." }));
    renderWizard();
    await selectPendingGitHub();

    expect(screen.getByText(/connection status: expired/i)).toBeTruthy();
    expect(screen.queryByRole("button", { name: /resume authorization check/i })).toBeNull();
    expect(document.activeElement).toBe(screen.getByLabelText(/^github connection$/i));
    await waitFor(() => expect(poll).toHaveBeenCalledTimes(1));
    expect(poll).toHaveBeenCalledWith(pending.id);
    await act(async () => new Promise((resolve) => window.setTimeout(resolve, 20)));
    expect(poll).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole("button", { name: /resume authorization check/i })).toBeNull();
  });

  it.each([
    new APIError({ status: 503, code: "provider_unavailable", detail: "GitHub is temporarily unavailable." }),
    new APIError({ status: 429, code: "poll_too_soon", detail: "Try again shortly.", retryAfterSeconds: 30 }),
  ])("pauses an expired reconciliation after %s and retries only on explicit action", async (firstError) => {
    const pending = pendingConnection({ pendingExpiresAt: new Date(Date.now() - 1000).toISOString(), nextPollAt: new Date(Date.now() + 30_000).toISOString() });
    vi.mocked(api.sourceConnections).mockResolvedValue({ items: [pending] });
    const poll = vi.spyOn(api, "pollGitHubConnection")
      .mockRejectedValueOnce(firstError)
      .mockRejectedValueOnce(new APIError({ status: 410, code: "authorization_expired", detail: "GitHub authorization expired." }));
    renderWizard();
    await selectPendingGitHub();

    await waitFor(() => expect(poll).toHaveBeenCalledTimes(1));
    expect(screen.queryByRole("button", { name: /resume authorization check/i })).toBeNull();
    const retry = await screen.findByRole("button", { name: /retry authorization status/i });
    retry.focus();
    fireEvent.click(retry);
    await waitFor(() => expect(poll).toHaveBeenCalledTimes(2));
    expect(screen.queryByRole("button", { name: /retry authorization status/i })).toBeNull();
    expect(screen.queryByRole("button", { name: /resume authorization check/i })).toBeNull();
    expect(document.activeElement).toBe(screen.getByLabelText(/^github connection$/i));
  });

  it("ignores a resumed poll completion after another pending connection is selected", async () => {
    const pendingA = pendingConnection({ nextPollAt: new Date(Date.now() - 1000).toISOString() });
    const pendingB = pendingConnection({ id: otherConnection.id, nextPollAt: new Date(Date.now() - 1000).toISOString() });
    const poll = deferred<Awaited<ReturnType<typeof api.pollGitHubConnection>>>();
    vi.mocked(api.sourceConnections).mockResolvedValue({ items: [pendingA, pendingB] });
    vi.spyOn(api, "pollGitHubConnection").mockReturnValue(poll.promise);
    renderWizard();
    await selectPendingGitHub(pendingA.id);
    vi.useFakeTimers();
    fireEvent.click(screen.getByRole("button", { name: /resume authorization check/i }));
    await vi.advanceTimersByTimeAsync(0);
    expect(api.pollGitHubConnection).toHaveBeenCalledWith(pendingA.id);

    fireEvent.change(screen.getByLabelText(/^github connection$/i), { target: { value: pendingB.id } });
    await act(async () => poll.resolve({ ...connection, id: pendingA.id, installUrl: pendingA.installUrl }));
    expect((screen.getByLabelText(/^github connection$/i) as HTMLSelectElement).value).toBe(pendingB.id);
    expect(screen.queryByText(/step 1 complete: signed in to github/i)).toBeNull();
    expect(screen.getByRole("button", { name: /resume authorization check/i })).toBeTruthy();
  });

  it("disables every connection action while one mutation is pending", async () => {
    let resolveRefresh: ((value: typeof connection) => void) | undefined;
    vi.spyOn(api, "refreshSourceConnection").mockImplementation(() => new Promise((resolve) => { resolveRefresh = resolve; }));
    renderWizard();
    await selectConnectedGitHub();
    await screen.findByRole("button", { name: /refresh connection/i });
    fireEvent.click(screen.getByRole("button", { name: /refresh connection/i }));

    await waitFor(() => expect((screen.getByRole("button", { name: /sign in to github/i }) as HTMLButtonElement).disabled).toBe(true));
    expect((screen.getByRole("button", { name: /refreshing/i }) as HTMLButtonElement).disabled).toBe(true);
    expect((screen.getByRole("button", { name: /disconnect/i }) as HTMLButtonElement).disabled).toBe(true);
    resolveRefresh?.(connection);
    await waitFor(() => expect((screen.getByRole("button", { name: /sign in to github/i }) as HTMLButtonElement).disabled).toBe(false));
  });

  it("ignores a refresh completion after selecting another connection", async () => {
    const refreshResult = deferred<Awaited<ReturnType<typeof api.refreshSourceConnection>>>();
    vi.mocked(api.sourceConnections).mockResolvedValue({ items: [connection, otherConnection] });
    vi.spyOn(api, "refreshSourceConnection").mockReturnValueOnce(refreshResult.promise);
    renderWizard();
    await selectConnectedGitHub();
    fireEvent.click(await screen.findByRole("button", { name: /refresh connection/i }));

    const connectionSelect = screen.getByLabelText(/^github connection$/i) as HTMLSelectElement;
    fireEvent.change(connectionSelect, { target: { value: otherConnection.id } });
    await selectInstallation();
    expect((screen.getByLabelText(/^github app installation$/i) as HTMLSelectElement).value).toBe("10");

    await act(async () => refreshResult.resolve({ ...connection, status: "access_lost" }));
    await waitFor(() => expect(connectionSelect.value).toBe(otherConnection.id));
    expect((screen.getByLabelText(/^github app installation$/i) as HTMLSelectElement).value).toBe("10");
    expect(screen.getByText(/connection status: connected/i)).toBeTruthy();
  });

  it("ignores a disconnect completion after selecting another connection", async () => {
    const disconnectResult = deferred<Awaited<ReturnType<typeof api.disconnectSourceConnection>>>();
    vi.mocked(api.sourceConnections).mockResolvedValue({ items: [connection, otherConnection] });
    vi.spyOn(api, "disconnectSourceConnection").mockReturnValueOnce(disconnectResult.promise);
    renderWizard();
    await selectConnectedGitHub();
    fireEvent.click(await screen.findByRole("button", { name: /^disconnect$/i }));

    const connectionSelect = screen.getByLabelText(/^github connection$/i) as HTMLSelectElement;
    fireEvent.change(connectionSelect, { target: { value: otherConnection.id } });
    await selectInstallation();
    await act(async () => disconnectResult.resolve(undefined));

    await waitFor(() => expect(connectionSelect.value).toBe(otherConnection.id));
    expect((screen.getByLabelText(/^github app installation$/i) as HTMLSelectElement).value).toBe("10");
    expect(screen.getByText(/connection status: connected/i)).toBeTruthy();
  });

  it.each(["local source", "another connection"] as const)("does not let a late connection start steal the %s", async (destination) => {
    const startResult = deferred<Awaited<ReturnType<typeof api.startGitHubConnection>>>();
    vi.mocked(api.sourceConnections).mockResolvedValue({ items: [connection, otherConnection] });
    vi.spyOn(api, "startGitHubConnection").mockReturnValueOnce(startResult.promise);
    renderWizard();
    fireEvent.click(screen.getByLabelText(/^github repository$/i));
    await screen.findByLabelText(/^github connection$/i);
    fireEvent.click(screen.getByRole("button", { name: /sign in to github/i }));

    if (destination === "local source") {
      fireEvent.click(screen.getByLabelText(/^local folder$/i));
    } else {
      await screen.findByRole("option", { name: /@rig-backup/i });
      fireEvent.change(screen.getByLabelText(/^github connection$/i), { target: { value: otherConnection.id } });
    }
    await act(async () => startResult.resolve({ connectionId: "late-connection", userCode: "LATE-CODE", verificationUri: "https://github.com/login/device", installUrl: "https://github.com/apps/rig/installations/new", expiresAt: "2099-01-01T00:00:00Z", pollIntervalSeconds: 5 }));

    expect(screen.queryByText("LATE-CODE")).toBeNull();
    if (destination === "local source") {
      expect((screen.getByRole("radio", { name: /^local folder$/i }) as HTMLInputElement).checked).toBe(true);
    } else {
      await waitFor(() => expect((screen.getByLabelText(/^github connection$/i) as HTMLSelectElement).value).toBe(otherConnection.id));
      expect(screen.getByText(/connection status: connected/i)).toBeTruthy();
    }
  });

  it("polls a replacement authorization while the previous authorization poll is unresolved", async () => {
    const firstPoll = deferred<Awaited<ReturnType<typeof api.pollGitHubConnection>>>();
    vi.mocked(api.sourceConnections).mockResolvedValue({ items: [{ ...connection, id: "authorization-b" }] });
    vi.spyOn(api, "startGitHubConnection")
      .mockResolvedValueOnce({ connectionId: "authorization-a", userCode: "CODE-A", verificationUri: "https://github.com/login/device", installUrl: "https://github.com/apps/rig/installations/new", expiresAt: "2099-01-01T00:00:00Z", pollIntervalSeconds: 1 })
      .mockResolvedValueOnce({ connectionId: "authorization-b", userCode: "CODE-B", verificationUri: "https://github.com/login/device", installUrl: "https://github.com/apps/rig/installations/new", expiresAt: "2099-01-01T00:00:00Z", pollIntervalSeconds: 1 });
    vi.spyOn(api, "pollGitHubConnection").mockImplementation((connectionId) => connectionId === "authorization-a"
      ? firstPoll.promise
      : Promise.resolve({ ...connection, id: "authorization-b", status: "connected" }));
    vi.spyOn(api, "disconnectSourceConnection").mockResolvedValue(undefined);
    renderWizard();
    fireEvent.click(screen.getByLabelText(/^github repository$/i));
    await screen.findByLabelText(/^github connection$/i);
    vi.useFakeTimers();

    fireEvent.click(screen.getByRole("button", { name: /sign in to github/i }));
    await vi.advanceTimersByTimeAsync(0);
    expect(screen.getByText("CODE-A")).toBeTruthy();
    await vi.advanceTimersByTimeAsync(1000);
    expect(api.pollGitHubConnection).toHaveBeenCalledWith("authorization-a");

    fireEvent.click(screen.getByRole("button", { name: /^disconnect$/i }));
    await vi.advanceTimersByTimeAsync(0);
    fireEvent.click(screen.getByRole("button", { name: /sign in to github/i }));
    await vi.advanceTimersByTimeAsync(0);
    expect(screen.getByText("CODE-B")).toBeTruthy();
    await vi.advanceTimersByTimeAsync(1000);
    expect(api.pollGitHubConnection).toHaveBeenCalledWith("authorization-b");

    await act(async () => firstPoll.resolve({ ...connection, id: "authorization-a", status: "denied" }));
    await vi.runOnlyPendingTimersAsync();
    expect((screen.getByLabelText(/^github connection$/i) as HTMLSelectElement).value).toBe("authorization-b");
    expect(screen.getByText(/connection status: connected/i)).toBeTruthy();
    expect(api.pollGitHubConnection).toHaveBeenCalledTimes(2);
  });

  it("reconciles an invalid device expiration once and treats authorization_expired as terminal", async () => {
    vi.spyOn(api, "startGitHubConnection").mockResolvedValue({ connectionId: "new-connection", userCode: "ABCD-EFGH", verificationUri: "https://github.com/login/device", installUrl: "https://github.com/apps/rig/installations/new", expiresAt: "invalid", pollIntervalSeconds: 5 });
    const poll = vi.spyOn(api, "pollGitHubConnection").mockRejectedValue(new APIError({ status: 410, code: "authorization_expired", detail: "GitHub authorization expired." }));
    renderWizard();
    fireEvent.click(screen.getByLabelText(/^github repository$/i));
    await screen.findByLabelText(/^github connection$/i);
    const connectionRegion = document.querySelector(".connection-status[role='status']");
    if (!connectionRegion) throw new Error("Expected persistent connection status region");
    fireEvent.click(screen.getByRole("button", { name: /sign in to github/i }));

    const expiration = await screen.findByText(/github authorization expired/i);
    expect(expiration.closest("[role='status']")).toBe(connectionRegion);
    expect(connectionRegion.textContent).toMatch(/connection status: expired/i);
    expect(screen.queryByText("ABCD-EFGH")).toBeNull();
    expect(poll).toHaveBeenCalledTimes(1);
  });

  it("shows recovery guidance when no installations are available", async () => {
    vi.mocked(api.githubInstallations)
      .mockResolvedValueOnce({ page: 1, perPage: 30, totalCount: 0, items: [] })
      .mockResolvedValueOnce({ page: 1, perPage: 30, totalCount: 1, items: [{ id: 10, accountLogin: "octo-org", accountType: "Organization", targetType: "Organization", repositorySelection: "selected", cachedAt: "2026-01-01T00:00:00Z" }] });
    renderWizard();
    await selectConnectedGitHub();

    expect(await screen.findByText(/no github app installations found/i)).toBeTruthy();
    expect(screen.getByText(/sign in to github again to install or configure repository access, then retry/i)).toBeTruthy();
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

  it("normalizes null inspection collections into the no-Compose state without enabling save", async () => {
    vi.mocked(api.inspect).mockRestore();
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ source: { type: "github" }, resolvedSha: "abc123", composeCandidates: null, services: null, findings: null }), { status: 200 }),
    ));
    renderWizard();
    fireEvent.change(screen.getByLabelText(/application name/i), { target: { value: "GitHub app" } });
    await selectConnectedGitHub();
    await selectInstallation();
    await selectRepository();
    await selectBranch();
    fireEvent.click(screen.getByRole("button", { name: /analyze project/i }));

    const emptyResult = (await screen.findByText(/no compose files found/i)).closest("[role='status']");
    expect(emptyResult?.getAttribute("aria-live")).toBe("polite");
    expect(emptyResult?.getAttribute("aria-atomic")).toBe("true");
    expect(screen.getByText("Add a Compose file to the tracked branch, then inspect again.")).toBeTruthy();
    expect(screen.getByText("Add a supported JavaScript project or a Compose file, then analyze again.")).toBeTruthy();
    expect(screen.queryByText(/source inspection completed/i)).toBeNull();
    expect(screen.queryByText(/ready to save/i)).toBeNull();
    expect(screen.queryByLabelText(/^compose file$/i)).toBeNull();
    expect(screen.getByRole("button", { name: /save application/i }).hasAttribute("disabled")).toBe(true);
    expect(api.createApp).not.toHaveBeenCalled();
  });

  it("fails closed when findings is a present malformed collection", async () => {
    vi.mocked(api.inspect).mockRestore();
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ source: { type: "github" }, resolvedSha: "abc123", composeCandidates: ["compose.yaml"], services: [], findings: { code: "hidden_finding" } }), { status: 200 }),
    ));
    renderWizard();
    fireEvent.change(screen.getByLabelText(/application name/i), { target: { value: "GitHub app" } });
    await selectConnectedGitHub();
    await selectInstallation();
    await selectRepository();
    await selectBranch();
    fireEvent.click(screen.getByRole("button", { name: /analyze project/i }));

    expect(await screen.findByText("The controller returned an invalid source inspection response.")).toBeTruthy();
    expect(screen.queryByText(/source inspection completed/i)).toBeNull();
    expect(screen.queryByText(/ready to save/i)).toBeNull();
    expect(screen.queryByLabelText(/^compose file$/i)).toBeNull();
    expect(screen.getByRole("button", { name: /save application/i }).hasAttribute("disabled")).toBe(true);
    expect(api.createApp).not.toHaveBeenCalled();
  });

  it.each(["success", "error"] as const)("ignores a stale GitHub inspection %s after an upstream branch change", async (outcome) => {
    const inspectionResult = deferred<Awaited<ReturnType<typeof api.inspect>>>();
    vi.mocked(api.inspect).mockReturnValueOnce(inspectionResult.promise);
    renderWizard();
    await selectConnectedGitHub();
    await selectInstallation();
    await selectRepository();
    await selectBranch();
    fireEvent.click(screen.getByRole("button", { name: /analyze project/i }));
    fireEvent.change(screen.getByLabelText(/^tracked branch$/i), { target: { value: "" } });

    await act(async () => {
      if (outcome === "success") {
        inspectionResult.resolve(inspectionFixture({ source: { type: "github" }, resolvedSha: "stale-sha", composeCandidates: ["compose.yaml"], services: [], findings: [] }));
      } else {
        inspectionResult.reject(new APIError({ status: 422, code: "invalid_source", detail: "The stale GitHub source failed." }));
      }
    });

    expect(screen.queryByLabelText(/^compose file$/i)).toBeNull();
    expect(screen.queryByText(/source inspection completed/i)).toBeNull();
    expect(screen.queryByText(/stale github source failed/i)).toBeNull();
    expect((screen.getByLabelText(/^tracked branch$/i) as HTMLSelectElement).value).toBe("");
  });

  it("does not let a stale exact inspection satisfy save gating", async () => {
    const exactInspectionResult = deferred<Awaited<ReturnType<typeof api.inspect>>>();
    vi.mocked(api.inspect)
      .mockResolvedValueOnce(inspectionFixture({ source: { type: "github" }, resolvedSha: "abc123", composeCandidates: ["compose.yaml"], services: [], findings: [] }))
      .mockReturnValueOnce(exactInspectionResult.promise);
    renderWizard();
    fireEvent.change(screen.getByLabelText(/application name/i), { target: { value: "GitHub app" } });
    await selectConnectedGitHub();
    await selectInstallation();
    await selectRepository();
    await selectBranch();
    fireEvent.click(screen.getByRole("button", { name: /analyze project/i }));
    const composeFile = await screen.findByLabelText(/^compose file$/i);
    fireEvent.change(composeFile, { target: { value: "compose.yaml" } });
    fireEvent.click(screen.getByRole("button", { name: /inspect selected compose file/i }));
    fireEvent.change(screen.getByLabelText(/^tracked branch$/i), { target: { value: "" } });

    await act(async () => exactInspectionResult.resolve(inspectionFixture({ source: { type: "github", composePath: "compose.yaml" }, resolvedSha: "abc123", composeCandidates: ["compose.yaml"], services: [{ name: "web" }], findings: [] })));

    expect(screen.queryByText(/source inspection completed/i)).toBeNull();
    expect(screen.getByRole("button", { name: /save application/i }).hasAttribute("disabled")).toBe(true);
    expect(api.createApp).not.toHaveBeenCalled();
  });

  it("selects a GitHub source, requires an exact clean inspection, and sends only githubSource", async () => {
    vi.mocked(api.inspect)
      .mockResolvedValueOnce(inspectionFixture({ source: { type: "github" }, resolvedSha: "abc123", composeCandidates: ["compose.yaml"], services: [], findings: [] }))
      .mockResolvedValueOnce(inspectionFixture({ source: { type: "github", composePath: "compose.yaml" }, resolvedSha: "abc123", composeCandidates: ["compose.yaml"], services: [{ name: "web" }], findings: [] }));
    const { onCreated } = renderWizard();
    fireEvent.change(screen.getByLabelText(/application name/i), { target: { value: "GitHub app" } });
    await selectConnectedGitHub();
    await selectInstallation();
    await selectRepository();
    await selectBranch();
    expect(document.getElementById("github-save-help")?.textContent).toMatch(/analyze the selected repository before continuing/i);
    fireEvent.click(screen.getByRole("button", { name: /analyze project/i }));
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

  it("accepts a generated GitHub application without requiring a Compose path", async () => {
    vi.mocked(api.inspect).mockResolvedValueOnce(generatedInspection({ type: "github", connectionId: connection.id, installationId: 10, repositoryId: 20, trackedBranch: "main", resolvedSha: "a".repeat(40) }));
    renderWizard();
    fireEvent.change(screen.getByLabelText(/application name/i), { target: { value: "Generated GitHub app" } });
    await selectConnectedGitHub();
    await selectInstallation();
    await selectRepository();
    await selectBranch();
    fireEvent.click(screen.getByRole("button", { name: /analyze project/i }));
    await screen.findByRole("heading", { name: /how rig will run this app/i });
    fireEvent.click(screen.getByRole("button", { name: /accept setup/i }));

    await waitFor(() => expect(api.createApp).toHaveBeenCalledWith({
      name: "Generated GitHub app",
      description: "",
      githubSource: { connectionId: connection.id, installationId: 10, repositoryId: 20, branch: "main" },
    }));
    expect(api.acceptDeploymentPlan).toHaveBeenCalledWith("app-1", expect.objectContaining({ candidateId: "candidate-web" }));
  });

  it("keeps an explicitly selected clean Compose source ahead of generated candidates", async () => {
    const discovery = generatedInspection({ type: "github" });
    discovery.composeCandidates = ["compose.yaml"];
    const exactCompose = generatedInspection({ type: "github", composePath: "compose.yaml" });
    exactCompose.composeCandidates = ["compose.yaml"];
    exactCompose.services = [{ name: "web" }];
    vi.mocked(api.inspect)
      .mockResolvedValueOnce(discovery)
      .mockResolvedValueOnce(exactCompose);
    const { onCreated } = renderWizard();
    fireEvent.change(screen.getByLabelText(/application name/i), { target: { value: "Compose app" } });
    await selectConnectedGitHub();
    await selectInstallation();
    await selectRepository();
    await selectBranch();
    fireEvent.click(screen.getByRole("button", { name: /analyze project/i }));
    await screen.findByRole("heading", { name: /how rig will run this app/i });
    fireEvent.click(screen.getByRole("button", { name: /use existing compose setup/i }));

    fireEvent.change(await screen.findByLabelText(/^compose file$/i), { target: { value: "compose.yaml" } });
    fireEvent.click(screen.getByRole("button", { name: /inspect selected compose file/i }));
    await screen.findByText(/source inspection completed/i);
    expect(screen.queryByRole("heading", { name: /how rig will run this app/i })).toBeNull();
    const save = screen.getByRole("button", { name: /save application/i });
    expect((save as HTMLButtonElement).disabled).toBe(false);
    fireEvent.click(save);

    await waitFor(() => expect(api.createApp).toHaveBeenCalled());
    expect(vi.mocked(api.createApp).mock.calls[0]?.[0]).toEqual(expect.objectContaining({
      name: "Compose app",
      githubSource: expect.objectContaining({ composePath: "compose.yaml" }),
    }));
    expect(api.acceptDeploymentPlan).not.toHaveBeenCalled();
    expect(onCreated).toHaveBeenCalledWith("app-1");
  });

  it("describes policy findings truthfully and keeps saving blocked", async () => {
    vi.mocked(api.inspect)
      .mockResolvedValueOnce(inspectionFixture({ source: { type: "github" }, composeCandidates: ["compose.yaml"], services: [], findings: [] }))
      .mockResolvedValueOnce(inspectionFixture({ source: { type: "github", composePath: "compose.yaml" }, composeCandidates: ["compose.yaml"], services: [], findings: [{ code: "unsupported_path", message: "A referenced file leaves the release workspace." }] }));
    renderWizard();
    fireEvent.change(screen.getByLabelText(/application name/i), { target: { value: "GitHub app" } });
    await selectConnectedGitHub();
    await selectInstallation();
    await selectRepository();
    await selectBranch();
    fireEvent.click(screen.getByRole("button", { name: /analyze project/i }));
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
      .mockResolvedValueOnce(inspectionFixture({ source: { type: "github" }, composeCandidates: ["compose.yaml"], services: [], findings: [] }))
      .mockResolvedValueOnce(inspectionFixture({ source: { type: "github" }, composeCandidates: ["compose.yaml"], services: [], findings: [] }));
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

  it("keeps the installation pagination and focused control stable while a page is loading", async () => {
    const secondPage = deferred<Awaited<ReturnType<typeof api.githubInstallations>>>();
    vi.mocked(api.githubInstallations).mockImplementation((_connectionId, page = 1) => page === 2
      ? secondPage.promise
      : Promise.resolve({
          page: 1,
          perPage: 30,
          totalCount: 90,
          items: [{ id: 10, accountLogin: "octo-org", accountType: "Organization", targetType: "Organization", repositorySelection: "selected" as const, cachedAt: "2026-01-01T00:00:00Z" }],
        }));
    renderWizard();
    await selectConnectedGitHub();
    await screen.findByRole("option", { name: /octo-org/i });

    const pagination = screen.getByRole("navigation", { name: /github app installations pagination/i });
    const previous = screen.getByRole("button", { name: /previous github app installations page/i }) as HTMLButtonElement;
    const next = screen.getByRole("button", { name: /next github app installations page/i }) as HTMLButtonElement;
    next.focus();
    expect(document.activeElement).toBe(next);
    fireEvent.click(next);

    await waitFor(() => expect(api.githubInstallations).toHaveBeenLastCalledWith(connection.id, 2, 30));
    expect(screen.getByRole("navigation", { name: /github app installations pagination/i })).toBe(pagination);
    expect(screen.getByRole("button", { name: /previous github app installations page/i })).toBe(previous);
    expect(screen.getByRole("button", { name: /next github app installations page/i })).toBe(next);
    await waitFor(() => expect(pagination.getAttribute("aria-busy")).toBe("true"));
    expect(previous.disabled).toBe(false);
    expect(previous.getAttribute("aria-disabled")).toBe("true");
    expect(next.disabled).toBe(false);
    expect(next.getAttribute("aria-disabled")).toBe("true");
    expect(document.activeElement).toBe(next);
    expect(screen.getAllByText(/^loading github app installations page 2\.$/i)).toHaveLength(1);
    fireEvent.click(next);
    expect(api.githubInstallations).toHaveBeenCalledTimes(2);

    await act(async () => secondPage.resolve({
      page: 2,
      perPage: 30,
      totalCount: 30,
      items: [],
    }));
    await screen.findByText(/^github app installations page 2 loaded\. 0 results\.$/i);
    expect(screen.getByRole("navigation", { name: /github app installations pagination/i })).toBe(pagination);
    expect(screen.getByRole("button", { name: /next github app installations page/i })).toBe(next);
    expect(pagination.getAttribute("aria-busy")).toBe("false");
    expect(previous.getAttribute("aria-disabled")).toBe("false");
    expect(next.getAttribute("aria-disabled")).toBe("true");
    expect(document.activeElement).toBe(next);
    fireEvent.click(next);
    expect(api.githubInstallations).toHaveBeenCalledTimes(2);
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

  it("retains Previous on an empty second branch page and returns to page one", async () => {
    const firstPageBranches = Array.from({ length: 30 }, (_, index) => ({
      name: index === 0 ? "main" : `branch-${index}`,
      sha: `sha-${index}`,
      protected: index === 0,
    }));
    vi.mocked(api.githubBranches).mockImplementation(async (_connectionId, _installationId, _repositoryId, page = 1) => ({
      page,
      perPage: 30,
      items: page === 1 ? firstPageBranches : [],
    }));
    renderWizard();
    await selectConnectedGitHub();
    await selectInstallation();
    await selectRepository();
    await screen.findByRole("option", { name: /main/i });

    fireEvent.click(screen.getByRole("button", { name: /next branches page/i }));
    await screen.findByText(/^branches page 2 loaded\. 0 results\.$/i);
    const pagination = screen.getByRole("navigation", { name: /branches pagination/i });
    const previous = screen.getByRole("button", { name: /previous branches page/i }) as HTMLButtonElement;
    const next = screen.getByRole("button", { name: /next branches page/i }) as HTMLButtonElement;
    expect(pagination.textContent).toMatch(/page 2/i);
    expect(previous.getAttribute("aria-disabled")).toBe("false");
    expect(next.getAttribute("aria-disabled")).toBe("true");
    expect(screen.getByText(/no branches found/i)).toBeTruthy();

    fireEvent.click(previous);
    await waitFor(() => expect(api.githubBranches).toHaveBeenLastCalledWith(connection.id, 10, 20, 1, 30));
    await screen.findByText(/^branches page 1 loaded\. 30 results\.$/i);
    expect(screen.getByRole("navigation", { name: /branches pagination/i })).toBe(pagination);
    expect(pagination.textContent).toMatch(/page 1/i);
    expect(previous.getAttribute("aria-disabled")).toBe("true");
    expect(next.getAttribute("aria-disabled")).toBe("false");
    expect(screen.getByRole("option", { name: /main/i })).toBeTruthy();
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
    fireEvent.click(screen.getByRole("button", { name: /sign in to github/i }));
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
