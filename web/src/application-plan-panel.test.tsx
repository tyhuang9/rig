import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  APIError,
  api,
  type Application,
  type DeploymentPlanCandidate,
  type DeploymentPlanRevision,
  type InspectResponse,
} from "./api";
import {
  ApplicationPlanPanel,
  applicationInspectionRequest,
} from "./application-plan-panel";

const evidence = [
  { code: "package_script", path: "package.json", field: "scripts.start" },
];
const localApp: Application = {
  id: "app-local",
  name: "Local app",
  slug: "local-app",
  description: "",
  machineName: "Controller",
  status: "ready",
  createdAt: "2026-08-31T12:00:00Z",
  source: { type: "local", path: "C:\\projects\\local-app" },
};
const githubApp: Application = {
  ...localApp,
  id: "app-github",
  source: {
    type: "github",
    connectionId: "connection-123",
    installationId: 42,
    repositoryId: 99,
    repositoryOwner: "octo",
    repositoryName: "service",
    trackedBranch: "release/current",
    trackedRef: "refs/heads/release/current",
    composePath: "deploy/compose.yaml",
  },
};

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => {
    resolve = done;
  });
  return { promise, resolve };
}

function candidate(
  overrides: Partial<DeploymentPlanCandidate> = {},
): DeploymentPlanCandidate {
  return {
    id: "candidate-web",
    origin: "inferred",
    status: "ready",
    kind: "javascript",
    rootDirectory: ".",
    configPath: "package.json",
    digest: "c".repeat(64),
    packageManager: {
      present: true,
      name: "npm",
      version: "11",
      lockfile: "package-lock.json",
      origin: "inferred",
      provenance: "lockfile",
      confidence: "high",
      evidence,
    },
    nodeVersion: {
      present: true,
      value: "24",
      origin: "inferred",
      provenance: "engines",
      confidence: "high",
      evidence,
    },
    install: {
      present: true,
      command: "npm ci",
      phase: "install",
      workingDirectory: ".",
      origin: "inferred",
      provenance: "lockfile",
      confidence: "high",
      evidence,
    },
    components: [
      {
        id: "web-12345678",
        origin: "inferred",
        name: "web",
        kind: "server",
        framework: "nextjs",
        rootDirectory: ".",
        staticOutputDirectory: "",
        migrationFingerprint: "",
        build: {
          present: true,
          command: "npm run build",
          phase: "build",
          workingDirectory: ".",
          evidence,
        },
        run: {
          present: true,
          command: "npm start",
          phase: "run",
          workingDirectory: ".",
          evidence,
        },
        internalPort: { present: true, value: "3000", evidence },
        healthProbe: { present: true, path: "/", method: "GET", evidence },
        evidence,
        findings: [],
      },
    ],
    evidence,
    findings: [],
    missingFields: [],
    advancedInputs: [],
    ...overrides,
  };
}

function inspection(
  app: Application = localApp,
  selected = candidate(),
): InspectResponse {
  return {
    source: app.source,
    composeCandidates: [],
    services: [],
    findings: [],
    analysis: {
      source: app.source,
      resolvedDigest: "a".repeat(64),
      schemaVersion: "2",
      structuralFingerprint: "b".repeat(64),
      candidates: [selected],
      findings: [],
    },
  };
}

function plan(
  overrides: Partial<DeploymentPlanRevision> = {},
): DeploymentPlanRevision {
  return {
    revisionId: "plan-revision-3",
    revisionNumber: 3,
    strategy: "generated_node",
    state: "accepted",
    canonicalDigest: "d".repeat(64),
    source: {
      provider: "local",
      repositoryId: 0,
      resolvedDigest: "a".repeat(64),
    },
    detector: {
      name: "projectanalysis",
      version: "2",
      sourceStructuralFingerprint: "b".repeat(64),
    },
    components: [],
    fieldProvenance: [],
    migration: { present: false },
    ...overrides,
  };
}

