import { expect, test } from "@playwright/test";
import { spawn, type ChildProcessWithoutNullStreams } from "node:child_process";
import { createServer } from "node:net";
import path from "node:path";

const webRoot = path.resolve(import.meta.dirname, "..");
const appId = "11111111-1111-4111-8111-111111111111";
const userId = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa";
const lanURL = "http://192.168.50.4:8100/";
const revision = {
  id: "33333333-3333-4333-8333-333333333333",
  appId,
  revisionNumber: 1,
  operationId: "44444444-4444-4444-8444-444444444444",
  specDigest: "b".repeat(64),
  approvedBy: "browser-admin",
  approvedAt: "2026-10-01T00:00:00Z",
  allocation: {
    id: "55555555-5555-4555-8555-555555555555",
    appId,
    port: 8100,
    ownerOperationId: "44444444-4444-4444-8444-444444444444",
    gatewayProfileRevisionId: "22222222-2222-4222-8222-222222222222",
    gatewayProfileRevisionNumber: 2,
    state: "active",
  },
};
const status = { daemon: "running", capabilities: { generatedRuntime: true, fakeRuntime: false, composeRuntime: false, githubConnections: false }, diagnostics: {} };
let baseURL = "";
let vite: ChildProcessWithoutNullStreams;

async function availablePort(): Promise<number> {
  return new Promise((resolve, reject) => {
    const server = createServer();
    server.once("error", reject);
    server.listen(0, "127.0.0.1", () => {
      const address = server.address();
      if (!address || typeof address === "string") return reject(new Error("No browser test port allocated"));
      server.close(() => resolve(address.port));
    });
  });
}

