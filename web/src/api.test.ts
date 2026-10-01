import { beforeEach, describe, expect, it, vi } from "vitest";
import { APIError, api, clearCSRF, lanAccessObservationFresh, localRouteObservationFresh, setCSRF, verifiedLANAccessURL, verifiedLocalRouteURL, type Deployment, type LANAppAccessRead, type LocalRoute } from "./api";

describe("API client", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
    setCSRF("csrf-token");
  });

  it("rotates CSRF and retries a restored-session mutation", async () => {
    clearCSRF();
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ code: "csrf_failed", detail: "CSRF validation failed" }), { status: 403 }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ csrfToken: "rotated-token" }), { status: 200 }))
      .mockResolvedValueOnce(new Response(null, { status: 204 }));
    vi.stubGlobal("fetch", fetchMock);

    await api.logout();

    expect(fetchMock).toHaveBeenNthCalledWith(2, "/api/v1/auth/csrf", expect.objectContaining({ credentials: "same-origin" }));
    expect(fetchMock).toHaveBeenNthCalledWith(3, "/api/v1/auth/sessions/current", expect.objectContaining({
      headers: expect.objectContaining({ "X-CSRF-Token": "rotated-token" }),
    }));
  });

  it("sends browser CSRF protection for mutations", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ id: "a" }), { status: 201 }),
    );
    vi.stubGlobal("fetch", fetchMock);
    await api.createApp({ name: "Fixture", description: "", sourcePath: "C:/fixture" });
    expect(fetchMock).toHaveBeenCalledWith(
      "/api/v1/apps",
      expect.objectContaining({
        credentials: "same-origin",
        headers: expect.objectContaining({ "X-CSRF-Token": "csrf-token" }),
      }),
    );
  });

  it("uses the account connector routes and keeps search values encoded", async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ configured: false }), { status: 200 }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ page: 1, perPage: 30, totalCount: 0, truncated: false, items: [] }), { status: 200 }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ authorizationId: "b".repeat(32), connectionId: "a".repeat(32), userCode: "ABCD-EFGH", verificationUri: "https://github.com/login/device", installUrl: "https://github.com/apps/rig/installations/new", expiresAt: "2099-01-01T00:00:00Z", pollIntervalSeconds: 5 }), { status: 201 }));
    vi.stubGlobal("fetch", fetchMock);
    await api.defaultSourceConnection();
    await api.defaultGitHubRepositories("org/repo & private", 1, 30);
    await api.startDefaultGitHubConnection();
    expect(fetchMock).toHaveBeenNthCalledWith(1, "/api/v1/source-connections/default", expect.anything());
    expect(fetchMock).toHaveBeenNthCalledWith(2, "/api/v1/source-connections/default/github/repositories?q=org%2Frepo+%26+private&page=1&perPage=30", expect.anything());
    expect(fetchMock).toHaveBeenNthCalledWith(3, "/api/v1/source-connections/default/github/device", expect.objectContaining({ method: "POST", headers: expect.objectContaining({ "X-CSRF-Token": "csrf-token" }) }));
  });

  it("keeps pending authorization distinct from a connected durable account", async () => {
    const connection = { id: "a".repeat(32), provider: "github", status: "connected", credentialGeneration: 1, createdAt: "2026-01-01T00:00:00Z", updatedAt: "2026-01-01T00:00:00Z" };
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ authorizationId: "b".repeat(32), status: "pending", connection, nextPollAt: "2099-01-01T00:00:00Z" }), { status: 202, headers: { "Retry-After": "5" } }));
    vi.stubGlobal("fetch", fetchMock);
    await expect(api.pollDefaultGitHubConnection(connection.id, "b".repeat(32))).resolves.toMatchObject({ status: "pending", connection: { status: "connected" } });
    expect(fetchMock).toHaveBeenCalledWith(`/api/v1/source-connections/${connection.id}/device/${"b".repeat(32)}/poll`, expect.objectContaining({ method: "POST" }));
  });

  it.each([null, {}, { page: 1, perPage: 30, totalCount: 0, truncated: false, items: null }])("rejects malformed repository responses instead of reporting empty", async (body) => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify(body), { status: 200 })));
    await expect(api.defaultGitHubRepositories()).rejects.toMatchObject({ code: "invalid_connector_response" });
  });

  it("rejects unexpected fields in saved connector responses before exposing the data to the UI", async () => {
    const connection = { id: "a".repeat(32), provider: "github", status: "connected", credentialGeneration: 1, createdAt: "2026-01-01T00:00:00Z", updatedAt: "2026-01-01T00:00:00Z", accessToken: "must-never-enter-query-cache" };
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({ configured: true, connection }), { status: 200 })));
    await expect(api.defaultSourceConnection()).rejects.toMatchObject({ code: "invalid_connector_response", detail: "The controller returned an invalid GitHub connection response." });
  });

  it("surfaces safe problem details", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ detail: "Invalid credentials" }), { status: 401 }),
    ));
    await expect(api.login({ username: "a", passphrase: "b" })).rejects.toThrow("Invalid credentials");
  });

  it("preserves safe field errors from problem responses", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ code: "invalid_configuration", detail: "Invalid configuration", errors: { variables: "Use portable names", unsafe: 42 } }), { status: 422 }),
    ));
    await expect(api.replaceApplicationConfiguration("app", { expectedRevisionNumber: 0, variables: [], secrets: [], remove: [] })).rejects.toEqual(expect.objectContaining<Partial<APIError>>({
      code: "invalid_configuration",
      errors: { variables: "Use portable names" },
    }));
  });

  it("uses the generated cancellation operation with CSRF", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ job: { id: "job/one", status: "cancelled" } }), { status: 200 }),
    );
    vi.stubGlobal("fetch", fetchMock);
    await api.cancelJob("job/one");
    expect(fetchMock).toHaveBeenCalledWith(
      "/api/v1/jobs/job%2Fone/cancel",
      expect.objectContaining({
        method: "POST",
        headers: expect.objectContaining({ "X-CSRF-Token": "csrf-token" }),
      }),
    );
  });

  it("uses the generated configuration path and sends the exact revision request", async () => {
    const response = { revisionNumber: 2, entries: [] };
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify(response), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);
    const body = { expectedRevisionNumber: 1, variables: [{ key: "MODE", value: "prod" }], secrets: [], remove: ["OLD_TOKEN"] };
    await api.replaceApplicationConfiguration("app/one", body);
    expect(fetchMock).toHaveBeenCalledWith(
      "/api/v1/apps/app%2Fone/configuration",
      expect.objectContaining({ method: "PUT", body: JSON.stringify(body), headers: expect.objectContaining({ "X-CSRF-Token": "csrf-token" }) }),
    );
  });

  it("uses exact encoded source-connection paths and pagination queries", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ page: 2, perPage: 30, totalCount: 0, items: [] }), { status: 200 }),
    );
    vi.stubGlobal("fetch", fetchMock);

    await api.githubRepositories("connection/one", 42, 2, 30);

    expect(fetchMock).toHaveBeenCalledWith(
      "/api/v1/source-connections/connection%2Fone/github/installations/42/repositories?page=2&perPage=30",
      expect.objectContaining({ credentials: "same-origin" }),
    );
  });

  it("uses generated deployment-plan paths with exact CAS and migration bodies", async () => {
    const revision = { revisionNumber: 1, canonicalDigest: "a".repeat(64), strategy: "generated_node", state: "accepted", source: { provider: "local", repositoryId: 0, resolvedDigest: "b".repeat(64) }, detector: { name: "projectanalysis", version: "2", sourceStructuralFingerprint: "c".repeat(64) }, components: [], fieldProvenance: [], migration: { present: false } };
    const fetchMock = vi.fn().mockImplementation(() => Promise.resolve(new Response(JSON.stringify(revision), { status: 200 })));
    vi.stubGlobal("fetch", fetchMock);
    const acceptance = { expectedRevisionNumber: 0, expectedSourceStructuralFingerprint: "c".repeat(64), expectedCandidateDigest: "d".repeat(64), candidateId: "web", packageManager: "npm", installBehavior: "npm ci", migrationCommand: "", components: [{ componentId: "web", nodeVersion: "24", buildCommand: "npm run build", runCommand: "npm start", internalPort: 3000, healthProbe: "/" }] };
    const approval = { revisionId: "11111111-1111-4111-8111-111111111111", revisionNumber: 1, expectedApprovalRevision: 0 };

    await api.deploymentPlan("app/one");
    await api.acceptDeploymentPlan("app/one", acceptance);
    await api.approveDeploymentPlanMigration("app/one", approval);

    expect(fetchMock).toHaveBeenNthCalledWith(1, "/api/v1/apps/app%2Fone/deployment-plan", expect.objectContaining({ credentials: "same-origin" }));
    expect(fetchMock).toHaveBeenNthCalledWith(2, "/api/v1/apps/app%2Fone/deployment-plan", expect.objectContaining({ method: "PUT", body: JSON.stringify(acceptance), headers: expect.objectContaining({ "X-CSRF-Token": "csrf-token" }) }));
    expect(fetchMock).toHaveBeenNthCalledWith(3, "/api/v1/apps/app%2Fone/deployment-plan/migration-approval", expect.objectContaining({ method: "POST", body: JSON.stringify(approval), headers: expect.objectContaining({ "X-CSRF-Token": "csrf-token" }) }));
  });

  it("treats a pending device poll as a successful 202 response", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ id: "a", provider: "github", status: "pending", credentialGeneration: 0, createdAt: "2026-01-01T00:00:00Z", updatedAt: "2026-01-01T00:00:00Z" }), { status: 202 }),
    );
    vi.stubGlobal("fetch", fetchMock);

    await expect(api.pollGitHubConnection("a")).resolves.toMatchObject({ status: "pending" });
    expect(fetchMock).toHaveBeenCalledWith(
      "/api/v1/source-connections/a/device/poll",
      expect.objectContaining({ method: "POST", headers: expect.objectContaining({ "X-CSRF-Token": "csrf-token" }) }),
    );
  });

  it("returns a typed safe problem with Retry-After metadata", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ code: "poll_too_soon", detail: "Try again shortly." }), { status: 429, headers: { "Retry-After": "7" } }),
    ));

    await expect(api.pollGitHubConnection("a")).rejects.toEqual(expect.objectContaining<Partial<APIError>>({
      name: "APIError",
      status: 429,
      code: "poll_too_soon",
      detail: "Try again shortly.",
      retryAfterSeconds: 7,
    }));
  });

  it("sends exactly one typed inspect source", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ source: { type: "github" }, composeCandidates: [], services: [], findings: [] }), { status: 200 }),
    );
    vi.stubGlobal("fetch", fetchMock);

    await api.inspect({ githubSource: { connectionId: "a", installationId: 1, repositoryId: 2, branch: "main", composePath: "compose.yaml" } });

    expect(fetchMock).toHaveBeenCalledWith(
      "/api/v1/apps/import/inspect",
      expect.objectContaining({ body: JSON.stringify({ githubSource: { connectionId: "a", installationId: 1, repositoryId: 2, branch: "main", composePath: "compose.yaml" } }) }),
    );
  });

  it.each([
    ["null", { source: { type: "github" }, composeCandidates: null, services: null, findings: null }],
    ["missing", { source: { type: "local" } }],
  ])("normalizes %s inspection collections at the API boundary", async (_name, body) => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(
      new Response(JSON.stringify(body), { status: 200 }),
    ));

    await expect(api.inspect({ sourcePath: "C:/fixture" })).resolves.toMatchObject({
      composeCandidates: [],
      services: [],
      findings: [],
    });
  });

  it("rejects a present malformed inspection collection with a stable safe error", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ source: { type: "github" }, composeCandidates: ["compose.yaml"], services: [], findings: { code: "hidden_finding" } }), { status: 200 }),
    ));

    await expect(api.inspect({ sourcePath: "C:/fixture" })).rejects.toMatchObject({
      name: "APIError",
      status: 502,
      code: "invalid_inspection_response",
      detail: "The controller returned an invalid source inspection response.",
    });
  });

  it("rejects malformed analysis candidates before the wizard can render them", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ source: { type: "local" }, composeCandidates: [], services: [], findings: [], analysis: { source: { type: "local" }, resolvedDigest: "a", schemaVersion: "2", structuralFingerprint: "b", candidates: [null], findings: [] } }), { status: 200 }),
    ));

    await expect(api.inspect({ sourcePath: "C:/fixture" })).rejects.toMatchObject({
      name: "APIError",
      status: 502,
      code: "invalid_inspection_response",
    });
  });

  it("uses generated deployment and approval operation paths with CSRF and deploy idempotency", async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ items: [] }), { status: 200 }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ items: [] }), { status: 200 }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ items: [] }), { status: 200 }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ created: true, job: { id: "job-1" } }), { status: 202 }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ created: true, job: { id: "job-2" } }), { status: 202 }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ created: true, approval: { id: "approval-1" } }), { status: 201 }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ approval: { id: "approval-1" } }), { status: 200 }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ job: { id: "job-1" } }), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);

    await api.deployments("app/one"); await api.releases("app/one"); await api.runtimeApprovals("app/one");
    await api.deployApplication("app/one", "setup-request-key"); await api.deployRelease("app/one", "release/one", { configurationMode: "original" });
    await api.grantRuntimeApproval("app/one", { fingerprint: "a".repeat(64) }); await api.revokeRuntimeApproval("app/one", "approval/one"); await api.resumeJob("job/one");

    expect(fetchMock).toHaveBeenNthCalledWith(4, "/api/v1/apps/app%2Fone/deployments", expect.objectContaining({ method: "POST", headers: expect.objectContaining({ "X-CSRF-Token": "csrf-token", "Idempotency-Key": "setup-request-key" }) }));
    expect(fetchMock).toHaveBeenNthCalledWith(5, "/api/v1/apps/app%2Fone/releases/release%2Fone/deployments", expect.objectContaining({ method: "POST", body: JSON.stringify({ configurationMode: "original" }), headers: expect.objectContaining({ "Idempotency-Key": expect.any(String) }) }));
    expect(fetchMock).toHaveBeenNthCalledWith(6, "/api/v1/apps/app%2Fone/runtime-approvals", expect.objectContaining({ method: "POST", headers: expect.not.objectContaining({ "Idempotency-Key": expect.any(String) }) }));
    expect(fetchMock).toHaveBeenNthCalledWith(8, "/api/v1/jobs/job%2Fone/resume", expect.objectContaining({ method: "POST", headers: expect.objectContaining({ "X-CSRF-Token": "csrf-token" }) }));
  });

  it("sends reviewed revision pins and looks up only the exact deployment request key", async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ created: true, job: { id: "job-1" } }), { status: 202 }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ id: "job-1", status: "queued" }), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);
    const expected = {
      expectedPlanRevisionId: "plan-1", expectedPlanRevisionNumber: 2,
      expectedConfigurationRevisionId: "config-1", expectedConfigurationRevisionNumber: 3,
    };

    await api.deployApplication("app/one", "retry-key", expected);
    await api.deploymentJobByIdempotency("app/one", "retry-key");

    expect(fetchMock).toHaveBeenNthCalledWith(1, "/api/v1/apps/app%2Fone/deployments", expect.objectContaining({
      method: "POST", body: JSON.stringify(expected),
      headers: expect.objectContaining({ "Idempotency-Key": "retry-key", "X-CSRF-Token": "csrf-token" }),
    }));
    expect(fetchMock).toHaveBeenNthCalledWith(2, "/api/v1/apps/app%2Fone/deployment-jobs/by-idempotency", expect.objectContaining({
      cache: "no-store", headers: expect.objectContaining({ "Idempotency-Key": "retry-key" }),
    }));
  });

  it("reads an exact durable job and clears session-scoped attempts on sign out", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({ id: "job-1", status: "succeeded" }), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);
    window.sessionStorage.setItem("rig-setup-deployment:app-1", JSON.stringify({ signature: "plan:1:config:1", key: "key" }));
    window.sessionStorage.setItem("rig-lan-access-grant:app-1", JSON.stringify({ attemptId: "claim" }));
    await expect(api.job("job/one")).resolves.toMatchObject({ id: "job-1", status: "succeeded" });
    expect(fetchMock).toHaveBeenCalledWith("/api/v1/jobs/job%2Fone", expect.objectContaining({ credentials: "same-origin" }));
    clearCSRF();
    expect(window.sessionStorage.getItem("rig-setup-deployment:app-1")).toBeNull();
    expect(window.sessionStorage.getItem("rig-lan-access-grant:app-1")).toBeNull();
  });

  it("uses generated auto-deploy paths with exact CAS request bodies and CSRF", async () => {
    const status = { applicationId: "app/one", revision: 4, enabled: false, state: "disabled", source: { type: "github" }, sourceScopeActive: false, latestResolvedSha: "", activeSha: "", lastSuccessfulDeployedSha: "", pausedSha: "", retryAttempt: 0, updatedAt: "2026-08-26T00:00:00Z" };
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify(status), { status: 200 }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ ...status, enabled: true, revision: 5 }), { status: 200 }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ ...status, enabled: true, state: "idle", revision: 6 }), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);

    await api.getApplicationAutoDeploy("app/one");
    await api.updateApplicationAutoDeploy("app/one", { expectedRevision: 4, enabled: true });
    await api.resumeApplicationAutoDeploy("app/one", { expectedRevision: 5 });

    expect(fetchMock).toHaveBeenNthCalledWith(1, "/api/v1/apps/app%2Fone/auto-deploy", expect.objectContaining({ credentials: "same-origin" }));
    expect(fetchMock).toHaveBeenNthCalledWith(2, "/api/v1/apps/app%2Fone/auto-deploy", expect.objectContaining({ method: "PUT", body: JSON.stringify({ expectedRevision: 4, enabled: true }), headers: expect.objectContaining({ "X-CSRF-Token": "csrf-token" }) }));
    expect(fetchMock).toHaveBeenNthCalledWith(3, "/api/v1/apps/app%2Fone/auto-deploy/resume", expect.objectContaining({ method: "POST", body: JSON.stringify({ expectedRevision: 5 }), headers: expect.objectContaining({ "X-CSRF-Token": "csrf-token" }) }));
  });

  it("gets relay status and preserves safe relay errors", async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ availability: "available", state: "ready", paused: false, outcome: "ready", diagnosticsUnavailable: false, pendingCommands: 0, activeLeases: 0, expiredLeases: 0, oldestPendingAgeSeconds: 0, observerDropped: 0 }), { status: 200 }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ code: "relay_unavailable", detail: "Relay is unavailable" }), { status: 503 }));
    vi.stubGlobal("fetch", fetchMock);
    await expect(api.relayStatus()).resolves.toMatchObject({ availability: "available" });
    await expect(api.relayStatus()).rejects.toEqual(expect.objectContaining<Partial<APIError>>({ code: "relay_unavailable", detail: "Relay is unavailable" }));
    expect(fetchMock).toHaveBeenCalledWith("/api/v1/relay/status", expect.objectContaining({ credentials: "same-origin" }));
  });

  it("uses generated relay mutation paths, exact bodies, encoding, and CSRF", async () => {
    const enrollment = { connectionId: "a".repeat(32), installationId: 2, repositoryId: 3 };
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({ enrollmentId: "enrollment", authorizationUrl: "https://github.com/login/oauth/authorize", status: "pending", expiresAt: "2026-08-27T12:00:00Z" }), { status: 201 }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ enrollmentId: "enrollment", status: "pending", createdAt: "2026-08-27T11:00:00Z", expiresAt: "2026-08-27T12:00:00Z", updatedAt: "2026-08-27T11:00:00Z" }), { status: 202 }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ bindingId: "binding", state: "removal_pending", updatedAt: "2026-08-27T11:00:00Z" }), { status: 202 }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ rotationId: "rotation", state: "prepare", expiresAt: "2026-08-27T12:00:00Z" }), { status: 202 }));
    vi.stubGlobal("fetch", fetchMock);

    await api.startRelayEnrollment(enrollment);
    await api.pollRelayEnrollment("enrollment/one");
    await api.removeRelayBinding("binding/one");
    await api.startRelayKeyRotation();

    expect(fetchMock).toHaveBeenNthCalledWith(1, "/api/v1/relay/enrollments", expect.objectContaining({ method: "POST", body: JSON.stringify(enrollment), headers: expect.objectContaining({ "X-CSRF-Token": "csrf-token" }) }));
    expect(fetchMock).toHaveBeenNthCalledWith(2, "/api/v1/relay/enrollments/enrollment%2Fone/poll", expect.objectContaining({ method: "POST", headers: expect.objectContaining({ "X-CSRF-Token": "csrf-token" }) }));
    expect(fetchMock).toHaveBeenNthCalledWith(3, "/api/v1/relay/bindings/binding%2Fone", expect.objectContaining({ method: "DELETE", headers: expect.objectContaining({ "X-CSRF-Token": "csrf-token" }) }));
    expect(fetchMock).toHaveBeenNthCalledWith(4, "/api/v1/relay/key-rotations", expect.objectContaining({ method: "POST", headers: expect.objectContaining({ "X-CSRF-Token": "csrf-token" }) }));
  });
});

