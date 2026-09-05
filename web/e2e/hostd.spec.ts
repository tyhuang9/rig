import { expect, test } from "@playwright/test";
import { spawn, spawnSync, type ChildProcessWithoutNullStreams } from "node:child_process";
import { createServer } from "node:net";
import { access, mkdtemp, mkdir, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import type { JobMutationResponse } from "../src/generated/api-contract";

const repoRoot = path.resolve(import.meta.dirname, "../..");
const screenshotDir = process.env.HOSTD_SCREENSHOT_DIR ?? path.join(repoRoot, "artifacts", "screenshots");
const longName = `Application ${"with-a-very-long-name-".repeat(3)}fixture`;
const composeName = "Compose cancellation fixture";
let daemon: ChildProcessWithoutNullStreams;
let dataRoot = "";
let sourceRoot = "";
let composeSourceRoot = "";
let baseURL = "";
let bootstrapToken = "";
let bootstrapTokenFile = "";

async function availablePort(): Promise<number> {
  return new Promise((resolve, reject) => {
    const server = createServer();
    server.once("error", reject);
    server.listen(0, "127.0.0.1", () => {
      const address = server.address();
      if (!address || typeof address === "string") return reject(new Error("No test port allocated"));
      server.close(() => resolve(address.port));
    });
  });
}

test.beforeAll(async () => {
  dataRoot = await mkdtemp(path.join(tmpdir(), "hostd-e2e-"));
  sourceRoot = await mkdtemp(path.join(tmpdir(), "hostd-e2e-source-"));
  composeSourceRoot = await mkdtemp(path.join(tmpdir(), "hostd-e2e-compose-source-"));
  await writeFile(path.join(sourceRoot, "package.json"), JSON.stringify({
    name: "hostd-e2e-fixture",
    packageManager: "npm@11",
    scripts: { build: "vite build" },
    dependencies: { vite: "6" },
  }), "utf8");
  await writeFile(path.join(sourceRoot, "package-lock.json"), JSON.stringify({ lockfileVersion: 3 }), "utf8");
  await writeFile(path.join(composeSourceRoot, "compose.yaml"), "services:\n  web:\n    image: nginx:alpine\n", "utf8");
  const binary = path.join(dataRoot, process.platform === "win32" ? "hostd-e2e.exe" : "hostd-e2e");
  const hostctl = path.join(dataRoot, process.platform === "win32" ? "hostctl-e2e.exe" : "hostctl-e2e");
  const build = spawnSync("go", ["build", "-o", binary, "./cmd/hostd"], { cwd: repoRoot, encoding: "utf8" });
  if (build.status !== 0) throw new Error(`hostd build failed: ${build.stderr}`);
  const ctlBuild = spawnSync("go", ["build", "-o", hostctl, "./cmd/hostctl"], { cwd: repoRoot, encoding: "utf8" });
  if (ctlBuild.status !== 0) throw new Error(`hostctl build failed: ${ctlBuild.stderr}`);
  const port = await availablePort();
  baseURL = `http://127.0.0.1:${port}`;
  daemon = spawn(binary, ["--data-root", dataRoot, "--listen", `127.0.0.1:${port}`, "--fake-runtime"], { cwd: repoRoot, windowsHide: true });
  let stderr = "";
  let stdout = "";
  daemon.stderr.on("data", (chunk) => {
    stderr += chunk.toString("utf8");
  });
  daemon.stdout.on("data", (chunk) => {
    stdout += chunk.toString("utf8");
    const pathLine = stdout
      .split(/\r?\n/)
      .map((line) => line.trim())
      .find((line) => line.endsWith("bootstrap-token.secret"));
    if (!pathLine || bootstrapToken) return;
    bootstrapTokenFile = pathLine;
    const read = spawnSync(hostctl, ["bootstrap-token", "--file", bootstrapTokenFile], { cwd: repoRoot, encoding: "utf8" });
    if (read.status === 0) bootstrapToken = read.stdout.trim();
  });
  const deadline = Date.now() + 20_000;
  while (Date.now() < deadline) {
    try {
      const response = await fetch(`${baseURL}/api/v1/auth/bootstrap/status`);
      if (response.ok && bootstrapToken) return;
    } catch {
      // The process may still be starting.
    }
    await new Promise((resolve) => setTimeout(resolve, 150));
  }
  throw new Error(`hostd did not start or expose a protected bootstrap token file. stdout: ${stdout}; stderr: ${stderr}`);
});

test.afterAll(async () => {
  if (daemon && !daemon.killed) {
    daemon.kill();
    await new Promise((resolve) => daemon.once("exit", resolve));
  }
  if (dataRoot) await rm(dataRoot, { recursive: true, force: true });
  if (sourceRoot) await rm(sourceRoot, { recursive: true, force: true });
  if (composeSourceRoot) await rm(composeSourceRoot, { recursive: true, force: true });
});

test("bootstraps, restores a fresh tab, cancels work, and stays responsive", async ({ page, context }) => {
  const browserErrors: string[] = [];
  const watchForErrors = (target: typeof page) => {
    target.on("console", (message) => {
      if (message.type() !== "error") return;
      const expectedLegacyPlanMiss = message.text().includes("status of 404") && /\/api\/v1\/apps\/[^/]+\/deployment-plan$/.test(message.location().url);
      if (!expectedLegacyPlanMiss) browserErrors.push(message.text());
    });
    target.on("pageerror", (error) => browserErrors.push(error.message));
  };
  watchForErrors(page);

  await page.goto(`${baseURL}/apps`);
  await expect(page).toHaveTitle("hostd");
  await page.getByRole("button", { name: "Create administrator" }).click();
  await expect(page.locator(".error-summary")).toBeFocused();
  await expect(page.getByLabel("Username")).toHaveAttribute("aria-invalid", "true");

  await page.getByLabel("Bootstrap token").fill(bootstrapToken);
  await page.getByLabel("Username").fill("playwright-admin");
  await page.getByLabel("Passphrase").fill("a safe local browser test passphrase");
  await page.getByRole("button", { name: "Create administrator" }).click();
  await expect(page.getByRole("heading", { name: "Applications", exact: true })).toBeVisible();
  await expect.poll(async () => {
    try {
      await access(bootstrapTokenFile);
      return true;
    } catch {
      return false;
    }
  }).toBe(false);
  await expect(page).toHaveTitle("Applications · hostd");

  await page.getByRole("button", { name: "Sign out" }).click();
  await expect(page.getByRole("heading", { name: "Welcome back" })).toBeVisible();
  await page.getByLabel("Username").fill("playwright-admin");
  await page.getByLabel("Passphrase").fill("a safe local browser test passphrase");
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page.getByRole("heading", { name: "Applications", exact: true })).toBeFocused();

  await page.getByRole("link", { name: "Add application" }).first().click();
  await expect(page.getByRole("heading", { name: "Add application" })).toBeFocused();
  await expect(page.getByRole("button", { name: "Save application" })).toBeDisabled();
  await page.getByLabel("Description").fill("A deterministic browser fixture");
  await page.getByLabel("Local source path").fill(sourceRoot);
  await page.getByRole("button", { name: "Analyze project" }).click();
  await expect(page.getByRole("heading", { name: "How Rig will run this app" })).toBeFocused();
  await page.getByRole("button", { name: "Accept setup" }).click();
  await expect(page.locator(".error-summary")).toBeFocused();
  await expect(page.getByLabel("Application name")).toHaveAttribute("aria-invalid", "true");
  await page.getByLabel("Application name").fill(longName);
  await page.getByRole("button", { name: "Review setup" }).click();
  await expect(page.getByRole("heading", { name: "How Rig will run this app" })).toBeFocused();
  await page.getByRole("button", { name: "Accept setup" }).click();
  await expect(page.getByRole("heading", { name: "Setup accepted" })).toBeFocused();
  await page.getByRole("button", { name: "Open application" }).click();
  await expect(page.getByRole("heading", { name: longName })).toBeVisible();
  const restoredPage = await context.newPage();
  watchForErrors(restoredPage);
  const csrfRestore = restoredPage.waitForResponse((response) => response.url().endsWith("/api/v1/auth/csrf"));
  await restoredPage.goto(page.url());
  expect((await csrfRestore).ok()).toBe(true);
  await expect(restoredPage.getByRole("heading", { name: longName })).toBeVisible();
  expect(await restoredPage.evaluate(() => window.sessionStorage.getItem("hostd-csrf"))).toBeTruthy();
  await expect(restoredPage.getByText("Development capability")).toBeVisible();
  await expect(restoredPage.getByRole("button", { name: "Deploy latest" })).toBeDisabled();
  await expect(restoredPage.getByText("Deploy latest requires the generated runtime on this controller.")).toBeVisible();

  await restoredPage.getByRole("link", { name: "Applications" }).click();
  await restoredPage.getByRole("link", { name: "Add application" }).first().click();
  await restoredPage.getByLabel("Application name").fill(composeName);
  await restoredPage.getByLabel("Local source path").fill(composeSourceRoot);
  await restoredPage.getByRole("button", { name: "Analyze project" }).click();
  await expect(restoredPage.getByText("Source inspection completed")).toBeVisible();
  await restoredPage.getByRole("button", { name: "Save application" }).click();
  await expect(restoredPage.getByRole("heading", { name: composeName })).toBeVisible();
  await expect(restoredPage.getByRole("button", { name: "Deploy latest" })).toBeEnabled();
  const deployResponse = restoredPage.waitForResponse((response) =>
    response.request().method() === "POST" && new URL(response.url()).pathname.endsWith("/deployments"),
  );
  await restoredPage.getByRole("button", { name: "Deploy latest" }).click();
  const deployHTTPResponse = await deployResponse;
  expect(deployHTTPResponse.status()).toBe(202);
  const deployment = await deployHTTPResponse.json() as JobMutationResponse;
  expect(deployment.job.id).toMatch(/^[0-9a-f-]{36}$/);
  expect(await restoredPage.evaluate(() => window.sessionStorage.getItem("hostd-csrf"))).toBeTruthy();

  await restoredPage.getByRole("link", { name: "Activity" }).click();
  await expect(restoredPage.getByRole("heading", { name: "Activity" })).toBeVisible();
  const activity = restoredPage.locator(".activity-row").filter({ hasText: "deploy application" }).first();
  await expect(activity.getByRole("button", { name: "Cancel job" })).toBeVisible();
  await activity.getByRole("button", { name: "Cancel job" }).click();
  const cancellationStatus = activity.getByRole("status");
  await expect(cancellationStatus).toHaveText("Cancellation recorded. Job cancelled.");
  await expect(cancellationStatus).toHaveAttribute("aria-live", "polite");
  await expect(cancellationStatus).toHaveAttribute("aria-atomic", "true");
  await expect(activity.getByText("cancelled", { exact: true })).toBeVisible();
  await expect(activity.locator("button")).toHaveCount(0);
  await restoredPage.getByRole("link", { name: "Machines" }).click();
  await expect(restoredPage.getByRole("heading", { name: "Machines" })).toBeVisible();
  await expect(restoredPage.getByText("Local controller · independent runtime diagnostics", { exact: true })).toBeVisible();
  await restoredPage.getByRole("link", { name: "Applications" }).click();
  await expect(restoredPage.getByText(longName)).toBeVisible();

  await mkdir(screenshotDir, { recursive: true });
  for (const [width, height] of [[375, 812], [768, 900], [1024, 900], [1440, 900]] as const) {
    await restoredPage.setViewportSize({ width, height });
    await expect(restoredPage.getByText(longName)).toBeVisible();
    const overflow = await restoredPage.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
    expect(overflow, `horizontal overflow at ${width}px`).toBeLessThanOrEqual(0);
    if (width === 768 || width === 1024) await expect(restoredPage.getByText("System ready")).toBeHidden();
    if (width === 375) {
      await expect(restoredPage.getByRole("link", { name: "Applications" })).toBeVisible();
      await expect(restoredPage.getByRole("link", { name: "Machines" })).toBeVisible();
      await expect(restoredPage.getByRole("link", { name: "Activity" })).toBeVisible();
    }
    if (width === 768) {
      await expect(restoredPage.getByText("Machine", { exact: true }).first()).toBeVisible();
      await expect(restoredPage.getByText("Release", { exact: true }).first()).toBeVisible();
      await expect(restoredPage.getByText("Created", { exact: true }).first()).toBeVisible();
    }
    await restoredPage.screenshot({ path: path.join(screenshotDir, `apps-${width}.png`), fullPage: true });
  }
  expect(browserErrors).toEqual([]);
});
