import { expect, test, type Page, type Route } from "@playwright/test";
import { spawn, type ChildProcessWithoutNullStreams } from "node:child_process";
import { createServer } from "node:net";
import path from "node:path";
import type { InspectLANGatewayRebindRequest, LANGatewayRebindReview, LANGatewayRebindStatus } from "../src/generated/api-contract";

const webRoot = path.resolve(import.meta.dirname, "..");
const id = (n: number) => `${n.toString(16).padStart(8, "0")}-1111-4111-8111-111111111111`;
const digest = "a".repeat(64);
const operationId = id(90);
const profile = {
  id: id(1), revisionNumber: 1, operationId: id(2), specDigest: digest,
  approvedAt: "2026-10-14T00:00:00Z", approvedBy: "browser-admin",
  spec: { interfaceId: "ethernet", selectedIpv4: "192.168.50.4", portStart: 8100, portEnd: 8119 },
};
const profileRead = {
  expectedRevisionNumber: 1, desiredProfile: profile,
  candidates: [
    { interfaceId: "ethernet", name: "Ethernet", selectedIpv4: "192.168.50.4", prefix: 24 },
    { interfaceId: "wifi", name: "Wi-Fi", selectedIpv4: "192.168.50.5", prefix: 24 },
  ],
};
const normalStatus = {
  daemon: "running",
  capabilities: { generatedRuntime: true, fakeRuntime: false, composeRuntime: false, githubConnections: false },
  diagnostics: {},
};

function rebindStatus(op: string, terminal = false): LANGatewayRebindStatus {
  return {
    version: 1, operationId: op, availability: "available", active: !terminal,
    historicalTerminal: terminal, currentProof: "unavailable",
    terminalProof: "unavailable", operationFence: terminal ? "released" : "held",
    allowedActions: [], recoveryPolicy: terminal ? "forward_only" : "evidence_decides",
    operation: {
      operationId: op, storedState: terminal ? "committed" : "prepared", stateSequence: terminal ? 5 : 1,
      databaseCommitObserved: terminal, ...(terminal ? { terminalDisposition: "commit", terminalReceiptDigest: digest } : {}),
    },
  };
}

function rebindReview(input: InspectLANGatewayRebindRequest): LANGatewayRebindReview {
  const roster = [1, 2].map((ordinal) => ({
    version: 2, operationId: input.operationId, ordinal, appId: id(10 + ordinal),
    allocationId: id(20 + ordinal), port: 8099 + ordinal, allocationOwnerOperationId: id(30 + ordinal),
    allocationState: "active", accessRevisionId: id(40 + ordinal), accessRevisionNumber: 1,
    accessSpecDigest: digest, grantAttemptId: id(50 + ordinal), grantStateSequence: 1,
    grantProtectedStateDigest: digest, sourceProfileRevisionId: profile.id, sourceProfileRevisionNumber: 1,
    sourceProfileSpecDigest: digest, servingDeploymentId: id(60 + ordinal), servingReleaseId: id(70 + ordinal),
    servingSlot: "blue", routeGeneration: 1,
  }));
  return {
    version: 1, proposalDigest: digest, configureApprovalDigest: "b".repeat(64), rebindApprovalDigest: "c".repeat(64),
    spec: {
      version: 2, ...input, successorProtectedGeneration: 1,
      predecessor: {
        sourceStateVersion: 2, sourceStateRevision: 1, sourceStateDigest: digest, predecessorCheckpointDigest: digest,
        lineage: { kind: "gateway_upgrade", operationId: id(3), profileRevisionId: profile.id,
          profileRevisionNumber: 1, profileSpecDigest: digest, protectedGeneration: 0, protectedIdentityDigest: digest },
      },
      rosterCount: 2, rosterVersion: 2, rosterDigest: digest,
      runtimeHeadsCount: 2, runtimeHeadsVersion: 1, runtimeHeadsDigest: digest,
    },
    roster,
    runtimeHeads: roster.map((entry) => ({ appId: entry.appId, deploymentId: entry.servingDeploymentId,
      releaseId: entry.servingReleaseId, slot: "blue", generation: 1, updatedAt: "2026-10-14T00:00:00Z" })),
  };
}

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

