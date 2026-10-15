import { expect, test, type Page, type Route } from "@playwright/test";
import { spawn, type ChildProcessWithoutNullStreams } from "node:child_process";
import { createServer } from "node:net";
import path from "node:path";
import type { LANGatewayUpgradeRead } from "../src/generated/api-contract";

const webRoot = path.resolve(import.meta.dirname, "..");
const id = (n: number) => `${n.toString(16).padStart(8, "0")}-1111-4111-8111-111111111111`;
const operationId = id(90);
let baseURL = "";
let vite: ChildProcessWithoutNullStreams;

test.beforeAll(async () => {
  const port = await new Promise<number>((resolve, reject) => {
    const server = createServer();
    server.once("error", reject);
    server.listen(0, "127.0.0.1", () => {
      const address = server.address();
      if (!address || typeof address === "string") return server.close(() => reject(new Error("Browser port unavailable")));
      server.close(() => resolve(address.port));
    });
  });
  baseURL = `http://127.0.0.1:${port}`;
  vite = spawn(process.execPath, [path.join(webRoot, "node_modules/vite/bin/vite.js"), "--host", "127.0.0.1", "--port", String(port), "--strictPort"], { cwd: webRoot, windowsHide: true });
  let stderr = "";
  vite.stderr.on("data", (chunk) => { stderr += chunk.toString("utf8"); });
  const deadline = Date.now() + 15_000;
  while (Date.now() < deadline) {
    if (vite.exitCode !== null) throw new Error(`Vite exited: ${stderr}`);
    try { if ((await fetch(baseURL)).ok) return; } catch { /* readiness probe */ }
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
  throw new Error(`Vite did not become ready: ${stderr}`);
});

test.afterAll(async () => {
  if (!vite || vite.exitCode !== null) return;
  vite.kill();
  await new Promise<void>((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error("Browser fixture server did not exit")), 5_000);
    vite.once("exit", () => { clearTimeout(timer); resolve(); });
  });
});

async function commonRoute(route: Route): Promise<boolean> {
  const pathname = new URL(route.request().url()).pathname;
  if (pathname === "/api/v1/auth/bootstrap/status") await route.fulfill({ json: { bootstrapRequired: false } });
  else if (pathname === "/api/v1/auth/me") await route.fulfill({ json: { user: { id: id(99), username: "browser-admin", role: "administrator" } } });
  else if (pathname === "/api/v1/auth/csrf") await route.fulfill({ json: { csrfToken: "browser-csrf-fixture" } });
  else return false;
  return true;
}

async function expectNoPageOverflow(page: Page, width: number) {
  await page.setViewportSize({ width, height: 900 });
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(width);
}

const proposal = { profileRevisionId: id(1), profileRevisionNumber: 1, actionDigest: "b".repeat(64) };
function upgradeRead(terminal: boolean): LANGatewayUpgradeRead {
  return {
    proposal,
    desiredClaim: { operationId, profileRevisionId: proposal.profileRevisionId, profileRevisionNumber: 1,
      profileSpecDigest: "a".repeat(64), state: terminal ? "committed" : "prepared", updatedAt: "2026-10-14T00:00:00Z" },
    observed: { operationId, availability: terminal ? "serving" : "unavailable" },
  };
}

