import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { DeploymentPlanCandidate, InspectResponse } from "./api";
import { DeploymentPlanReview } from "./deployment-plan-review";

const evidence = [{ code: "package_script", path: "package.json", field: "scripts.start" }];

function candidate(overrides: Partial<DeploymentPlanCandidate> = {}): DeploymentPlanCandidate {
  return {
    id: "candidate-web",
    origin: "inferred",
    status: "ready",
    kind: "javascript",
    rootDirectory: ".",
    configPath: "package.json",
    digest: "c".repeat(64),
    packageManager: { present: true, name: "npm", version: "11", lockfile: "package-lock.json", origin: "inferred", provenance: "lockfile", confidence: "high", evidence },
    nodeVersion: { present: true, value: "24", origin: "inferred", provenance: "engines", confidence: "high", evidence },
    install: { present: true, command: "npm ci", phase: "install", workingDirectory: ".", origin: "inferred", provenance: "lockfile", confidence: "high", evidence },
    components: [{
      id: "web-12345678",
      origin: "inferred",
      name: "web",
      kind: "server",
      framework: "nextjs",
      rootDirectory: ".",
      staticOutputDirectory: "",
      migrationFingerprint: "",
      build: { present: true, command: "npm run build", phase: "build", workingDirectory: ".", evidence },
      run: { present: true, command: "npm start", phase: "run", workingDirectory: ".", evidence },
      internalPort: { present: true, value: "3000", evidence },
      healthProbe: { present: true, path: "/", method: "GET", evidence },
      evidence,
      findings: [],
    }],
    evidence,
    findings: [],
    missingFields: [],
    advancedInputs: [],
    ...overrides,
  };
}

function inspection(candidates: DeploymentPlanCandidate[]): InspectResponse {
  return {
    source: { type: "local", path: "C:/projects/app" },
    composeCandidates: [],
    services: [],
    findings: [],
    analysis: {
      source: { type: "local", path: "C:/projects/app" },
      resolvedDigest: "a".repeat(64),
      schemaVersion: "2",
      structuralFingerprint: "b".repeat(64),
      candidates,
      findings: [],
    },
  };
}

function renderReview(candidates = [candidate()]) {
  const onAccept = vi.fn();
  render(<DeploymentPlanReview inspection={inspection(candidates)} expectedRevisionNumber={0} pending={false} error="" onBack={vi.fn()} onRefresh={vi.fn()} onAccept={onAccept} />);
  return onAccept;
}

afterEach(cleanup);

