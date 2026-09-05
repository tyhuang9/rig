import { useEffect, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  APIError,
  api,
  type AcceptDeploymentPlanRequest,
  type Application,
  type DeploymentPlanRevision,
  type InspectRequest,
  type InspectResponse,
} from "./api";
import { DeploymentPlanReview } from "./deployment-plan-review";

const noPlan = (error: unknown) =>
  error instanceof APIError &&
  error.status === 404 &&
  error.code === "deployment_plan_not_found";

async function loadDeploymentPlan(appId: string) {
  try {
    return await api.deploymentPlan(appId);
  } catch (error) {
    if (noPlan(error)) return null;
    throw error;
  }
}

export function applicationInspectionRequest(
  app: Application,
): InspectRequest | null {
  const source = app.source;
  if (source.type === "local") {
    return source.path?.trim() ? { sourcePath: source.path } : null;
  }
  if (
    source.type !== "github" ||
    !source.connectionId?.trim() ||
    !Number.isInteger(source.installationId) ||
    (source.installationId ?? 0) < 1 ||
    !Number.isInteger(source.repositoryId) ||
    (source.repositoryId ?? 0) < 1 ||
    !source.trackedBranch?.trim()
  ) {
    return null;
  }
  return {
    githubSource: {
      connectionId: source.connectionId,
      installationId: source.installationId!,
      repositoryId: source.repositoryId!,
      branch: source.trackedBranch,
      ...(source.composePath ? { composePath: source.composePath } : {}),
    },
  };
}

const sourceIdentity = (app: Application) =>
  JSON.stringify({ appId: app.id, request: applicationInspectionRequest(app) });

const runtimeLabel = (strategy?: string) =>
  strategy === "generated_node"
    ? "Generated runtime"
    : strategy === "compose"
      ? "Compose runtime"
      : "Runtime not recognized";