for (const recoveryOutcome of ["terminal", "normal discovery"] as const) {
test(`retains the interrupted upgrade after a lost response and verifies ${recoveryOutcome}`, async ({ page }, info) => {
  let attempted = false;
  let readTerminal = false;
  let restarted = false;
  const posts: unknown[] = [];
  const ordinaryReads: string[] = [];
  let upgradeReads = 0;
  await page.route("**/api/v1/**", async (route) => {
    if (await commonRoute(route)) return;
    const pathname = new URL(route.request().url()).pathname;
    if (pathname === "/api/v1/system/status") return route.fulfill(restarted
      ? { json: { daemon: "running", capabilities: { generatedRuntime: true, fakeRuntime: false, composeRuntime: false, githubConnections: false }, diagnostics: {} } }
      : { status: 503, json: { code: "gateway_reconciliation_required", detail: "Recovery is required." } });
    if (pathname === "/api/v1/system/recovery") return route.fulfill({ json: readTerminal && recoveryOutcome === "normal discovery" ? { version: 1, mode: "normal" } : {
      version: 1, mode: "recovery_only", recovery: { kind: "gateway_upgrade", operationId },
    } });
    if (pathname === "/api/v1/system/lan-gateway-upgrade") {
      if (route.request().method() === "POST") {
        posts.push(route.request().postDataJSON());
        attempted = true;
        return route.abort("connectionfailed");
      }
      expect(route.request().method()).toBe("GET");
      upgradeReads++;
      return route.fulfill(attempted && !readTerminal
        ? { status: 503, json: { code: "lan_gateway_unavailable", detail: "Unavailable" } }
        : { json: upgradeRead(readTerminal) });
    }
    ordinaryReads.push(pathname);
    if (pathname === "/api/v1/apps") return route.fulfill({ json: { items: [] } });
    return route.fulfill({ status: 404, json: { code: "not_found" } });
  });

  await page.goto(baseURL);
  const heading = page.getByRole("heading", { name: "LAN gateway upgrade recovery required", exact: true });
  await expect(heading).toBeVisible();
  await expect(heading).toBeFocused();
  await expect(page).toHaveTitle("LAN gateway upgrade recovery · hostd");
  const review = page.getByRole("button", { name: "Review upgrade recovery", exact: true });
  await expect(review).toBeVisible();
  expect(posts).toEqual([]);
  expect(ordinaryReads).toEqual([]);
  await review.click();
  const consent = page.getByRole("checkbox", { name: "I approve recovery of this gateway upgrade.", exact: true });
  const recover = page.getByRole("button", { name: "Recover this upgrade", exact: true });
  await expect(consent).not.toBeChecked();
  await expect(recover).toBeDisabled();
  for (const width of [375, 768, 1440]) {
    await expectNoPageOverflow(page, width);
    await page.screenshot({ path: info.outputPath(`upgrade-recovery-${width}.png`), fullPage: true });
  }
  await consent.focus();
  await page.keyboard.press("Space");
  await expect(consent).toBeChecked();
  await page.keyboard.press("Tab");
  await expect(page.getByRole("button", { name: "Cancel", exact: true })).toBeFocused();
  await page.keyboard.press("Tab");
  await expect(recover).toBeFocused();
  const readsBeforeSubmit = upgradeReads;
  await page.keyboard.press("Enter");
  await expect(page.getByRole("button", { name: "Check operation status", exact: true })).toBeVisible();
  expect(upgradeReads).toBeGreaterThan(readsBeforeSubmit);
  expect(posts).toEqual([{ operationId, ...proposal }]);
  await expect(consent).toHaveCount(0);
  await expect(recover).toHaveCount(0);
  expect(ordinaryReads).toEqual([]);
  readTerminal = true;
  await page.getByRole("button", { name: "Check operation status", exact: true }).click();
  const checkRestart = page.getByRole("button", { name: "Check system status after restart", exact: true });
  await expect(checkRestart).toBeVisible();
  await expect(heading).toBeVisible();
  expect(posts).toHaveLength(1);
  expect(ordinaryReads).toEqual([]);
  await checkRestart.click();
  await expect(page.getByRole("heading", { name: "Applications", exact: true })).toHaveCount(0);
  // A failed system check remounts the isolated recovery entry. Establish fresh
  // terminal evidence again before offering another explicit restart check.
  if (recoveryOutcome === "terminal") {
    await expect(heading).toBeVisible();
    await review.click();
  }
  await expect(checkRestart).toBeVisible();
  await expect(consent).toHaveCount(0);
  expect(posts).toHaveLength(1);
  expect(ordinaryReads).toEqual([]);
  restarted = true;
  await checkRestart.click();
  await expect(page.getByRole("heading", { name: "Applications", exact: true })).toBeVisible();
  await expect(heading).toHaveCount(0);
  expect(posts).toHaveLength(1);
});
}