describe("LAN API client", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
    setCSRF("csrf-token");
  });

  it("uses generated LAN endpoints with exact review bodies and CSRF", async () => {
    const profileSpec = { interfaceId: "ethernet/one", selectedIpv4: "192.168.50.4", portStart: 8100, portEnd: 8103 };
    const profile = { expectedRevisionNumber: 2, candidates: [] };
    const access = { expectedRevisionNumber: 0, availability: "local_only" };
    const claim = { attemptId: "22222222-2222-4222-8222-222222222222", appId: "app", allocationId: "allocation", ownerOperationId: "operation", accessRevisionId: "access", accessRevisionNumber: 1, port: 8100, state: "committed", updatedAt: "2026-10-01T00:00:00Z" };
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify(profile), { status: 200 }))
      .mockResolvedValueOnce(new Response(JSON.stringify(profile), { status: 200 }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ profile: { id: "profile", revisionNumber: 3 }, created: true }), { status: 201 }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ observed: { availability: "unknown" } }), { status: 200 }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ claim, created: true }), { status: 201 }))
      .mockResolvedValueOnce(new Response(JSON.stringify(access), { status: 200 }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ allocation: { id: "allocation" }, approvalDigest: "a".repeat(64), created: true }), { status: 201 }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ revision: { id: "access" }, created: true }), { status: 201 }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ claim, created: true }), { status: 201 }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ claim, observed: { availability: "committed" } }), { status: 200 }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ claim: { operationId: "disable" }, created: true }), { status: 201 }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ claim: { operationId: "disable" }, observed: { availability: "disabled" } }), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);

    const profileRequest = { operationId: "11111111-1111-4111-8111-111111111111", expectedRevisionNumber: 2, spec: profileSpec, approvalDigest: "b".repeat(64) };
    const upgradeRequest = { operationId: "33333333-3333-4333-8333-333333333333", profileRevisionId: "profile", profileRevisionNumber: 3, actionDigest: "c".repeat(64) };
    const reserveRequest = { operationId: "44444444-4444-4444-8444-444444444444", expectedRevisionNumber: 0, gatewayProfileRevisionId: "profile", gatewayProfileRevisionNumber: 3 };
    const approveRequest = { operationId: reserveRequest.operationId, allocationId: "allocation", expectedRevisionNumber: 0, approvalDigest: "a".repeat(64) };
    const grantRequest = { attemptId: claim.attemptId, accessRevisionId: "access", accessRevisionNumber: 1, approvalDigest: "d".repeat(64) };
    const disableRequest = { operationId: "55555555-5555-4555-8555-555555555555", accessRevisionId: "access", accessRevisionNumber: 1, allocationId: "allocation", approvalDigest: "e".repeat(64) };

    await api.lanGatewayProfile();
    await api.lanGatewayProfile(profileSpec);
    await api.configureLANGatewayProfile(profileRequest);
    await api.lanGatewayUpgrade();
    await api.upgradeLANGateway(upgradeRequest);
    await api.lanAccess("app/one");
    await api.reserveLANAccess("app/one", reserveRequest);
    await api.approveLANAccess("app/one", approveRequest);
    await api.grantLANAccess("app/one", grantRequest);
    await api.lanGrant("app/one", claim.attemptId);
    await api.disableLANAccess("app/one", disableRequest);
    await api.lanDisable("app/one", disableRequest.operationId);

    expect(fetchMock).toHaveBeenNthCalledWith(1, "/api/v1/system/lan-gateway-profile", expect.objectContaining({ cache: "no-store" }));
    expect(fetchMock).toHaveBeenNthCalledWith(2, "/api/v1/system/lan-gateway-profile?interfaceId=ethernet%2Fone&selectedIpv4=192.168.50.4&portStart=8100&portEnd=8103", expect.objectContaining({ cache: "no-store" }));
    expect(fetchMock).toHaveBeenNthCalledWith(3, "/api/v1/system/lan-gateway-profile", expect.objectContaining({ method: "POST", body: JSON.stringify(profileRequest), headers: expect.objectContaining({ "X-CSRF-Token": "csrf-token" }) }));
    expect(fetchMock).toHaveBeenNthCalledWith(5, "/api/v1/system/lan-gateway-upgrade", expect.objectContaining({ method: "POST", body: JSON.stringify(upgradeRequest), headers: expect.objectContaining({ "X-CSRF-Token": "csrf-token" }) }));
    expect(fetchMock).toHaveBeenNthCalledWith(6, "/api/v1/apps/app%2Fone/lan-access", expect.objectContaining({ cache: "no-store" }));
    expect(fetchMock).toHaveBeenNthCalledWith(7, "/api/v1/apps/app%2Fone/lan-access/reservations", expect.objectContaining({ method: "POST", body: JSON.stringify(reserveRequest), headers: expect.objectContaining({ "X-CSRF-Token": "csrf-token" }) }));
    expect(fetchMock).toHaveBeenNthCalledWith(8, "/api/v1/apps/app%2Fone/lan-access/approval", expect.objectContaining({ method: "POST", body: JSON.stringify(approveRequest), headers: expect.objectContaining({ "X-CSRF-Token": "csrf-token" }) }));
    expect(fetchMock).toHaveBeenNthCalledWith(9, "/api/v1/apps/app%2Fone/lan-access/grants", expect.objectContaining({ method: "POST", body: JSON.stringify(grantRequest), headers: expect.objectContaining({ "X-CSRF-Token": "csrf-token" }) }));
    expect(fetchMock).toHaveBeenNthCalledWith(10, `/api/v1/apps/app%2Fone/lan-access/grants/${claim.attemptId}`, expect.objectContaining({ cache: "no-store" }));
    expect(fetchMock).toHaveBeenNthCalledWith(11, "/api/v1/apps/app%2Fone/lan-access/disables", expect.objectContaining({ method: "POST", body: JSON.stringify(disableRequest), headers: expect.objectContaining({ "X-CSRF-Token": "csrf-token" }) }));
    expect(fetchMock).toHaveBeenNthCalledWith(12, `/api/v1/apps/app%2Fone/lan-access/disables/${disableRequest.operationId}`, expect.objectContaining({ cache: "no-store" }));
  });
});

