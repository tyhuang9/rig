import { expect, test } from "@playwright/test";
import { spawn, type ChildProcessWithoutNullStreams } from "node:child_process";
import { createServer } from "node:net";
import path from "node:path";
import type {
  BootstrapStatus,
  CSRFResponse,
  GitHubInstallationPage,
  MeResponse,
  SourceConnectionList,
  SystemStatus,
} from "../src/generated/api-contract";

const webRoot = path.resolve(import.meta.dirname, "..");
const connectionId = "0123456789abcdef0123456789abcdef";
let baseURL = "";
let vite: ChildProcessWithoutNullStreams;

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
  const port = await availablePort();
  baseURL = `http://127.0.0.1:${port}`;
  const viteEntry = path.join(webRoot, "node_modules", "vite", "bin", "vite.js");
  vite = spawn(process.execPath, [viteEntry, "--host", "127.0.0.1", "--port", String(port), "--strictPort"], {
    cwd: webRoot,
    windowsHide: true,
  });
  let stderr = "";
  vite.stderr.on("data", (chunk) => { stderr += chunk.toString("utf8"); });
  const deadline = Date.now() + 15_000;
  while (Date.now() < deadline) {
    if (vite.exitCode !== null) throw new Error(`Vite exited before startup: ${stderr}`);
    try {
      const response = await fetch(baseURL);
      if (response.ok) return;
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
    vite.once("exit", () => {
      clearTimeout(timeout);
      resolve();
    });
  });
});

test("keeps focusable pagination guarded while loading and on an empty final page", async ({ page }) => {
  let releaseSecondPage!: () => void;
  let reportSecondPageStarted!: () => void;
  const secondPageGate = new Promise<void>((resolve) => { releaseSecondPage = resolve; });
  const secondPageStarted = new Promise<void>((resolve) => { reportSecondPageStarted = resolve; });
  const requestedInstallationPages: number[] = [];

  await page.route("**/api/v1/**", async (route) => {
    const url = new URL(route.request().url());
    if (url.pathname === "/api/v1/auth/bootstrap/status") return route.fulfill({ json: { bootstrapRequired: false } satisfies BootstrapStatus });
    if (url.pathname === "/api/v1/auth/me") return route.fulfill({ json: { user: { id: "user-1", username: "browser-admin", role: "administrator" } } satisfies MeResponse });
    if (url.pathname === "/api/v1/auth/csrf") return route.fulfill({ json: { csrfToken: "browser-csrf" } satisfies CSRFResponse });
    if (url.pathname === "/api/v1/system/status") return route.fulfill({
      json: {
        daemon: "browser-test",
        capabilities: { fakeRuntime: false, githubConnections: true },
        diagnostics: {
          architecture: "amd64",
          caddyManaged: false,
          clientAvailable: false,
          composeAvailable: false,
          composeDetail: "Not required by this browser test.",
          composeVersion: "",
          daemonRunning: false,
          dockerDetail: "Not required by this browser test.",
          dockerVersion: "",
          engineReady: false,
          os: "browser-test",
          resources: { diskAvailableBytes: 0, diskTotalBytes: 0, memoryAvailableBytes: 0, memoryTotalBytes: 0 },
          startupLimitation: "",
        },
      } satisfies SystemStatus,
    });
    if (url.pathname === "/api/v1/source-connections") return route.fulfill({
      json: {
        items: [{
          id: connectionId,
          provider: "github",
          status: "connected",
          providerLogin: "browser-admin",
          credentialGeneration: 1,
          createdAt: "2026-01-01T00:00:00Z",
          updatedAt: "2026-01-01T00:00:00Z",
        }],
      } satisfies SourceConnectionList,
    });
    if (url.pathname === `/api/v1/source-connections/${connectionId}/github/installations`) {
      const requestedPage = Number(url.searchParams.get("page") ?? "1");
      requestedInstallationPages.push(requestedPage);
      if (requestedPage === 2) {
        reportSecondPageStarted();
        await secondPageGate;
        return route.fulfill({ json: { page: 2, perPage: 30, totalCount: 30, items: [] } satisfies GitHubInstallationPage });
      }
      if (requestedPage === 1) return route.fulfill({
        json: {
          page: 1,
          perPage: 30,
          totalCount: 60,
          items: [{
            id: 10,
            accountLogin: "octo-org",
            accountType: "Organization",
            targetType: "Organization",
            repositorySelection: "selected",
            cachedAt: "2026-01-01T00:00:00Z",
          }],
        } satisfies GitHubInstallationPage,
      });
      return route.fulfill({ json: { page: requestedPage, perPage: 30, totalCount: 30, items: [] } satisfies GitHubInstallationPage });
    }
    return route.fulfill({ status: 404, json: { code: "not_found", detail: "Unexpected browser-test request." } });
  });

  await page.goto(`${baseURL}/apps/new`);
  await expect(page.getByRole("heading", { name: "Add application" })).toBeVisible();
  await page.getByLabel("GitHub repository").check();
  await page.getByLabel("GitHub connection").selectOption(connectionId);
  await expect(page.getByRole("option", { name: /octo-org/i })).toBeAttached();

  const pagination = page.getByRole("navigation", { name: "GitHub App installations pagination" });
  const previous = page.getByRole("button", { name: "Previous GitHub App installations page" });
  const next = page.getByRole("button", { name: "Next GitHub App installations page" });
  await expect(next).toHaveAttribute("aria-disabled", "false");
  await next.focus();
  await expect(next).toBeFocused();
  await next.press("Enter");
  await secondPageStarted;

  await expect(pagination).toHaveAttribute("aria-busy", "true");
  await expect(previous).toHaveAttribute("aria-disabled", "true");
  await expect(next).toHaveAttribute("aria-disabled", "true");
  await expect(next).not.toHaveAttribute("disabled");
  await expect(next).toBeFocused();
  await next.press("Enter");
  await next.press("Space");
  await page.evaluate(() => new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))));
  expect(requestedInstallationPages).toEqual([1, 2]);

  releaseSecondPage();
  await expect(page.locator("#github-installation-status")).toHaveText("GitHub App installations page 2 loaded. 0 results.");
  await expect(pagination).toHaveAttribute("aria-busy", "false");
  await expect(previous).toHaveAttribute("aria-disabled", "false");
  await expect(next).toHaveAttribute("aria-disabled", "true");
  await expect(next).toBeFocused();
  await next.press("Enter");
  await next.press("Space");
  await page.evaluate(() => new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))));
  expect(requestedInstallationPages).toEqual([1, 2]);

  await previous.press("Enter");
  await expect(page.locator("#github-installation-status")).toHaveText("GitHub App installations page 1 loaded. 1 result.");
  await expect(pagination).toContainText("Page 1");
  await expect(previous).toHaveAttribute("aria-disabled", "true");
  await expect(next).toHaveAttribute("aria-disabled", "false");
});
