import { beforeEach, describe, expect, it, vi } from "vitest";
import { APIError, api, clearCSRF, setCSRF } from "./api";

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
});