test("fresh-tab rebind recovery discovers the pin, requires fresh consent and keeps terminal isolation", async ({ page }, info) => {
  let terminal = false;
  let restarted = false;
  let reviews = 0;
  const reconciles: unknown[] = [];
  const normalReads: string[] = [];
  const statusReads: string[] = [];
  const rebindPath = `/api/v1/system/lan-gateway-rebind/${operationId}`;
  await page.route("**/api/v1/**", async (route) => {
    if (await commonRoute(route)) return;
    const pathname = new URL(route.request().url()).pathname;
    if (pathname === "/api/v1/system/status") return route.fulfill(restarted
      ? { json: normalStatus }
      : { status: 503, json: { code: "gateway_reconciliation_required", detail: "Recovery is required." } });
    if (pathname === "/api/v1/system/recovery") return route.fulfill({ json: {
      version: 1, mode: "recovery_only", recovery: { kind: "gateway_rebind", operationId },
    } });
    if (pathname === rebindPath) {
      statusReads.push(pathname);
      return route.fulfill({ json: rebindStatus(operationId, terminal) });
    }
    if (pathname === `${rebindPath}/recovery-proposal`) {
      reviews++;
      expect(route.request().postDataJSON()).toEqual({});
      return route.fulfill({ json: { version: 1, operationId, status: { ...rebindStatus(operationId), allowedActions: ["reconcile"] },
        recoveryDigest: digest, recoveryApprovalDigest: "b".repeat(64) } });
    }
    if (pathname === `${rebindPath}/reconcile`) {
      reconciles.push(route.request().postDataJSON());
      if (reconciles.length === 1) return route.fulfill({ status: 409, json: { code: "lan_gateway_conflict", detail: "Review changed." } });
      terminal = true;
      return route.fulfill({ json: { version: 1, status: rebindStatus(operationId, true) } });
    }
    normalReads.push(pathname);
    if (pathname === "/api/v1/apps") return route.fulfill({ json: { items: [] } });
    return route.fulfill({ status: 404, json: { code: "not_found" } });
  });

  await page.goto(baseURL);
  const heading = page.getByRole("heading", { name: "LAN gateway recovery required" });
  await expect(heading).toBeVisible();
  await expect(heading).toBeFocused();
  await expect(page.getByRole("button", { name: "Review recovery", exact: true })).toBeVisible();
  expect(reconciles).toEqual([]);
  expect(reviews).toBe(0);
  expect(normalReads).toEqual([]);
  // StrictMode can repeat the mount read; every read must retain the exact pin.
  expect(statusReads.length).toBeGreaterThan(0);
  expect(new Set(statusReads)).toEqual(new Set([rebindPath]));
  for (const width of [375, 768, 1440]) {
    await expectNoPageOverflow(page, width);
    await page.screenshot({ path: info.outputPath(`rebind-recovery-${width}.png`), fullPage: true });
  }
  await page.getByRole("button", { name: "Review recovery", exact: true }).click();
  const consent = page.getByRole("checkbox", { name: "I approve recovery of this gateway operation." });
  const recover = page.getByRole("button", { name: "Recover this operation", exact: true });
  await expect(consent).not.toBeChecked();
  await expect(recover).toBeDisabled();
  await consent.check();
  await recover.click();
  await expect(page.getByRole("alert")).toContainText("changed");
  await expect(consent).toHaveCount(0);
  expect(reconciles).toHaveLength(1);
  await page.getByRole("button", { name: "Check operation status", exact: true }).click();
  await page.getByRole("button", { name: "Review recovery", exact: true }).click();
  await expect(consent).not.toBeChecked();
  await consent.check();
  await recover.click();
  const afterRestart = page.getByRole("button", { name: "Check system status after restart", exact: true });
  await expect(afterRestart).toBeVisible();
  expect(reconciles).toEqual([1, 2].map(() => ({ version: 1, action: "reconcile", recoveryDigest: digest, recoveryApprovalDigest: "b".repeat(64) })));
  expect(normalReads).toEqual([]);
  await afterRestart.click();
  await expect(heading).toBeVisible();
  expect(normalReads).toEqual([]);
  restarted = true;
  await page.getByRole("button", { name: "Check system status after restart", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Applications", exact: true })).toBeVisible();
  await expect(heading).toHaveCount(0);
  expect(reconciles).toHaveLength(2);
});

test("gateway change reviews the full roster and preserves a lost-response operation across refresh", async ({ page }, info) => {
  let submitted = "";
  let statusUnavailable = true;
  const commits: unknown[] = [];
  const reads: string[] = [];
  let otherMutations = 0;
  await page.route("**/api/v1/**", async (route) => {
    if (await commonRoute(route)) return;
    const pathname = new URL(route.request().url()).pathname;
    const method = route.request().method();
    if (pathname === "/api/v1/system/status") return route.fulfill({ json: normalStatus });
    if (pathname === "/api/v1/system/recovery") return route.fulfill({ json: { version: 1, mode: "normal" } });
    if (pathname === "/api/v1/system/lan-gateway-profile") {
      if (method !== "GET") otherMutations++;
      return route.fulfill({ json: profileRead });
    }
    if (pathname === "/api/v1/system/lan-gateway-upgrade") return route.fulfill({ json: {
      desiredClaim: { operationId: id(3), profileRevisionId: profile.id, profileRevisionNumber: 1,
        profileSpecDigest: digest, state: "committed", updatedAt: "2026-10-14T00:00:00Z" },
      observed: { availability: "serving", operationId: id(3) },
    } });
    if (pathname === "/api/v1/system/lan-gateway-rebind/proposal") return route.fulfill({ json: rebindReview(route.request().postDataJSON()) });
    if (pathname === "/api/v1/system/lan-gateway-rebind") {
      const body = route.request().postDataJSON();
      submitted = body.operationId;
      commits.push(body);
      return route.abort("connectionfailed");
    }
    if (submitted && pathname === `/api/v1/system/lan-gateway-rebind/${submitted}`) {
      reads.push(pathname);
      return route.fulfill(statusUnavailable ? { status: 503, json: { code: "lan_gateway_unavailable" } } : { json: rebindStatus(submitted, true) });
    }
    if (pathname === "/api/v1/apps") return route.fulfill({ json: { items: [1, 2].map((n) => ({ id: id(10 + n), name: `Roster application ${n}`, slug: `roster-${n}`, status: "ready", createdAt: "2026-10-14T00:00:00Z", source: { type: "local" } })) } });
    if (pathname === "/api/v1/machines") return route.fulfill({ json: { items: [] } });
    return route.fulfill({ status: 404, json: { code: "not_found" } });
  });

  await page.goto(`${baseURL}/machines`);
  const panel = page.getByRole("region", { name: "Change LAN gateway", exact: true });
  const reviewButton = panel.getByRole("button", { name: "Review gateway change", exact: true });
  await expect(reviewButton).toBeVisible();
  await panel.getByLabel("Interface and IPv4 address", { exact: true }).selectOption({ label: "Wi-Fi · 192.168.50.5/24" });
  await reviewButton.click();
  const dialog = page.getByRole("dialog", { name: "Review gateway change", exact: true });
  await expect(dialog).toBeVisible();
  await expect(dialog.getByRole("heading", { name: "Review gateway change", exact: true })).toBeFocused();
  await expect(dialog.locator(".lan-rebind-roster > ol > li")).toHaveCount(2);
  for (const width of [375, 768, 1440]) {
    await expectNoPageOverflow(page, width);
    const bounds = await dialog.boundingBox();
    expect(bounds!.x).toBeGreaterThanOrEqual(0);
    expect(bounds!.x + bounds!.width).toBeLessThanOrEqual(width);
    await page.screenshot({ path: info.outputPath(`rebind-review-${width}.png`), fullPage: true });
  }
  await page.keyboard.press("Escape");
  await expect(dialog).toHaveCount(0);
  await expect(reviewButton).toBeFocused();
  await reviewButton.press("Enter");
  const apply = dialog.getByRole("button", { name: "Apply gateway change", exact: true });
  await expect(apply).toBeDisabled();
  await dialog.getByRole("checkbox", { name: "I approve these new gateway settings." }).check();
  await expect(apply).toBeDisabled();
  await dialog.getByRole("checkbox", { name: "I approve moving all 2 listed applications to this gateway." }).check();
  await apply.click();
  await expect(panel.getByRole("button", { name: "Check operation status", exact: true })).toBeVisible();
  expect(commits).toHaveLength(1);
  expect(otherMutations).toBe(0);
  expect(new URL(page.url()).searchParams.get("gatewayRebindOperation")).toBe(submitted);
  await page.reload();
  await expect(panel.getByRole("alert")).toContainText("could not confirm");
  expect(reads).toEqual([`/api/v1/system/lan-gateway-rebind/${submitted}`]);
  await expect(reviewButton).toHaveCount(0);
  statusUnavailable = false;
  await panel.getByRole("button", { name: "Check operation status", exact: true }).click();
  await expect(panel.getByRole("heading", { name: "Gateway change status", exact: true })).toBeVisible();
  expect(reads).toHaveLength(2);
  expect(commits).toHaveLength(1);
  expect(otherMutations).toBe(0);
  expect(new URL(page.url()).searchParams.get("gatewayRebindOperation")).toBe(submitted);
  await panel.getByRole("button", { name: "Return to gateway review", exact: true }).click();
  await expect(reviewButton).toBeVisible();
  expect(new URL(page.url()).searchParams.has("gatewayRebindOperation")).toBe(false);
  expect(commits).toHaveLength(1);
  await expect(panel.getByRole("heading", { name: "Gateway change status", exact: true })).toBeVisible();
});

test("a fresh normal tab discovers an active gateway operation despite a failed profile read", async ({ page }) => {
  const posts: string[] = [];
  const exactReads: string[] = [];
  await page.route("**/api/v1/**", async (route) => {
    if (await commonRoute(route)) return;
    const pathname = new URL(route.request().url()).pathname;
    if (route.request().method() !== "GET") posts.push(pathname);
    if (pathname === "/api/v1/system/status") return route.fulfill({ json: normalStatus });
    if (pathname === "/api/v1/system/recovery") return route.fulfill({ json: {
      version: 1, mode: "normal", recovery: { kind: "gateway_rebind", operationId },
    } });
    if (pathname === "/api/v1/system/lan-gateway-profile") return route.fulfill({ status: 503, json: { code: "lan_gateway_unavailable" } });
    if (pathname === "/api/v1/system/lan-gateway-upgrade") return route.fulfill({ json: { observed: { availability: "unknown" } } });
    if (pathname === `/api/v1/system/lan-gateway-rebind/${operationId}`) {
      exactReads.push(pathname);
      return route.fulfill({ json: rebindStatus(operationId) });
    }
    if (pathname === "/api/v1/apps" || pathname === "/api/v1/machines") return route.fulfill({ json: { items: [] } });
    return route.fulfill({ status: 404, json: { code: "not_found" } });
  });
  await page.goto(`${baseURL}/machines`);
  await page.getByRole("button", { name: "Open gateway recovery", exact: true }).click();
  await expect(page.getByRole("heading", { name: "LAN gateway recovery required" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Review recovery", exact: true })).toBeVisible();
  expect(exactReads.length).toBeGreaterThan(0);
  expect(new Set(exactReads)).toEqual(new Set([`/api/v1/system/lan-gateway-rebind/${operationId}`]));
  expect(posts).toEqual([]);
});