describe("DeploymentPlanReview", () => {
  it("auto-populates one ready project and submits the immutable candidate identity", async () => {
    const onAccept = renderReview();
    expect(screen.getByRole("textbox", { name: "Build command" })).toHaveProperty("value", "npm run build");
    expect(screen.getByRole("textbox", { name: "Run command (required)" })).toHaveProperty("value", "npm start");
    fireEvent.click(screen.getByRole("button", { name: /accept setup/i }));

    expect(onAccept).toHaveBeenCalledWith(expect.objectContaining({
      candidateId: "candidate-web",
      expectedCandidateDigest: "c".repeat(64),
      expectedSourceStructuralFingerprint: "b".repeat(64),
      packageManager: "npm",
      installBehavior: "npm ci",
      components: [{ componentId: "web-12345678", buildCommand: "npm run build", runCommand: "npm start", nodeVersion: "24", internalPort: 3000, healthProbe: "/" }],
    }));
  });

  it("keeps exact shell syntax as one command and marks the user edit", () => {
    const onAccept = renderReview();
    fireEvent.change(screen.getByRole("textbox", { name: "Run command (required)" }), { target: { value: "node server.js && echo ${READY} $()" } });
    expect(screen.getByText("Changed")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: /accept setup/i }));
    expect(onAccept.mock.calls[0][0].components[0].runCommand).toBe("node server.js && echo ${READY} $()");
  });

  it("shows managed Vite serving behavior without exposing an editable run command", () => {
    const vite = candidate({ components: [{
      ...candidate().components[0],
      kind: "static",
      framework: "vite",
      staticOutputDirectory: "dist",
      run: { present: true, command: "rig-static --root dist --port 8080", evidence },
      internalPort: { present: true, value: "8080", evidence },
    }] });
    renderReview([vite]);
    expect(screen.getByText(/serve generated static files/i)).toBeTruthy();
    expect(screen.queryByDisplayValue(/rig-static/i)).toBeNull();
  });

  it("opens Advanced and validates user-supplied port and health settings", async () => {
    const missing = candidate({
      status: "needs_input",
      missingFields: ["components.web-12345678.internal_port", "components.web-12345678.health_probe"],
      advancedInputs: [
        { componentId: "web-12345678", field: "components.web-12345678.internal_port", reason: "Port was not detected.", required: true },
        { componentId: "web-12345678", field: "components.web-12345678.health_probe", reason: "Health path was not detected.", required: true },
      ],
      components: [{ ...candidate().components[0], internalPort: { present: false, evidence: [] }, healthProbe: { present: false, evidence: [] } }],
    });
    const onAccept = renderReview([missing]);
    const details = screen.getByText(/advanced settings/i).closest("details");
    expect(details?.hasAttribute("open")).toBe(true);
    fireEvent.click(screen.getByRole("button", { name: /accept setup/i }));
    await waitFor(() => expect(document.getElementById("plan-web-12345678-internalPort-error")?.textContent).toMatch(/enter a port from 1 to 65535/i));
    expect(document.getElementById("plan-web-12345678-healthProbe-error")?.textContent).toMatch(/enter a health-check path beginning with/i);
    fireEvent.change(screen.getByRole("spinbutton", { name: "Internal port (required)" }), { target: { value: "4000" } });
    fireEvent.change(screen.getByRole("textbox", { name: "Health-check path (required)" }), { target: { value: "/healthz" } });
    fireEvent.click(screen.getByRole("button", { name: /accept setup/i }));
    await waitFor(() => expect(onAccept).toHaveBeenCalled());
  });

  it("requires an explicit root choice when independent projects are found", () => {
    const api = candidate({ id: "candidate-api", rootDirectory: "apps/api", digest: "d".repeat(64), components: [{ ...candidate().components[0], id: "api-12345678", name: "api", rootDirectory: "apps/api", framework: "fastify" }] });
    renderReview([candidate({ rootDirectory: "apps/web" }), api]);
    expect(screen.getByRole("group", { name: /which app do you want to deploy/i })).toBeTruthy();
    expect(screen.getByRole("button", { name: /accept setup/i }).hasAttribute("disabled")).toBe(true);
    fireEvent.click(screen.getByRole("radio", { name: /apps\/api/i }));
    expect(screen.getByRole("button", { name: /accept setup/i }).hasAttribute("disabled")).toBe(false);
  });

  it("blocks unsupported topology instead of presenting editable commands", () => {
    renderReview([candidate({ status: "unsupported", findings: [{ code: "worker", severity: "error", message: "Worker topologies need explicit configuration." }] })]);
    expect(screen.getByRole("heading", { name: /can’t safely identify/i })).toBeTruthy();
    expect(screen.queryByLabelText(/run command/i)).toBeNull();
  });

  it("shows migration risk and keeps approval separate from setup acceptance", () => {
    const migration = candidate({ components: [{ ...candidate().components[0], migrationFingerprint: "e".repeat(64), migration: { present: true, command: "npx prisma migrate deploy", evidence } }] });
    const onAccept = renderReview([migration]);
    expect(screen.getByText(/will not automatically undo database changes/i)).toBeTruthy();
    expect(screen.getByText(/does not approve the migration/i)).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: /accept setup/i }));
    expect(onAccept.mock.calls[0][0].migrationCommand).toBe("npx prisma migrate deploy");
  });

  it("reconciles a refreshed single candidate and accepts its new immutable identity", async () => {
    const onAccept = vi.fn();
    const initial = inspection([candidate()]);
    const refreshed = inspection([candidate({ id: "candidate-refreshed", digest: "d".repeat(64), components: [{ ...candidate().components[0], build: { present: true, command: "npm run build:new", evidence } }] })]);
    const view = render(<DeploymentPlanReview inspection={initial} expectedRevisionNumber={0} pending={false} error="" onBack={vi.fn()} onRefresh={vi.fn()} onAccept={onAccept} />);

    view.rerender(<DeploymentPlanReview inspection={refreshed} expectedRevisionNumber={0} pending={false} error="" onBack={vi.fn()} onRefresh={vi.fn()} onAccept={onAccept} />);

    await waitFor(() => expect(screen.getByRole("textbox", { name: "Build command" })).toHaveProperty("value", "npm run build:new"));
    fireEvent.click(screen.getByRole("button", { name: /accept setup/i }));
    expect(onAccept).toHaveBeenCalledWith(expect.objectContaining({ candidateId: "candidate-refreshed", expectedCandidateDigest: "d".repeat(64) }));
  });

  it("opens collapsed Advanced settings and associates validation errors with their fields", async () => {
    renderReview();
    const details = screen.getByText(/advanced settings/i).closest("details");
    expect(details?.hasAttribute("open")).toBe(false);
    const port = document.getElementById("plan-web-12345678-internalPort") as HTMLInputElement;
    fireEvent.change(port, { target: { value: "" } });
    fireEvent.click(screen.getByRole("button", { name: /accept setup/i }));

    await waitFor(() => expect(details?.hasAttribute("open")).toBe(true));
    expect(port.getAttribute("aria-describedby")).toBe("plan-web-12345678-internalPort-error");
    expect(document.activeElement).toHaveProperty("className", "error-summary");
  });

  it("keeps reset available and returns focus to the edited command", async () => {
    renderReview();
    const run = screen.getByRole("textbox", { name: "Run command (required)" }) as HTMLInputElement;
    fireEvent.change(run, { target: { value: "node custom.js" } });
    const reset = screen.getByRole("button", { name: /reset web run command to suggestion/i });
    reset.focus();
    fireEvent.click(reset);

    await waitFor(() => expect(document.activeElement).toBe(run));
    expect(run.value).toBe("npm start");
    expect(reset.hasAttribute("disabled")).toBe(true);
  });
});