describe("verified LAN access link guard", () => {
  const access = {
    expectedRevisionNumber: 1,
    availability: "verified",
    observedAt: "2026-10-01T00:00:00Z",
    url: "http://192.168.50.4:8101/",
    desiredAccess: { allocation: { port: 8101, state: "active" } },
  } as LANAppAccessRead;
  const now = Date.parse("2026-10-01T00:00:30Z");

  it("accepts an attested recent private IPv4 address only", () => {
    expect(verifiedLANAccessURL(access, now)).toBe(access.url);
    expect(lanAccessObservationFresh(access, now)).toBe(true);
  });

  it.each([
    ["public host", { url: "http://203.0.113.9:8101/" }],
    ["different allocation port", { url: "http://192.168.50.4:8102/" }],
    ["HTTPS", { url: "https://192.168.50.4:8101/" }],
    ["credentials", { url: "http://user:pass@192.168.50.4:8101/" }],
    ["path", { url: "http://192.168.50.4:8101/admin" }],
    ["query", { url: "http://192.168.50.4:8101/?token=x" }],
    ["inactive allocation", { desiredAccess: { allocation: { port: 8101, state: "uncertain" } } }],
    ["disable claim", { disableClaim: { operationId: "disable", state: "prepared" } }],
    ["expired observation", { observedAt: "2026-09-30T23:58:59Z" }],
  ] as const)("rejects %s", (_name, change) => {
    expect(verifiedLANAccessURL({ ...access, ...change } as LANAppAccessRead, now)).toBeNull();
  });
});