function renderPanel(app: Application = localApp) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  const view = render(
    <QueryClientProvider client={client}>
      <ApplicationPlanPanel app={app} />
    </QueryClientProvider>,
  );
  return { client, ...view };
}

describe("ApplicationPlanPanel", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
    vi.spyOn(api, "deploymentPlan").mockResolvedValue(plan());
    vi.spyOn(api, "inspect").mockResolvedValue(inspection());
    vi.spyOn(api, "acceptDeploymentPlan").mockResolvedValue(
      plan({ revisionId: "plan-revision-4", revisionNumber: 4 }),
    );
    vi.spyOn(api, "approveDeploymentPlanMigration").mockResolvedValue(
      plan({ migration: { present: true, approvalStatus: "approved" } }),
    );
  });
  afterEach(cleanup);

  it("constructs the exact local request and accepts the reviewed CAS identity", async () => {
    renderPanel();
    await screen.findByRole("heading", { name: "Accepted deployment setup" });
    fireEvent.click(screen.getByRole("button", { name: "Review current source" }));
    expect(api.inspect).toHaveBeenCalledWith({
      sourcePath: "C:\\projects\\local-app",
    });
    const heading = await screen.findByRole("heading", {
      name: "How Rig will run this app",
    });
    await waitFor(() => expect(document.activeElement).toBe(heading));
    fireEvent.click(screen.getByRole("button", { name: "Accept setup" }));
    await waitFor(() =>
      expect(api.acceptDeploymentPlan).toHaveBeenCalledWith(
        localApp.id,
        expect.objectContaining({
          expectedRevisionNumber: 3,
          expectedCandidateDigest: "c".repeat(64),
          expectedSourceStructuralFingerprint: "b".repeat(64),
        }),
      ),
    );
    expect(await screen.findByText("Revision 4 · Generated runtime")).not.toBeNull();
    const status = screen.getByRole("status");
    expect(status.getAttribute("aria-live")).toBe("polite");
    expect(status.getAttribute("aria-atomic")).toBe("true");
  });

  it("constructs the exact GitHub inspection request", async () => {
    vi.mocked(api.inspect).mockResolvedValue(inspection(githubApp));
    renderPanel(githubApp);
    fireEvent.click(
      await screen.findByRole("button", { name: "Review current source" }),
    );
    expect(api.inspect).toHaveBeenCalledWith({
      githubSource: {
        connectionId: "connection-123",
        installationId: 42,
        repositoryId: 99,
        branch: "release/current",
        composePath: "deploy/compose.yaml",
      },
    });
    expect(
      await screen.findByRole("heading", { name: "How Rig will run this app" }),
    ).not.toBeNull();
  });

  it("pins acceptance CAS to the plan head reviewed with the inspection", async () => {
    const { client } = renderPanel();
    fireEvent.click(
      await screen.findByRole("button", { name: "Review current source" }),
    );
    await screen.findByRole("heading", { name: "How Rig will run this app" });
    client.setQueryData(
      ["deployment-plan", localApp.id],
      plan({ revisionId: "unreviewed-head", revisionNumber: 9 }),
    );
    fireEvent.click(screen.getByRole("button", { name: "Accept setup" }));
    await waitFor(() =>
      expect(api.acceptDeploymentPlan).toHaveBeenCalledWith(
        localApp.id,
        expect.objectContaining({ expectedRevisionNumber: 3 }),
      ),
    );
  });

  it("maps only the exact plan-not-found response to legacy revision zero", async () => {
    vi.mocked(api.deploymentPlan).mockRejectedValue(
      new APIError({
        status: 404,
        code: "deployment_plan_not_found",
        detail: "No accepted plan",
      }),
    );
    renderPanel();
    expect(await screen.findByText("Legacy Compose setup")).not.toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Review current source" }));
    await screen.findByRole("heading", { name: "How Rig will run this app" });
    fireEvent.click(screen.getByRole("button", { name: "Accept setup" }));
    await waitFor(() =>
      expect(api.acceptDeploymentPlan).toHaveBeenCalledWith(
        localApp.id,
        expect.objectContaining({ expectedRevisionNumber: 0 }),
      ),
    );
  });

  it("fails closed for incomplete source provenance without inspecting", async () => {
    const incomplete = {
      ...githubApp,
      source: { ...githubApp.source, trackedBranch: undefined },
    };
    renderPanel(incomplete);
    const review = await screen.findByRole("button", {
      name: "Review current source",
    });
    expect((review as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByText("Saved source details are incomplete")).not.toBeNull();
    expect(applicationInspectionRequest(incomplete)).toBeNull();
    expect(api.inspect).not.toHaveBeenCalled();
  });

  it("ignores an old inspection after the application source changes", async () => {
    const oldInspection = deferred<InspectResponse>();
    vi.mocked(api.inspect).mockImplementation((request) =>
      request.sourcePath
        ? oldInspection.promise
        : Promise.resolve(inspection(githubApp)),
    );
    const { client, rerender } = renderPanel();
    fireEvent.click(
      await screen.findByRole("button", { name: "Review current source" }),
    );
    await waitFor(() => expect(api.inspect).toHaveBeenCalledTimes(1));

    rerender(
      <QueryClientProvider client={client}>
        <ApplicationPlanPanel app={githubApp} />
      </QueryClientProvider>,
    );
    fireEvent.click(
      await screen.findByRole("button", { name: "Review current source" }),
    );
    await screen.findByRole("heading", { name: "How Rig will run this app" });
    await act(async () =>
      oldInspection.resolve(
        inspection(
          localApp,
          candidate({
            components: [
              {
                ...candidate().components[0],
                build: {
                  present: true,
                  command: "old-source-secret-command",
                  evidence,
                },
              },
            ],
          }),
        ),
      ),
    );
    expect(screen.queryByDisplayValue("old-source-secret-command")).toBeNull();
    expect(screen.getByDisplayValue("npm run build")).not.toBeNull();
  });

  it("reanalyzes stale acceptance without exposing server command details", async () => {
    const refreshed = candidate({
      id: "candidate-refreshed",
      digest: "e".repeat(64),
      components: [
        {
          ...candidate().components[0],
          build: {
            present: true,
            command: "npm run build:refreshed",
            evidence,
          },
        },
      ],
    });
    vi.mocked(api.inspect)
      .mockResolvedValueOnce(inspection())
      .mockResolvedValueOnce(inspection(localApp, refreshed));
    vi.mocked(api.acceptDeploymentPlan).mockRejectedValueOnce(
      new APIError({
        status: 409,
        code: "deployment_plan_review_required",
        detail: "secret-error-command --token value",
      }),
    );
    renderPanel();
    fireEvent.click(
      await screen.findByRole("button", { name: "Review current source" }),
    );
    await screen.findByRole("heading", { name: "How Rig will run this app" });
    fireEvent.click(screen.getByRole("button", { name: "Accept setup" }));
    expect(
      await screen.findByText(
        "The source changed while this setup was being accepted. Review the updated setup before trying again.",
      ),
    ).not.toBeNull();
    expect(
      await screen.findByDisplayValue("npm run build:refreshed"),
    ).not.toBeNull();
    expect(api.inspect).toHaveBeenCalledTimes(2);
    expect(document.body.textContent).not.toContain("secret-error-command");
  });

  it("loads the winning acceptance head and requires a new review", async () => {
    vi.mocked(api.deploymentPlan)
      .mockResolvedValueOnce(plan())
      .mockResolvedValueOnce(
        plan({ revisionId: "winning-plan", revisionNumber: 8 }),
      );
    vi.mocked(api.acceptDeploymentPlan).mockRejectedValueOnce(
      new APIError({
        status: 409,
        code: "deployment_plan_conflict",
        detail: "secret-conflicting-command",
      }),
    );
    renderPanel();
    fireEvent.click(
      await screen.findByRole("button", { name: "Review current source" }),
    );
    await screen.findByRole("heading", { name: "How Rig will run this app" });
    fireEvent.click(screen.getByRole("button", { name: "Accept setup" }));
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("updated in another session");
    expect(await screen.findByText("Revision 8 · Generated runtime")).not.toBeNull();
    expect(screen.getByRole("button", { name: "Review current source" })).not.toBeNull();
    expect(api.inspect).toHaveBeenCalledTimes(1);
    expect(document.body.textContent).not.toContain("secret-conflicting-command");
  });

  it("approves migration only for the exact accepted revision", async () => {
    const pending = plan({
      migration: {
        present: true,
        approvalStatus: "pending",
        command: "secret-migration-command --password value",
      },
    });
    vi.mocked(api.deploymentPlan).mockResolvedValue(pending);
    vi.mocked(api.approveDeploymentPlanMigration).mockResolvedValue(
      plan({
        migration: {
          present: true,
          approvalStatus: "approved",
          command: "secret-migration-command --password value",
        },
      }),
    );
    renderPanel();
    const approve = await screen.findByRole("button", {
      name: "Approve migration for revision 3",
    });
    expect(document.body.textContent).not.toContain("secret-migration-command");
    fireEvent.click(approve);
    await waitFor(() =>
      expect(api.approveDeploymentPlanMigration).toHaveBeenCalledWith(
        localApp.id,
        {
          revisionId: "plan-revision-3",
          revisionNumber: 3,
          expectedApprovalRevision: 0,
        },
      ),
    );
    expect(await screen.findByText("Database migration approved")).not.toBeNull();
    expect(document.body.textContent).not.toContain("secret-migration-command");
  });

  it("reloads migration conflicts with fixed non-secret recovery", async () => {
    const pending = plan({
      migration: { present: true, approvalStatus: "pending" },
    });
    vi.mocked(api.deploymentPlan)
      .mockResolvedValueOnce(pending)
      .mockResolvedValueOnce(
        plan({ migration: { present: true, approvalStatus: "approved" } }),
      );
    vi.mocked(api.approveDeploymentPlanMigration).mockRejectedValueOnce(
      new APIError({
        status: 409,
        code: "migration_approval_conflict",
        detail: "secret-migration-error-command",
      }),
    );
    renderPanel();
    fireEvent.click(
      await screen.findByRole("button", {
        name: "Approve migration for revision 3",
      }),
    );
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("changed in another session");
    expect(await screen.findByText("Database migration approved")).not.toBeNull();
    expect(document.body.textContent).not.toContain(
      "secret-migration-error-command",
    );
    await waitFor(() => expect(document.activeElement).toBe(alert));
  });

  it("keeps arbitrary plan-load failures out of generic alerts", async () => {
    vi.mocked(api.deploymentPlan).mockRejectedValue(
      new Error("secret-build-command && upload-token"),
    );
    renderPanel();
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain(
      "Rig could not load the accepted deployment setup.",
    );
    expect(alert.textContent).not.toContain("secret-build-command");
    expect(
      screen.getByRole("button", { name: "Retry deployment setup" }),
    ).not.toBeNull();
    await waitFor(() => expect(document.activeElement).toBe(alert));
  });

  it("keeps arbitrary inspection failures out of recovery feedback", async () => {
    vi.mocked(api.inspect).mockRejectedValue(
      new Error("npm run secret-command && send-token"),
    );
    renderPanel();
    fireEvent.click(
      await screen.findByRole("button", { name: "Review current source" }),
    );

    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain(
      "Rig could not analyze the current source.",
    );
    expect(alert.textContent).not.toContain("secret-command");
    expect(
      screen.getByRole("button", { name: "Review current source" }),
    ).not.toBeNull();
    await waitFor(() => expect(document.activeElement).toBe(alert));
  });
});
