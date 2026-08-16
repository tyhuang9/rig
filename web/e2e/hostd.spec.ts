import { expect, test } from "@playwright/test";
import { spawn, spawnSync, type ChildProcessWithoutNullStreams } from "node:child_process";
import { createServer } from "node:net";
import { mkdtemp, mkdir, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";

const repoRoot = path.resolve(import.meta.dirname, "../..");
const screenshotDir = process.env.HOSTD_SCREENSHOT_DIR ?? path.join(repoRoot, "artifacts", "screenshots");
const longName = `Application ${"with-a-very-long-name-".repeat(3)}fixture`;
let daemon: ChildProcessWithoutNullStreams;
let dataRoot = "";
let baseURL = "";
let bootstrapToken = "";

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
  const binary = path.join(dataRoot, process.platform === "win32" ? "hostd-e2e.exe" : "hostd-e2e");
  const build = spawnSync("go", ["build", "-o", binary, "./cmd/hostd"], { cwd: repoRoot, encoding: "utf8" });
  if (build.status !== 0) throw new Error(`hostd build failed: ${build.stderr}`);
  const port = await availablePort();
  baseURL = `http://127.0.0.1:${port}`;
  daemon = spawn(binary, ["--data-root", dataRoot, "--listen", `127.0.0.1:${port}`, "--fake-runtime"], { cwd: repoRoot, windowsHide: true });
  let stderr = "";
  daemon.stderr.on("data", (chunk) => {
    stderr += chunk.toString("utf8");
    const match = stderr.match(/HOSTD BOOTSTRAP TOKEN \(sensitive, one-time, expires in 15 minutes\): ([A-Za-z0-9_-]+)/);
    if (match) bootstrapToken = match[1];
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
  const redactedStderr = stderr.replace(
    /HOSTD BOOTSTRAP TOKEN \(sensitive, one-time, expires in 15 minutes\): [A-Za-z0-9_-]+/g,
    "HOSTD BOOTSTRAP TOKEN: [REDACTED]",
  );
  throw new Error(`hostd did not start or expose a bootstrap token. stderr: ${redactedStderr}`);
});

test.afterAll(async () => {
  if (daemon && !daemon.killed) {
    daemon.kill();
    await new Promise((resolve) => daemon.once("exit", resolve));
  }
  if (dataRoot) await rm(dataRoot, { recursive: true, force: true });
});

test("bootstraps, restores a fresh tab, cancels work, and stays responsive", async ({ page, context }) => {
  const browserErrors: string[] = [];
  const watchForErrors = (target: typeof page) => {
    target.on("console", (message) => { if (message.type() === "error") browserErrors.push(message.text()); });
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
  await expect(page).toHaveTitle("Applications · hostd");

  await page.getByRole("button", { name: "Sign out" }).click();
  await expect(page.getByRole("heading", { name: "Welcome back" })).toBeVisible();
  await page.getByLabel("Username").fill("playwright-admin");
  await page.getByLabel("Passphrase").fill("a safe local browser test passphrase");
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page.getByRole("heading", { name: "Applications", exact: true })).toBeFocused();

  await page.getByRole("link", { name: "Add application" }).first().click();
  await expect(page.getByRole("heading", { name: "Add application" })).toBeFocused();
  await page.getByRole("button", { name: "Save application" }).click();
  await expect(page.locator(".error-summary")).toBeFocused();
  await expect(page.getByLabel("Application name")).toHaveAttribute("aria-invalid", "true");
  await page.getByLabel("Application name").fill(longName);
  await page.getByLabel("Description").fill("A deterministic browser fixture");
  await page.getByLabel("Local source path").fill("C:\\fixtures\\hostd-e2e");
  await page.getByRole("button", { name: "Save application" }).click();
  await expect(page.getByRole("heading", { name: longName })).toBeVisible();
  const restoredPage = await context.newPage();
  watchForErrors(restoredPage);
  const csrfRestore = restoredPage.waitForResponse((response) => response.url().endsWith("/api/v1/auth/csrf"));
  await restoredPage.goto(page.url());
  expect((await csrfRestore).ok()).toBe(true);
  await expect(restoredPage.getByRole("heading", { name: longName })).toBeVisible();
  expect(await restoredPage.evaluate(() => window.sessionStorage.getItem("hostd-csrf"))).toBeTruthy();
  await expect(restoredPage.getByRole("button", { name: "Deploy with fake runtime" })).toBeVisible();
  await expect(restoredPage.getByText("Development capability")).toBeVisible();
  await restoredPage.getByRole("button", { name: "Deploy with fake runtime" }).click();
  await expect(restoredPage.getByText(/Deployment job queued:/)).toBeVisible();
  expect(await restoredPage.evaluate(() => window.sessionStorage.getItem("hostd-csrf"))).toBeTruthy();

  await restoredPage.getByRole("link", { name: "Activity" }).click();
  await expect(restoredPage.getByRole("heading", { name: "Activity" })).toBeVisible();
  const activity = restoredPage.locator(".activity-row").filter({ hasText: "deploy application" }).first();
  await expect(activity.getByRole("button", { name: "Cancel job" })).toBeVisible();
  await activity.getByRole("button", { name: "Cancel job" }).click();
  await expect(activity.locator('[role="status"]').filter({ hasText: "Cancellation recorded" })).toBeVisible();
  await expect(activity.getByText("cancelled", { exact: true })).toBeVisible();
  await restoredPage.getByRole("link", { name: "Machines" }).click();
  await expect(restoredPage.getByRole("heading", { name: "Machines" })).toBeVisible();
  await expect(restoredPage.getByText(/Local controller/)).toBeVisible();
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
      await expect(restoredPage.getByText("Machine", { exact: true })).toBeVisible();
      await expect(restoredPage.getByText("Release", { exact: true })).toBeVisible();
      await expect(restoredPage.getByText("Created", { exact: true })).toBeVisible();
    }
    await restoredPage.screenshot({ path: path.join(screenshotDir, `apps-${width}.png`), fullPage: true });
  }
  expect(browserErrors).toEqual([]);
});