export function ApplicationPlanPanel({ app }: { app: Application }) {
  const queryClient = useQueryClient();
  const contextKey = sourceIdentity(app);
  const context = useRef(contextKey);
  context.current = contextKey;
  const inspectionGeneration = useRef(0);
  const acceptanceGeneration = useRef(0);
  const migrationGeneration = useRef(0);
  const heading = useRef<HTMLHeadingElement>(null);
  const errorSummary = useRef<HTMLDivElement>(null);
  const [reviewing, setReviewing] = useState(false);
  const [inspection, setInspection] = useState<InspectResponse | null>(null);
  const [inspectionContext, setInspectionContext] = useState("");
  const [inspectionRevision, setInspectionRevision] = useState(0);
  const [inspectionPending, setInspectionPending] = useState(false);
  const [accepting, setAccepting] = useState(false);
  const [approvingMigration, setApprovingMigration] = useState(false);
  const [panelError, setPanelError] = useState("");
  const [reviewError, setReviewError] = useState("");
  const [announcement, setAnnouncement] = useState("");
  const plan = useQuery({
    queryKey: ["deployment-plan", app.id],
    queryFn: () => loadDeploymentPlan(app.id),
    retry: false,
  });

  useEffect(() => {
    context.current = contextKey;
    inspectionGeneration.current += 1;
    acceptanceGeneration.current += 1;
    migrationGeneration.current += 1;
    setReviewing(false);
    setInspection(null);
    setInspectionContext("");
    setInspectionRevision(0);
    setInspectionPending(false);
    setAccepting(false);
    setApprovingMigration(false);
    setPanelError("");
    setReviewError("");
    setAnnouncement("");
    return () => {
      if (context.current === contextKey) context.current = "";
      inspectionGeneration.current += 1;
      acceptanceGeneration.current += 1;
      migrationGeneration.current += 1;
    };
  }, [contextKey]);

  useEffect(() => {
    if (!panelError && !plan.isError) return;
    const timer = window.setTimeout(() => errorSummary.current?.focus(), 0);
    return () => window.clearTimeout(timer);
  }, [panelError, plan.isError]);

  const focusHeading = () =>
    window.setTimeout(() => heading.current?.focus(), 0);

  const updatePlan = (revision: DeploymentPlanRevision | null) => {
    queryClient.setQueryData(["deployment-plan", app.id], revision);
  };

  const reloadAcceptedPlan = async (operationContext: string) => {
    const revision = await loadDeploymentPlan(app.id);
    if (context.current !== operationContext) return undefined;
    updatePlan(revision);
    return revision;
  };

  const analyze = async (reason: "requested" | "stale" = "requested") => {
    const request = applicationInspectionRequest(app);
    if (!request) {
      setPanelError(
        "Rig cannot analyze this application because its saved source details are incomplete.",
      );
      setAnnouncement("Source analysis is unavailable.");
      return;
    }
    const operationContext = context.current;
    const expectedRevisionNumber = plan.data?.revisionNumber ?? 0;
    const generation = inspectionGeneration.current + 1;
    inspectionGeneration.current = generation;
    setReviewing(true);
    setInspection(null);
    setInspectionPending(true);
    setPanelError("");
    setReviewError("");
    setAnnouncement("Analyzing the current application source.");
    try {
      const result = await api.inspect(request);
      if (
        context.current !== operationContext ||
        inspectionGeneration.current !== generation
      )
        return;
      setInspection(result);
      setInspectionContext(operationContext);
      setInspectionRevision(expectedRevisionNumber);
      if (reason === "stale") {
        setReviewError(
          "The source changed while this setup was being accepted. Review the updated setup before trying again.",
        );
        setAnnouncement("Updated source analysis is ready for review.");
      } else {
        setAnnouncement("Current source analysis is ready for review.");
      }
    } catch {
      if (
        context.current !== operationContext ||
        inspectionGeneration.current !== generation
      )
        return;
      setReviewing(false);
      setPanelError(
        "Rig could not analyze the current source. Check source access and try again.",
      );
      setAnnouncement("Source analysis failed.");
    } finally {
      if (
        context.current === operationContext &&
        inspectionGeneration.current === generation
      )
        setInspectionPending(false);
    }
  };

  const accept = async (request: AcceptDeploymentPlanRequest) => {
    const operationContext = context.current;
    const generation = acceptanceGeneration.current + 1;
    acceptanceGeneration.current = generation;
    setAccepting(true);
    setReviewError("");
    setAnnouncement("Accepting the reviewed deployment setup.");
    try {
      const revision = await api.acceptDeploymentPlan(app.id, request);
      if (
        context.current !== operationContext ||
        acceptanceGeneration.current !== generation
      )
        return;
      updatePlan(revision);
      setReviewing(false);
      setInspection(null);
      setInspectionContext("");
      setAnnouncement(`Deployment setup revision ${revision.revisionNumber} accepted.`);
      focusHeading();
    } catch (error) {
      if (
        context.current !== operationContext ||
        acceptanceGeneration.current !== generation
      )
        return;
      if (
        error instanceof APIError &&
        error.code === "deployment_plan_review_required"
      ) {
        await analyze("stale");
      } else if (
        error instanceof APIError &&
        error.code === "deployment_plan_conflict"
      ) {
        try {
          await reloadAcceptedPlan(operationContext);
          if (context.current !== operationContext) return;
          setReviewing(false);
          setInspection(null);
          setInspectionContext("");
          setPanelError(
            "The deployment setup was updated in another session. Review the current source again before making another change.",
          );
          setAnnouncement("A newer accepted deployment setup was loaded.");
        } catch {
          if (context.current !== operationContext) return;
          setReviewing(false);
          setInspectionContext("");
          setPanelError(
            "The deployment setup changed, but Rig could not load the accepted revision. Try again.",
          );
          setAnnouncement("The accepted deployment setup could not be reloaded.");
        }
      } else {
        setReviewError(
          "Rig could not accept this deployment setup. Analyze the current source and try again.",
        );
        setAnnouncement("Deployment setup was not accepted.");
      }
    } finally {
      if (
        context.current === operationContext &&
        acceptanceGeneration.current === generation
      )
        setAccepting(false);
    }
  };

  const approveMigration = async (revision: DeploymentPlanRevision) => {
    if (!revision.revisionId) {
      setPanelError(
        "Rig cannot verify the accepted revision required to approve this migration.",
      );
      return;
    }
    const operationContext = context.current;
    const generation = migrationGeneration.current + 1;
    migrationGeneration.current = generation;
    setApprovingMigration(true);
    setPanelError("");
    setAnnouncement("Approving the database migration.");
    try {
      const approved = await api.approveDeploymentPlanMigration(app.id, {
        revisionId: revision.revisionId,
        revisionNumber: revision.revisionNumber,
        expectedApprovalRevision: 0,
      });
      if (
        context.current !== operationContext ||
        migrationGeneration.current !== generation
      )
        return;
      updatePlan(approved);
      setAnnouncement(
        `Database migration approved for deployment setup revision ${approved.revisionNumber}.`,
      );
      focusHeading();
    } catch (error) {
      if (
        context.current !== operationContext ||
        migrationGeneration.current !== generation
      )
        return;
      if (
        error instanceof APIError &&
        (error.code === "migration_approval_conflict" ||
          error.code === "deployment_plan_conflict")
      ) {
        try {
          await reloadAcceptedPlan(operationContext);
          if (context.current !== operationContext) return;
          setPanelError(
            "The migration approval changed in another session. Rig loaded the current deployment setup; review it before trying again.",
          );
          setAnnouncement("The current migration approval state was loaded.");
        } catch {
          if (context.current !== operationContext) return;
          setPanelError(
            "The migration approval changed, but Rig could not reload it. Try again.",
          );
          setAnnouncement("The migration approval state could not be reloaded.");
        }
      } else {
        setPanelError(
          "Rig could not approve this database migration. Review the accepted setup and try again.",
        );
        setAnnouncement("The database migration was not approved.");
      }
    } finally {
      if (
        context.current === operationContext &&
        migrationGeneration.current === generation
      )
        setApprovingMigration(false);
    }
  };

  if (reviewing && inspection && inspectionContext === contextKey) {
    return (
      <div className="application-plan-review">
        <span
          className="sr-only"
          role="status"
          aria-live="polite"
          aria-atomic="true"
        >
          {announcement}
        </span>
        <DeploymentPlanReview
          key={`${contextKey}:${inspection.analysis.structuralFingerprint}`}
          inspection={inspection}
          expectedRevisionNumber={inspectionRevision}
          pending={accepting}
          error={reviewError}
          onBack={() => {
            setReviewing(false);
            setInspection(null);
            setInspectionContext("");
            setReviewError("");
            setAnnouncement("Returned to the accepted deployment setup.");
            focusHeading();
          }}
          onRefresh={() => void analyze()}
          onAccept={(request) => void accept(request)}
        />
      </div>
    );
  }

  const accepted = plan.data ?? null;
  const sourceRequest = applicationInspectionRequest(app);
  const busy =
    plan.isLoading || inspectionPending || accepting || approvingMigration;

  return (
    <section
      className="plan-review application-plan-panel"
      aria-labelledby="application-plan-title"
      aria-busy={busy}
    >
      <h2 id="application-plan-title" ref={heading} tabIndex={-1}>
        Deployment setup
      </h2>
      <p>
        Review how Rig builds and runs the current application source before a
        deployment uses it.
      </p>
      <span
        className="sr-only"
        role="status"
        aria-live="polite"
        aria-atomic="true"
      >
        {announcement}
      </span>

      {(panelError || plan.isError) && (
        <div
          ref={errorSummary}
          className="error-summary"
          role="alert"
          tabIndex={-1}
        >
          {panelError ||
            "Rig could not load the accepted deployment setup. Try again."}
          {plan.isError && (
            <button
              className="button small"
              type="button"
              onClick={() => void plan.refetch()}
            >
              Retry deployment setup
            </button>
          )}
        </div>
      )}

      {plan.isLoading ? (
        <p role="status" aria-busy="true">
          Loading accepted deployment setup…
        </p>
      ) : plan.isSuccess ? (
        <>
          {accepted ? (
            <article className="component-plan">
              <header>
                <div>
                  <h3>Accepted deployment setup</h3>
                  <p>
                    Revision {accepted.revisionNumber} · {runtimeLabel(accepted.strategy)}
                  </p>
                </div>
                <span className="badge">Accepted</span>
              </header>
              <p>
                Rig will keep using this immutable revision until a reviewed
                source change is accepted.
              </p>
            </article>
          ) : (
            <div className="callout info">
              <strong>Legacy Compose setup</strong>
              <span>
                No generated deployment plan has been accepted for this
                application. Review the source to create one.
              </span>
            </div>
          )}

          {accepted?.migration.present && (
            <div
              className={
                accepted.migration.approvalStatus === "approved"
                  ? "callout success"
                  : "migration-review"
              }
            >
              <strong>
                {accepted.migration.approvalStatus === "approved"
                  ? "Database migration approved"
                  : "Database migration needs separate approval"}
              </strong>
              {accepted.migration.approvalStatus === "approved" ? (
                <span>
                  The migration is approved for deployment setup revision {accepted.revisionNumber}.
                </span>
              ) : (
                <>
                  <p>
                    This can change persistent data. Old and new versions may
                    briefly share the migrated database, and Rig will not
                    automatically roll it back.
                  </p>
                  <button
                    className="button"
                    type="button"
                    disabled={approvingMigration || !accepted.revisionId}
                    onClick={() => void approveMigration(accepted)}
                  >
                    {approvingMigration
                      ? "Approving migration…"
                      : `Approve migration for revision ${accepted.revisionNumber}`}
                  </button>
                </>
              )}
            </div>
          )}

          {!sourceRequest && (
            <div id="application-plan-source-unavailable" className="callout warning">
              <strong>Saved source details are incomplete</strong>
              <span>
                Rig cannot safely reconstruct the current source for analysis.
              </span>
            </div>
          )}
          <footer>
            <button
              className="button primary"
              type="button"
              disabled={!sourceRequest || busy}
              aria-describedby={
                sourceRequest ? undefined : "application-plan-source-unavailable"
              }
              onClick={() => void analyze()}
            >
              {inspectionPending ? "Analyzing current source…" : "Review current source"}
            </button>
          </footer>
        </>
      ) : null}
    </section>
  );
}
