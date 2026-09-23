import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { APIError, api, type InspectResponse } from "./api";

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


function renderWizard() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const onCreated = vi.fn();
  render(<QueryClientProvider client={client}><SourceWizard onCancel={vi.fn()} onCreated={onCreated} /></QueryClientProvider>);
  return { client, onCreated };
}

function mockCommon(enabled = true) {
  vi.spyOn(api, "status").mockResolvedValue({ capabilities: { githubConnections: enabled } } as never);
  vi.spyOn(api, "defaultSourceConnection").mockResolvedValue({ configured: true, connection });
  vi.spyOn(api, "defaultGitHubRepositories").mockResolvedValue({ page: 1, perPage: 30, totalCount: 1, truncated: false, items: [{ connectionId: connection.id, installationId: 10, accountLogin: "octo-org", id: 20, owner: "octo-org", name: "web", defaultBranch: "main", private: true, archived: false, disabled: false }] });
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
  await screen.findByText("Connected as @rig-admin");
}

async function selectRepository() {
  const repository = await screen.findByLabelText(/^repository$/i);
  await screen.findByRole("option", { name: /octo-org\/web/i });
  fireEvent.change(repository, { target: { value: `${connection.id}:10:20` } });
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

function reviewedSetupInspection(source: InspectResponse["source"] = { type: "local", path: "C:/projects/generated" }): InspectResponse {
  const result = generatedInspection(source);
  const candidate = result.analysis.candidates[0];
  candidate.id = "user:deployment-setup";
  candidate.origin = "user";
  candidate.digest = "d".repeat(64);
  return result;
}

async function reviewGeneratedSetup() {
  fireEvent.click(screen.getByRole("button", { name: "Review setup" }));
  await screen.findByRole("button", { name: "Accept setup" });
}

async function reachCleanExactSource() {
  fireEvent.change(screen.getByLabelText(/application name/i), { target: { value: "GitHub app" } });
  await selectConnectedGitHub();
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
    sessionStorage.clear();
    mockCommon();
  });
  afterEach(() => {
    cleanup();
    vi.useRealTimers();
  });

  it("keeps the local source create flow usable", async () => {
    mockLocalComposeInspection();
    const { onCreated } = renderWizard();
    expect(screen.getByLabelText(/local source path/i).getAttribute("placeholder")).toBe("C:\\projects\\my-app");
    expect(screen.getByLabelText(/local source path/i).getAttribute("placeholder")?.split(String.fromCharCode(92))).toEqual(["C:", "projects", "my-app"]);
    fireEvent.change(screen.getByLabelText(/application name/i), { target: { value: "Local app" } });
    fireEvent.change(screen.getByLabelText(/local source path/i), { target: { value: "C:/projects/local" } });
    fireEvent.click(screen.getByRole("button", { name: /analyze project/i }));
    await screen.findByText(/source inspection completed/i);
    fireEvent.click(screen.getByRole("button", { name: /save application/i }));

    await waitFor(() => expect(api.createApp).toHaveBeenCalledWith({ name: "Local app", description: "", sourcePath: "C:/projects/local" }, expect.anything()));
    expect(onCreated).toHaveBeenCalledWith("app-1");
  });


  it("analyzes, reviews, and accepts a generated local application before saving it", async () => {
    vi.mocked(api.inspect).mockResolvedValueOnce(generatedInspection()).mockResolvedValueOnce(reviewedSetupInspection());
    const { onCreated } = renderWizard();
    fireEvent.change(screen.getByLabelText(/application name/i), { target: { value: "Generated app" } });
    fireEvent.change(screen.getByLabelText(/local source path/i), { target: { value: "C:/projects/generated" } });
    fireEvent.click(screen.getByRole("button", { name: /analyze project/i }));

    expect(await screen.findByRole("heading", { name: /how rig will run this app/i })).toBeTruthy();
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole("heading", { name: /how rig will run this app/i })));
    fireEvent.change(screen.getByRole("textbox", { name: "Start command" }), { target: { value: "node server.js && echo ${READY} $()" } });
    await reviewGeneratedSetup();
    fireEvent.click(screen.getByRole("button", { name: "Accept setup" }));

    await waitFor(() => expect(api.createApp).toHaveBeenCalledWith(expect.objectContaining({ name: "Generated app", description: "", sourcePath: "C:/projects/generated", setup: expect.any(Object) })));
    expect(api.acceptDeploymentPlan).toHaveBeenCalledWith("app-1", expect.objectContaining({
      candidateId: "user:deployment-setup",
      expectedCandidateDigest: "d".repeat(64),
      expectedSourceStructuralFingerprint: "b".repeat(64),
      setup: expect.objectContaining({ components: [expect.objectContaining({ startCommand: "node server.js && echo ${READY} $()" })] }),
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
    vi.mocked(api.inspect).mockResolvedValueOnce(generatedInspection()).mockResolvedValueOnce(reviewedSetupInspection());
    renderWizard();
    fireEvent.change(screen.getByLabelText(/local source path/i), { target: { value: "C:/projects/generated" } });
    fireEvent.click(screen.getByRole("button", { name: /analyze project/i }));

    await screen.findByRole("heading", { name: /how rig will run this app/i });
    await reviewGeneratedSetup();
    fireEvent.click(screen.getByRole("button", { name: /accept setup/i }));

    const summary = await screen.findByText("Check the highlighted fields.");
    await waitFor(() => expect(document.activeElement).toBe(summary));
    expect(screen.getByLabelText(/application name/i).getAttribute("aria-invalid")).toBe("true");
  });

  it("reuses its draft application when plan acceptance must be retried", async () => {
    vi.mocked(api.inspect).mockResolvedValueOnce(generatedInspection()).mockResolvedValueOnce(reviewedSetupInspection()).mockResolvedValueOnce(reviewedSetupInspection());
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
    await reviewGeneratedSetup();
    fireEvent.click(screen.getByRole("button", { name: "Accept setup" }));
    expect(await screen.findByText(/project setup changed while you were reviewing it/i)).toBeTruthy();
    expect(screen.getByRole("button", { name: /back to source/i }).hasAttribute("disabled")).toBe(true);
    expect(screen.getByRole("button", { name: /open saved draft/i })).toBeTruthy();
    await screen.findByRole("button", { name: "Accept setup" });
    fireEvent.click(screen.getByRole("button", { name: "Accept setup" }));
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
    vi.mocked(api.inspect).mockResolvedValueOnce(generatedInspection()).mockResolvedValueOnce(reviewedSetupInspection());
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
    await reviewGeneratedSetup();
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
    vi.mocked(api.inspect).mockResolvedValueOnce(detected).mockResolvedValueOnce(reviewedSetupInspection());
    vi.mocked(api.acceptDeploymentPlan).mockReturnValueOnce(response.promise);
    renderWizard();
    fireEvent.change(screen.getByLabelText(/application name/i), { target: { value: "Generated app" } });
    fireEvent.change(screen.getByLabelText(/local source path/i), { target: { value: "C:/projects/generated" } });
    fireEvent.click(screen.getByRole("button", { name: /analyze project/i }));
    await screen.findByRole("heading", { name: /how rig will run this app/i });
    await reviewGeneratedSetup();
    fireEvent.click(screen.getByRole("button", { name: "Accept setup" }));

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
    vi.mocked(api.inspect).mockResolvedValueOnce(generatedInspection()).mockResolvedValueOnce(reviewedSetupInspection());
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
    await reviewGeneratedSetup();
    fireEvent.click(screen.getByRole("button", { name: "Accept setup" }));

    expect(await screen.findByRole("heading", { name: /setup accepted/i })).toBeTruthy();
    expect(screen.getByText(/revision 2/i)).toBeTruthy();
    expect(api.deploymentPlan).toHaveBeenCalledWith("app-1");
    expect(api.createApp).toHaveBeenCalledTimes(1);
  });

  it("automatically reuses the account connection and requires only a repository selection", async () => {
    const start = vi.spyOn(api, "startDefaultGitHubConnection");
    renderWizard(); await selectConnectedGitHub();
    await screen.findByRole("option", { name: "octo-org/web (private)" });
    expect(screen.queryByLabelText("GitHub connection")).toBeNull();
    expect(screen.queryByLabelText("GitHub App installation")).toBeNull();
    expect(start).not.toHaveBeenCalled();
    await selectRepository();
    await waitFor(() => expect(api.githubBranches).toHaveBeenCalledWith(connection.id, 10, 20, 1, 30));
  });

  it("keeps repository discovery failures actionable and save blocked until retry succeeds", async () => {
    vi.mocked(api.defaultGitHubRepositories).mockRejectedValueOnce(new Error("private provider failure"));
    renderWizard(); await selectConnectedGitHub();
    await screen.findByRole("alert");
    expect(screen.queryByText("No repositories found")).toBeNull();
    expect(screen.getByRole("button", { name: "Save application" }).hasAttribute("disabled")).toBe(true);
    fireEvent.click(screen.getByRole("button", { name: "Retry repositories" }));
    await selectRepository();
    await screen.findByLabelText("Tracked branch");
    expect(api.createApp).not.toHaveBeenCalled();
  });

  it("invalidates a clean source when the saved account loses access", async () => {
    mockCleanInspection();
    const { client } = renderWizard(); await reachCleanExactSource();
    await act(async () => { client.setQueryData(["default-source-connection"], { configured: true, connection: { ...connection, status: "access_lost" } }); });
    await waitFor(() => expect(screen.getByRole("button", { name: "Save application" }).hasAttribute("disabled")).toBe(true));
    expect(screen.queryByLabelText("Tracked branch")).toBeNull();
    expect(screen.queryByText("Source inspection completed")).toBeNull();
    expect(api.createApp).not.toHaveBeenCalled();
  });

  it("clears inspection when Enter applies a repository search without submitting the application", async () => {
    mockCleanInspection(); renderWizard(); await reachCleanExactSource();
    const search = screen.getByLabelText("Search repositories");
    search.focus(); fireEvent.change(search, { target: { value: "another" } });
    fireEvent.keyDown(search, { key: "Enter" });
    await waitFor(() => expect(api.defaultGitHubRepositories).toHaveBeenLastCalledWith("another", 1, 30));
    expect(document.activeElement).toBe(search);
    expect(screen.queryByLabelText("Tracked branch")).toBeNull();
    expect(screen.getByRole("button", { name: "Save application" }).hasAttribute("disabled")).toBe(true);
    expect(api.createApp).not.toHaveBeenCalled();

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
    expect(api.defaultSourceConnection).not.toHaveBeenCalled();
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
    await screen.findByText("Connected as @rig-admin");
    const saveButton = screen.getByRole("button", { name: /save application/i });
    expect(saveButton.getAttribute("aria-describedby")).toBe("github-save-help");
    expect(document.getElementById("github-save-help")?.textContent).toMatch(/choose a repository before saving/i);
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

    expect(await screen.findByText("Connected as @rig-admin")).toBeTruthy();

  });

  it("shows recovery guidance when no branches are available", async () => {
    vi.mocked(api.githubBranches)
      .mockResolvedValueOnce({ page: 1, perPage: 30, items: [] })
      .mockResolvedValueOnce({ page: 1, perPage: 30, items: [{ name: "main", sha: "abc123", protected: true }] });
    renderWizard();
    await selectConnectedGitHub();
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

  it("normalizes null inspection collections and keeps the manual setup path available", async () => {
    vi.mocked(api.inspect).mockRestore();
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ source: { type: "github" }, resolvedSha: "abc123", composeCandidates: null, services: null, findings: null }), { status: 200 }),
    ));
    renderWizard();
    fireEvent.change(screen.getByLabelText(/application name/i), { target: { value: "GitHub app" } });
    await selectConnectedGitHub();
      await selectRepository();
    await selectBranch();
    fireEvent.click(screen.getByRole("button", { name: /analyze project/i }));


    expect(await screen.findByRole("heading", { name: /how rig will run this app/i })).toBeTruthy();
    expect(screen.getByText("No supported setup was detected")).toBeTruthy();
    expect(screen.getByLabelText(/^start command$/i).getAttribute("aria-invalid")).toBe("true");

    expect(screen.queryByText(/source inspection completed/i)).toBeNull();
    expect(screen.getByRole("button", { name: "Review setup" }).hasAttribute("disabled")).toBe(false);
    expect(api.createApp).not.toHaveBeenCalled();
  });

  it("opens undetected local settings, serializes numeric ports, and preserves edits through failed review", async () => {
    const review = deferred<InspectResponse>();
    vi.mocked(api.inspect).mockResolvedValueOnce(inspectionFixture({source: {type: "local", path: "C:/projects/manual"}, composeCandidates: [], services: [], findings: [{code: "compose_not_found", message: "No Compose file"}]})).mockReturnValueOnce(review.promise);
    renderWizard();
    fireEvent.change(screen.getByLabelText(/application name/i), {target: {value: "Manual app"}});
    fireEvent.change(screen.getByLabelText(/local source path/i), {target: {value: "C:/projects/manual"}});
    fireEvent.click(screen.getByRole("button", {name: "Analyze project"}));
    expect(await screen.findByRole("heading", {name: /how rig will run this app/i})).toBeTruthy();
    fireEvent.change(screen.getByLabelText(/^start command$/i), {target: {value: "node server.js"}});
    fireEvent.change(screen.getByLabelText(/^internal port$/i), {target: {value: "3001"}});
    fireEvent.click(screen.getByRole("button", {name: "Back to source"}));
    fireEvent.click(screen.getByRole("button", {name: "Configure build and run"}));
    expect((screen.getByLabelText(/^start command$/i) as HTMLInputElement).value).toBe("node server.js");
    expect((screen.getByLabelText(/^internal port$/i) as HTMLInputElement).value).toBe("3001");
    fireEvent.click(screen.getByRole("button", {name: "Review setup"}));
    await waitFor(() => expect(api.inspect).toHaveBeenCalledTimes(2));
    expect(vi.mocked(api.inspect).mock.calls[1][0].setup?.components[0]).toMatchObject({rootDirectory: ".", startCommand: "node server.js", internalPort: 3001, installCommand: "", buildCommand: ""});
    expect(screen.getByRole("button", {name: /^Reviewing/}).hasAttribute("disabled")).toBe(true);
    fireEvent.click(screen.getByRole("button", {name: /^Reviewing/}));
    expect(api.inspect).toHaveBeenCalledTimes(2);
    await act(async () => {review.reject(new APIError({status: 503, code: "source_unavailable", detail: "Source temporarily unavailable"}));});
    expect((screen.getByLabelText(/^start command$/i) as HTMLInputElement).value).toBe("node server.js");
    expect(await screen.findByRole("button", {name: "Retry review"})).toBeTruthy();
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
      await selectRepository();
    await selectBranch();
    expect(document.getElementById("github-save-help")?.textContent).toMatch(/analyze the selected repository before continuing/i);
    fireEvent.click(screen.getByRole("button", { name: /analyze project/i }));
    await screen.findByLabelText(/^compose file$/i);
    expect(screen.getByRole("button", { name: /save application/i }).hasAttribute("disabled")).toBe(true);
    fireEvent.change(screen.getByLabelText(/compose file/i), { target: { value: "compose.yaml" } });
    expect(document.getElementById("github-save-help")?.textContent).toMatch(/inspect the selected compose file/i);
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
    const githubSource = { type: "github" as const, connectionId: connection.id, installationId: 10, repositoryId: 20, trackedBranch: "main", resolvedSha: "a".repeat(40) };
    vi.mocked(api.inspect).mockResolvedValueOnce(generatedInspection(githubSource)).mockResolvedValueOnce(reviewedSetupInspection(githubSource));
    renderWizard();
    fireEvent.change(screen.getByLabelText(/application name/i), { target: { value: "Generated GitHub app" } });
    await selectConnectedGitHub();
    await selectRepository();
    await selectBranch();
    fireEvent.click(screen.getByRole("button", { name: /analyze project/i }));
    await screen.findByRole("heading", { name: /how rig will run this app/i });
    await reviewGeneratedSetup();
    fireEvent.click(screen.getByRole("button", { name: "Accept setup" }));

    await waitFor(() => expect(api.createApp).toHaveBeenCalledWith(expect.objectContaining({
      name: "Generated GitHub app",
      description: "",
      githubSource: { connectionId: connection.id, installationId: 10, repositoryId: 20, branch: "main" },
      setup: expect.any(Object),
    })));
    expect(api.acceptDeploymentPlan).toHaveBeenCalledWith("app-1", expect.objectContaining({ candidateId: "user:deployment-setup" }));
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
      await selectRepository();
    await selectBranch();
    await inspectExactSource();
    fireEvent.change(screen.getByLabelText(/^tracked branch$/i), { target: { value: "" } });

    expect(screen.queryByText(/source inspection completed/i)).toBeNull();
    expect(screen.getByRole("button", { name: /save application/i }).hasAttribute("disabled")).toBe(true);
  });

  it("clears repository and all downstream state when the repository page changes", async () => {
    vi.mocked(api.defaultGitHubRepositories).mockImplementation(async (_query, page = 1) => ({ page, perPage: 30, totalCount: 60, truncated: false, items: [{ connectionId: connection.id, installationId: 10, accountLogin: "octo-org", id: page === 1 ? 20 : 21, owner: "octo-org", name: `web-${page}`, defaultBranch: "main", private: true, archived: false, disabled: false }] }));
    mockCleanInspection();
    renderWizard();
    await reachCleanExactSource();

    fireEvent.click(screen.getByRole("button", { name: /next repositories page/i }));
    await waitFor(() => expect(api.defaultGitHubRepositories).toHaveBeenLastCalledWith("", 2, 30));
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

});
