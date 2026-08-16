import { beforeEach, describe, expect, it, vi } from "vitest";
import { api, setCSRF } from "./api";

describe("API client", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
    setCSRF("csrf-token");
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
