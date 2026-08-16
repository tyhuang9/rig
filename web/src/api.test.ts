import { beforeEach, describe, expect, it, vi } from "vitest";
import { api, clearCSRF, setCSRF } from "./api";

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
});
