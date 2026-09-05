import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { DeploymentPlanCandidate, DeploymentPlanRevision, InspectResponse } from "./api";
import { DeploymentPlanReview, deploymentPlanRequest, deploymentSetupFromRevision } from "./deployment-plan-review";

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
  const onAnalyze = vi.fn();
  const onAccept = vi.fn();
  const view = render(<DeploymentPlanReview inspection={inspection(candidates)} expectedRevisionNumber={0} pending={false} error="" onBack={vi.fn()} onAnalyze={onAnalyze} onAccept={onAccept} />);
  return { onAnalyze, onAccept, view };
}

afterEach(cleanup);

describe("DeploymentPlanReview", () => {
  it("prefills detected server settings and reviews the exact editable setup", () => {
    const { onAnalyze } = renderReview();
    expect((screen.getByLabelText("Technology") as HTMLSelectElement).value).toBe("nextjs");
    expect((screen.getByLabelText("Install command (optional)") as HTMLInputElement).value).toBe("npm ci");
    expect((screen.getByLabelText("Build command (optional)") as HTMLInputElement).value).toBe("npm run build");
    expect((screen.getByLabelText("Start command") as HTMLInputElement).value).toBe("npm start");

    fireEvent.click(screen.getByRole("button", { name: "Review setup" }));

    expect(onAnalyze).toHaveBeenCalledWith({
      components: [{ id: "web-12345678", technology: "nextjs", rootDirectory: ".", packageManager: "npm", nodeVersion: "24", installCommand: "npm ci", buildCommand: "npm run build", startCommand: "npm start", outputDirectory: "", internalPort: 3000, healthProbe: "/" }],
    });
  });

  it("accepts only the user-reviewed candidate and sends setup with immutable identity", async () => {
    const { onAnalyze, onAccept, view } = renderReview();
    fireEvent.click(screen.getByRole("button", { name: "Review setup" }));
    const setup = onAnalyze.mock.calls[0][0];
    const reviewed = candidate({ id: "user:deployment-setup", origin: "user", digest: "d".repeat(64) });
    view.rerender(<DeploymentPlanReview inspection={inspection([reviewed])} expectedRevisionNumber={4} pending={false} error="" reviewedSetup={setup} onAnalyze={onAnalyze} onAccept={onAccept} />);

    await waitFor(() => expect(screen.getByRole("button", { name: "Accept setup" })).toBeTruthy());
    fireEvent.click(screen.getByRole("button", { name: "Accept setup" }));

    expect(onAccept).toHaveBeenCalledWith(expect.objectContaining({
      candidateId: "user:deployment-setup",
      expectedCandidateDigest: "d".repeat(64),
      expectedRevisionNumber: 4,
      expectedSourceStructuralFingerprint: "b".repeat(64),
      setup,
    }));
  });

  it("keeps exact shell syntax in commands and requires a new review after an edit", () => {
    const { onAnalyze } = renderReview();
    fireEvent.change(screen.getByLabelText("Start command"), { target: { value: "node server.js && echo ${READY} $()" } });
    fireEvent.click(screen.getByRole("button", { name: "Review setup" }));
    expect(onAnalyze.mock.calls[0][0].components[0].startCommand).toBe("node server.js && echo ${READY} $()");
  });

  it("uses managed static serving and lets empty install and build commands explicitly skip phases", () => {
    const staticCandidate = candidate({ components: [{
      ...candidate().components[0],
      kind: "static",
      framework: "vite",
      staticOutputDirectory: "dist",
      run: { present: true, command: "rig-static --root dist --port 8080", evidence },
      internalPort: { present: true, value: "8080", evidence },
    }] });
    const { onAnalyze } = renderReview([staticCandidate]);
    expect(screen.queryByLabelText("Start command")).toBeNull();
    expect((screen.getByLabelText("Output directory") as HTMLInputElement).value).toBe("dist");
    fireEvent.change(screen.getByLabelText("Install command (optional)"), { target: { value: "" } });
    fireEvent.change(screen.getByLabelText("Build command (optional)"), { target: { value: "" } });
    fireEvent.click(screen.getByRole("button", { name: "Review setup" }));
    expect(onAnalyze.mock.calls[0][0].components[0]).toMatchObject({ technology: "static", installCommand: "", buildCommand: "", startCommand: "", outputDirectory: "dist", internalPort: 8080 });
  });

  it("offers a complete manual form when detection has no candidate", () => {
    const { onAnalyze } = renderReview([]);
    expect(screen.getByText(/no supported setup was detected/i)).toBeTruthy();
    expect((screen.getByLabelText("Root directory") as HTMLInputElement).value).toBe(".");
    expect((screen.getByLabelText("Start command") as HTMLInputElement).value).toBe("");
    expect(screen.getByLabelText("Start command").getAttribute("aria-invalid")).toBe("true");
    fireEvent.change(screen.getByLabelText("Start command"), { target: { value: "node server.js" } });
    fireEvent.click(screen.getByRole("button", { name: "Add static site" }));
    expect(screen.getAllByLabelText("Technology")).toHaveLength(2);
    fireEvent.click(screen.getByRole("button", { name: "Review setup" }));
    expect(onAnalyze).toHaveBeenCalledWith(expect.objectContaining({ components: expect.arrayContaining([expect.objectContaining({ technology: "static", outputDirectory: "dist", startCommand: "" })]) }));
  });

  it("validates advanced port and health fields and focuses the error summary", async () => {
    renderReview();
    fireEvent.change(screen.getByLabelText("Internal port"), { target: { value: "0" } });
    fireEvent.change(screen.getByLabelText("Health-check path"), { target: { value: "health" } });
    fireEvent.click(screen.getByRole("button", { name: "Review setup" }));
    await waitFor(() => expect(document.getElementById("deployment-setup-0-internalPort-error")?.textContent).toMatch(/port from 1/i));
    expect(document.getElementById("deployment-setup-0-healthProbe-error")?.textContent).toMatch(/beginning with/i);
    expect(document.activeElement).toHaveProperty("className", "error-summary");
  });

  it("preserves edits during reanalysis until Reset to detected settings is selected", async () => {
    const { onAnalyze, onAccept, view } = renderReview();
    fireEvent.change(screen.getByLabelText("Start command"), { target: { value: "node custom.js" } });
    fireEvent.click(screen.getByRole("button", { name: "Review setup" }));
    const reviewed = candidate({ id: "user:deployment-setup", origin: "user", components: [{ ...candidate().components[0], build: { present: true, command: "npm run build:changed", evidence } }] });
    view.rerender(<DeploymentPlanReview inspection={inspection([reviewed])} expectedRevisionNumber={0} pending={false} error="" onAnalyze={onAnalyze} onAccept={onAccept} />);

    await waitFor(() => expect((screen.getByLabelText("Start command") as HTMLInputElement).value).toBe("node custom.js"));
    fireEvent.click(screen.getByRole("button", { name: "Reset to detected settings" }));
    expect((screen.getByLabelText("Start command") as HTMLInputElement).value).toBe("npm start");
  });

  it("creates an acceptance request that does not include legacy override fields", () => {
    const source = inspection([candidate()]);
    const setup = { components: [{ id: "web-12345678", technology: "nextjs", rootDirectory: ".", packageManager: "npm", nodeVersion: "24", installCommand: "npm ci", buildCommand: "npm run build", startCommand: "npm start", outputDirectory: "", internalPort: 3000, healthProbe: "/" }] };
    const request = deploymentPlanRequest(source, candidate({ id: "user:deployment-setup", origin: "user" }), setup, 2);
    expect(request).toEqual(expect.objectContaining({ setup, expectedRevisionNumber: 2 }));
    expect(request).not.toHaveProperty("installBehavior");
    expect(request).not.toHaveProperty("components");
  });

  it("uses static setup defaults only for a newly added static site and limits the layout", () => {
    renderReview();
    fireEvent.click(screen.getByRole("button", { name: "Add static site" }));
    expect((screen.getAllByLabelText("Install command (optional)")[1] as HTMLInputElement).value).toBe("npm install");
    expect((screen.getAllByLabelText("Build command (optional)")[1] as HTMLInputElement).value).toBe("npm run build");
    expect((screen.getByRole("button", { name: "Add server" }) as HTMLButtonElement).disabled).toBe(true);
    expect((screen.getByRole("button", { name: "Add static site" }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("does not overwrite edited values when a different detection is selected", () => {
    const alternative = candidate({ id: "candidate-alternative", rootDirectory: "apps/web", components: [{ ...candidate().components[0], id: "alt", rootDirectory: "apps/web", run: { present: true, command: "node detected.js", evidence } }] });
    renderReview([candidate(), alternative]);
    fireEvent.change(screen.getByLabelText("Start command"), { target: { value: "node custom.js" } });
    fireEvent.click(screen.getByLabelText(/apps\/web/i));
    expect((screen.getByLabelText("Start command") as HTMLInputElement).value).toBe("node custom.js");
    fireEvent.click(screen.getByRole("button", { name: "Reset to detected settings" }));
    expect((screen.getByLabelText("Start command") as HTMLInputElement).value).toBe("node detected.js");
  });

  it("sends an edited advanced port as a number", () => {
    const { onAnalyze } = renderReview();
    fireEvent.change(screen.getByLabelText("Internal port"), { target: { value: "4100" } });
    fireEvent.click(screen.getByRole("button", { name: "Review setup" }));
    expect(onAnalyze.mock.calls[0][0].components[0].internalPort).toBe(4100);
  });

  it("reconstructs legacy component names and only parses a managed static output command", () => {
    const revision = {
      components: [
        { name: "public.site", role: "static", rootDirectory: ".", packageManager: "npm", nodeVersion: "24", installBehavior: "npm ci", buildCommand: "npm run build", runCommand: "rig-static --root 'public/site' --port 8080", internalPort: 8080, healthProbe: "/" },
        { name: "unknown", role: "static", rootDirectory: ".", packageManager: "npm", nodeVersion: "24", installBehavior: "", buildCommand: "", runCommand: "serve dist", internalPort: 8080, healthProbe: "/" },
      ],
      migration: { present: false },
    } as DeploymentPlanRevision;
    expect(deploymentSetupFromRevision(revision).components).toEqual(expect.arrayContaining([
      expect.objectContaining({ id: "public.site", outputDirectory: "public/site" }),
      expect.objectContaining({ id: "unknown", outputDirectory: "" }),
    ]));
  });

  it("marks command errors and anchors dotted component identities to the affected input", () => {
    const dotted = candidate({ components: [{ ...candidate().components[0], id: "apps.web" }] });
    render(<DeploymentPlanReview inspection={inspection([dotted])} expectedRevisionNumber={0} pending={false} error="" apiErrors={{ "components.0.installCommand": "Use a supported command." }} onAnalyze={vi.fn()} onAccept={vi.fn()} />);
    const input = screen.getByLabelText("Install command (optional)");
    expect(input.getAttribute("aria-invalid")).toBe("true");
    expect(input.getAttribute("aria-describedby")).toBe("deployment-setup-0-installCommand-error");
    expect(screen.getByRole("link", { name: /install command/i }).getAttribute("href")).toBe("#deployment-setup-0-installCommand");
  });
});