describe("verified local route link guard", () => {
  const now = Date.parse("2026-09-24T19:00:30Z");
  const deployment = {
    id: "deployment-1", status: "succeeded", runtimeStrategy: "generated_node", releaseId: "release-1",
    deploymentPlanRevisionId: "plan-1", deploymentPlanRevisionNumber: 2,
    actualConfigurationRevisionId: "config-1", actualConfigurationRevisionNumber: 3,
  } as Deployment;
  const route = {
    status: "verified", scope: "controller_loopback", url: "http://app-1.rig.localhost:8080/",
    deploymentId: "deployment-1", releaseId: "release-1", planRevisionId: "plan-1", planRevisionNumber: 2,
    configurationRevisionId: "config-1", configurationRevisionNumber: 3,
    observedAt: "2026-09-24T19:00:00Z",
  } as LocalRoute;

  it("accepts the exact recent controller-host route", () => {
    expect(verifiedLocalRouteURL(route, "app-1", deployment, now)).toBe(route.url);
    expect(localRouteObservationFresh(route, now)).toBe(true);
  });

  it.each([
    ["public origin", { url: "https://public.example/" }],
    ["different app", { url: "http://app-2.rig.localhost:8080/" }],
    ["embedded credentials", { url: "http://user:pass@app-1.rig.localhost:8080/" }],
    ["extra path", { url: "http://app-1.rig.localhost:8080/admin" }],
    ["query", { url: "http://app-1.rig.localhost:8080/?token=x" }],
    ["different deployment", { deploymentId: "deployment-2" }],
    ["different release", { releaseId: "release-2" }],
    ["different plan", { planRevisionNumber: 3 }],
    ["different configuration", { configurationRevisionId: "config-2" }],
    ["expired observation", { observedAt: "2026-09-24T18:59:29Z" }],
    ["far-future observation", { observedAt: "2026-09-24T19:00:36Z" }],
  ] as const)("rejects %s", (_name, change) => {
    expect(verifiedLocalRouteURL({ ...route, ...change }, "app-1", deployment, now)).toBeNull();
  });
});
