import { chromium, expect } from "@playwright/test";

const [appId, port, mode, note] = process.argv.slice(2);
if (!/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/.test(appId ?? "") ||
    !/^[0-9]{1,5}$/.test(port ?? "") || Number(port) < 1 || Number(port) > 65535 ||
    !["create", "read"].includes(mode) || typeof note !== "string" || !note.startsWith("browser TLS note ")) {
  throw new Error("invalid hosted notes browser test input");
}

const hostname = `${appId}.rig.localhost`;
const browser = await chromium.launch({
  headless: true,
  args: [`--host-resolver-rules=MAP ${hostname} 127.0.0.1`]
});

try {
  const page = await browser.newPage();
  await page.goto(`http://${hostname}:${port}/`, { waitUntil: "domcontentloaded" });
  await expect(page.getByRole("heading", { name: "Hosting notes" })).toBeVisible();
  await expect(page.getByRole("status")).toHaveText("Ready");
  await expect(page.getByText("controller-journey-public")).toBeVisible();

  if (mode === "create") {
    await page.getByLabel("New note").fill(note);
    await page.getByRole("button", { name: "Add note" }).click();
    await expect(page.getByRole("status")).toHaveText("Saved");
  }

  await expect(page.getByRole("list", { name: "Notes" }).getByRole("listitem").filter({ hasText: note })).toBeVisible();
  await page.reload({ waitUntil: "domcontentloaded" });
  await expect(page.getByRole("status")).toHaveText("Ready");
  await expect(page.getByRole("list", { name: "Notes" }).getByRole("listitem").filter({ hasText: note })).toBeVisible();
  process.stdout.write(`hosted Chromium ${mode} passed\n`);
} finally {
  await browser.close();
}