test.beforeAll(async () => {
  const port = await availablePort();
  baseURL = `http://127.0.0.1:${port}`;
  vite = spawn(process.execPath, [path.join(webRoot, "node_modules", "vite", "bin", "vite.js"), "--host", "127.0.0.1", "--port", String(port), "--strictPort"], {
    cwd: webRoot,
    windowsHide: true,
  });
  let stderr = "";
  vite.stderr.on("data", (chunk) => { stderr += chunk.toString("utf8"); });
  const deadline = Date.now() + 15_000;
  while (Date.now() < deadline) {
    if (vite.exitCode !== null) throw new Error(`Vite exited before startup: ${stderr}`);
    try {
      if ((await fetch(baseURL)).ok) return;
    } catch {
      // Vite may still be starting.
    }
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
  throw new Error(`Vite did not start: ${stderr}`);
});

test.afterAll(async () => {
  if (!vite || vite.exitCode !== null) return;
  vite.kill();
  await new Promise<void>((resolve) => {
    const timeout = setTimeout(resolve, 5_000);
    vite.once("exit", () => { clearTimeout(timeout); resolve(); });
  });
});

test("opens and copies only after a fresh LAN proof, and closes a popup on route drift", async ({ page, context }) => {
  let releaseStatus: (() => void) | undefined;
  let heldStatus: Promise<void> | undefined;
  let drift = false;
  const proofReads: string[] = [];

  await context.route(`${lanURL}**`, (route) => route.fulfill({ contentType: "text/html", body: "<!doctype html><title>LAN test target</title>" }));
  await page.route("**/api/v1/**", async (route) => {
    const url = new URL(route.request().url());
    const pathname = url.pathname;
    if (pathname === "/api/v1/auth/bootstrap/status") return route.fulfill({ json: { bootstrapRequired: false } });
    if (pathname === "/api/v1/auth/me") return route.fulfill({ json: { user: { id: userId, username: "browser-admin", role: "administrator" } } });
    if (pathname === "/api/v1/auth/csrf") return route.fulfill({ json: { csrfToken: "browser-csrf" } });
    if (pathname === "/api/v1/system/status") {
      proofReads.push("status");
      if (heldStatus) await heldStatus;
      return route.fulfill({ json: status });
    }
    if (pathname === `/api/v1/apps/${appId}`) return route.fulfill({ json: { id: appId, name: "Browser LAN fixture", slug: "browser-lan", description: "", machineName: "fixture", status: "ready", createdAt: "2026-10-01T00:00:00Z", source: { type: "local" } } });
    if (pathname === `/api/v1/apps/${appId}/lan-access`) {
      proofReads.push("access");
      return route.fulfill({ json: {
        expectedRevisionNumber: 1,
        availability: "verified",
        desiredAccess: revision,
        observedAt: new Date().toISOString(),
        url: drift ? "http://192.168.50.5:8100/" : lanURL,
      } });
    }
    if (pathname === "/api/v1/system/lan-gateway-profile") return route.fulfill({ json: { expectedRevisionNumber: 2, candidates: [] } });
    if (pathname === `/api/v1/apps/${appId}/deployments`) return route.fulfill({ json: { items: [] } });
    return route.fulfill({ status: 404, json: { code: "not_found" } });
  });

  await page.goto(`${baseURL}/apps/${appId}`);
  const open = page.getByRole("button", { name: "Open LAN address" });
  await expect(open).toBeVisible();
  await page.setViewportSize({ width: 375, height: 800 });
  for (const action of [open, page.getByRole("button", { name: "Copy address" })]) {
    const box = await action.boundingBox();
    expect(box).not.toBeNull();
    expect(box!.x).toBeGreaterThanOrEqual(0);
    expect(box!.x + box!.width).toBeLessThanOrEqual(375);
  }
  heldStatus = new Promise<void>((resolve) => { releaseStatus = resolve; });
  proofReads.length = 0;
  const firstPopupPromise = context.waitForEvent("page");
  await open.focus();
  await open.press("Enter");
  const firstPopup = await firstPopupPromise;
  await expect(page.getByText("Checking LAN address")).toBeVisible();
  await expect(firstPopup).toHaveURL("about:blank");
  await expect(firstPopup).toHaveTitle("Checking LAN address — Rig");
  await expect(firstPopup.getByRole("heading", { name: "Checking LAN address" })).toBeVisible();
  await expect(firstPopup.getByRole("status")).toContainText("proving the current LAN address");
  expect(proofReads).toEqual(["status"]);
  releaseStatus?.();
  heldStatus = undefined;
  await expect(firstPopup).toHaveURL(lanURL);
  await expect(page.locator(".lan-message").getByText("The freshly proved LAN address opened in a new window.")).toBeVisible();
  expect(proofReads).toEqual(["status", "access", "status"]);
  await firstPopup.close();

  await context.grantPermissions(["clipboard-read", "clipboard-write"], { origin: baseURL });
  proofReads.length = 0;
  await page.getByRole("button", { name: "Copy address" }).click();
  await expect(page.locator(".lan-message").getByText("The freshly proved LAN address was copied.")).toBeVisible();
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(lanURL);
  expect(proofReads).toEqual(["status", "access", "status"]);

  await page.evaluate(() => {
    Object.defineProperty(navigator.clipboard, "writeText", {
      configurable: true,
      value: () => Promise.reject(new DOMException("Clipboard permission denied", "NotAllowedError")),
    });
  });
  proofReads.length = 0;
  await page.getByRole("button", { name: "Copy address" }).click();
  await expect(page.getByRole("alert").filter({ hasText: "could not be copied" })).toBeVisible();
  expect(proofReads).toEqual(["status", "access", "status"]);
  await page.getByRole("button", { name: "Check LAN access" }).click();
  await expect(open).toBeVisible();

  drift = true;
  proofReads.length = 0;
  const driftPopupPromise = context.waitForEvent("page");
  await page.getByRole("button", { name: "Open LAN address" }).click();
  const driftPopup = await driftPopupPromise;
  await expect(page.getByRole("alert").filter({ hasText: "LAN address action stopped" })).toBeVisible();
  await expect.poll(() => driftPopup.isClosed()).toBe(true);
  expect(proofReads).toEqual(["status", "access", "status"]);
  await expect(page.locator(".lan-address code")).toHaveCount(0);

  drift = false;
  await page.getByRole("button", { name: "Check LAN access" }).click();
  await expect(open).toBeVisible();
  heldStatus = new Promise<void>((resolve) => { releaseStatus = resolve; });
  proofReads.length = 0;
  const movedPopupPromise = context.waitForEvent("page");
  await open.click();
  const movedPopup = await movedPopupPromise;
  await expect(movedPopup).toHaveURL("about:blank");
  await movedPopup.goto("about:blank#operator-changed-window");
  releaseStatus?.();
  heldStatus = undefined;
  await expect(page.getByRole("alert").filter({ hasText: "new window changed" })).toBeVisible();
  await expect.poll(() => movedPopup.isClosed()).toBe(true);
  expect(proofReads).toEqual(["status", "access", "status"]);
});
