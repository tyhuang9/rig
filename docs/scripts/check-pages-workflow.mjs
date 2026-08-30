import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

const workflowPath = fileURLToPath(
  new URL("../../.github/workflows/pages.yml", import.meta.url),
);
const workflow = readFileSync(workflowPath, "utf8").replaceAll("\r\n", "\n");
const publicationGuard =
  "if: github.ref == 'refs/heads/main' && (github.event_name == 'push' || github.event_name == 'workflow_dispatch')";

function assert(condition, message) {
  if (!condition) throw new Error(message);
}

function section(start, end) {
  const startIndex = workflow.indexOf(start);
  assert(startIndex >= 0, `missing workflow section: ${start.trim()}`);
  const endIndex = end ? workflow.indexOf(end, startIndex + start.length) : workflow.length;
  assert(endIndex >= 0, `missing workflow boundary: ${end?.trim() ?? "end of file"}`);
  return workflow.slice(startIndex, endIndex);
}

function mayPublish(eventName, ref) {
  return (
    ref === "refs/heads/main" &&
    (eventName === "push" || eventName === "workflow_dispatch")
  );
}

const cases = [
  { eventName: "pull_request", ref: "refs/pull/42/merge", expected: false },
  { eventName: "workflow_dispatch", ref: "refs/heads/feature/docs", expected: false },
  { eventName: "workflow_dispatch", ref: "refs/tags/v1.0.0", expected: false },
  { eventName: "push", ref: "refs/heads/main", expected: true },
  { eventName: "workflow_dispatch", ref: "refs/heads/main", expected: true },
  { eventName: "schedule", ref: "refs/heads/main", expected: false },
];

for (const testCase of cases) {
  assert(
    mayPublish(testCase.eventName, testCase.ref) === testCase.expected,
    `unexpected publication decision for ${testCase.eventName} on ${testCase.ref}`,
  );
}

const configureStep = section(
  "      - name: Configure GitHub Pages\n",
  "      - name: Upload GitHub Pages artifact\n",
);
const uploadStep = section(
  "      - name: Upload GitHub Pages artifact\n",
  "\n  deploy:\n",
);
const deployJob = section("\n  deploy:\n");
const buildJob = section("\n  build:\n", "\n  deploy:\n");

for (const [name, block] of [
  ["Configure GitHub Pages", configureStep],
  ["Upload GitHub Pages artifact", uploadStep],
  ["deploy job", deployJob],
]) {
  assert(block.includes(publicationGuard), `${name} is missing the fail-closed publication guard`);
}

assert(
  workflow.split(publicationGuard).length - 1 === 3,
  "the publication guard must appear at exactly the two artifact steps and deploy job",
);
assert(
  workflow.includes("permissions:\n  contents: read\n"),
  "the workflow default permission must remain contents: read",
);
assert(!buildJob.includes("pages: write"), "the build job must not receive Pages write permission");
assert(!buildJob.includes("id-token: write"), "the build job must not receive OIDC write permission");
assert(deployJob.includes("      pages: write\n"), "the deploy job needs Pages write permission");
assert(deployJob.includes("      id-token: write\n"), "the deploy job needs OIDC write permission");
assert(
  workflow.split("pages: write").length - 1 === 1 &&
    workflow.split("id-token: write").length - 1 === 1,
  "write permissions must appear only in the deploy job",
);

const actions = [...workflow.matchAll(/uses:\s+[^@\s]+@([^\s]+)/g)].map((match) => match[1]);
assert(actions.length === 6, "expected exactly six GitHub Actions dependencies");
assert(actions.every((revision) => /^[0-9a-f]{40}$/.test(revision)), "every action must use a full SHA");

console.log("Pages workflow publication contract passed");
