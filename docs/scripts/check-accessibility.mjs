import { readFileSync, readdirSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { join } from "node:path";

const docsRoot = fileURLToPath(new URL("../", import.meta.url));
const outputRoot = fileURLToPath(new URL("../.vitepress/dist/", import.meta.url));
const config = readFileSync(new URL("../.vitepress/config.mts", import.meta.url), "utf8");
const theme = readFileSync(new URL("../.vitepress/theme/custom.css", import.meta.url), "utf8");
const home = readFileSync(new URL("../.vitepress/dist/index.html", import.meta.url), "utf8");
const builtPages = readdirSync(outputRoot)
  .filter((file) => file.endsWith(".html"))
  .map((file) => readFileSync(join(outputRoot, file), "utf8"));

function assert(condition, message) {
  if (!condition) throw new Error(message);
}

function channel(value) {
  const normalized = value / 255;
  return normalized <= 0.04045
    ? normalized / 12.92
    : ((normalized + 0.055) / 1.055) ** 2.4;
}

function luminance(hex) {
  const channels = hex
    .slice(1)
    .match(/.{2}/g)
    .map((value) => channel(Number.parseInt(value, 16)));
  return 0.2126 * channels[0] + 0.7152 * channels[1] + 0.0722 * channels[2];
}

function contrast(first, second) {
  const lighter = Math.max(luminance(first), luminance(second));
  const darker = Math.min(luminance(first), luminance(second));
  return (lighter + 0.05) / (darker + 0.05);
}

assert(!config.includes("search:"), "the inaccessible local-search control must remain disabled");
assert(
  builtPages.every((page) => !page.includes("VPLocalSearchBox")),
  "built pages must not render the local-search control",
);
assert(
  builtPages.every((page) => !page.includes('class="item" role="button"')),
  "static sidebar labels must not render as inert button controls",
);
assert(home.includes('<main class="AccessibleHome"'), "the home page needs a main landmark");
assert([...home.matchAll(/<main\b/g)].length === 1, "the home page needs exactly one main landmark");
assert(home.includes('href="#VPContent"'), "the home page skip link must remain available");
assert(home.includes('id="VPContent"'), "the skip-link target must remain available");

function themeColors(selector, label) {
  const block = theme.match(new RegExp(`${selector}\\s*\\{([^}]*)\\}`, "s"))?.[1] ?? "";
  const colors = Object.fromEntries(
    [...block.matchAll(/--vp-button-brand-(bg|hover-bg|active-bg):\s*(#[0-9a-f]{6})/gi)].map(
      ([, state, color]) => [state, color],
    ),
  );

  for (const state of ["bg", "hover-bg", "active-bg"]) {
    assert(colors[state], `missing ${label}-theme brand button ${state} color`);
    assert(
      contrast(colors[state], "#ffffff") >= 4.5,
      `${label}-theme brand button ${state} contrast must be at least 4.5:1`,
    );
  }
}

themeColors(":root:not\\(.dark\\)", "light");
themeColors(":root\\.dark", "dark");

console.log(`Documentation accessibility contract passed for ${docsRoot}`);
