import { useEffect, useMemo, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  APIError,
  api,
  type Deployment,
  type Job,
  type Release,
  type RuntimeApproval,
} from "./api";
import { Dialog } from "./dialog";

type Mode = "current" | "original";
type RuntimeStrategy = "compose" | "generated_node";
type RuntimeCapabilities = {
  compose: boolean;
  fake: boolean;
  generated: boolean;
};
type PauseDisposition =
  | "approval_required"
  | "migration_approval_required"
  | "insufficient_replacement_capacity"
  | "route_reconciliation_required";

const runtimeStrategy = (value?: string): RuntimeStrategy | undefined =>
  value === "compose" || value === "generated_node" ? value : undefined;
const runtimeAvailable = (
  strategy: RuntimeStrategy | undefined,
  capabilities: RuntimeCapabilities,
  allowFakeCompose: boolean,
) =>
  strategy === "generated_node"
    ? capabilities.generated
    : strategy === "compose"
      ? capabilities.compose || (allowFakeCompose && capabilities.fake)
      : false;
const pinnedReleaseStrategy = (release: Release): RuntimeStrategy | undefined =>
  runtimeStrategy(release.runtimeStrategy);
const deploymentResult = (item: Deployment) => {
  switch (item.diagnosticCode) {
    case "migration_approval_required":
      return "Database migration approval is required.";
    case "insufficient_replacement_capacity":
      return "More temporary capacity is required for a safe replacement.";
    case "route_reconciliation_required":
      return "Route state must be reconciled before deployment can continue.";
    default:
      return item.failureSummary || item.diagnosticCode || "No failure recorded";
  }
};
const deploymentPlanOrLegacy = async (appId: string) => {
  try {
    return await api.deploymentPlan(appId);
  } catch (error) {
    if (
      error instanceof APIError &&
      error.status === 404 &&
      error.code === "deployment_plan_not_found"
    )
      return null;
    throw error;
  }
};
const stamp = (value?: string) => {
  if (!value) return "Not recorded";
  const date = new Date(value);
  return Number.isNaN(date.valueOf())
    ? "Not recorded"
    : date.toLocaleString([], { dateStyle: "medium", timeStyle: "short" });
};
const sha = (release?: Release) => {
  const value = releaseSha(release);
  return value ? value.slice(0, 12) : "No commit recorded";
};
const releaseSha = (release?: Release) =>
  release?.resolvedSha || release?.sourceCommitSha;
const repositoryDisplayName = (release: Release) => {
  const owner = release.repositoryOwner?.trim();
  const name = release.repositoryName?.trim();
  if (!name) return undefined;
  return owner && !name.startsWith(`${owner}/`) ? `${owner}/${name}` : name;
};
const composeDisplayPath = (value?: string) => {
  const path = value?.trim().replaceAll("\\", "/");
  if (
    !path ||
    path.startsWith("/") ||
    /^[a-z]:\//i.test(path) ||
    path.split("/").includes("..")
  )
    return undefined;
  return path.replace(/^\.\//, "");
};
const statusPresentation = {
  preparing: { label: "Preparing", tone: "active" },
  applying: { label: "Applying", tone: "active" },
  waiting_health: { label: "Waiting for health checks", tone: "active" },
  waiting_user: { label: "Waiting for approval", tone: "waiting" },
  needs_attention: { label: "Needs attention", tone: "attention" },
} as const;
const statusLabel = (value: string) =>
  statusPresentation[value as keyof typeof statusPresentation]?.label ??
  value.replaceAll("_", " ");
const Status = ({ value }: { value: string }) => {
  const presentation =
    statusPresentation[value as keyof typeof statusPresentation];
  return (
    <span
      className={`status ${value.toLowerCase().replaceAll(" ", "-")}${
        presentation ? ` status-${presentation.tone}` : ""
      }`}
    >
      <i aria-hidden="true" />
      <span>{statusLabel(value)}</span>
    </span>
  );
};
const Fingerprint = ({ value }: { value: string }) => (
  <span className="mono" title={value}>
    <span aria-hidden="true">Fingerprint {value.slice(0, 12)}...</span>
    <span className="sr-only">Fingerprint {value}</span>
  </span>
);

function ReleaseProvenance({ release }: { release?: Release }) {
  if (!release) return null;
  const fields: Array<[string, string]> = [];
  if (release.sourceProvider) fields.push(["Provider", release.sourceProvider]);
  const repository = repositoryDisplayName(release);
  if (repository) fields.push(["Repository", repository]);
  if (release.trackedRef) fields.push(["Tracked ref", release.trackedRef]);
  const composePath = composeDisplayPath(release.composePath);
  if (composePath) fields.push(["Compose path", composePath]);
  if (!fields.length) return null;
  return (
    <dl className="release-provenance" aria-label="Release provenance">
      {fields.map(([label, value]) => (
        <div key={label}>
          <dt>{label}</dt>
          <dd className="mono">{value}</dd>
        </div>
      ))}
    </dl>
  );
}

function Section({
  loading,
  error,
  empty,
  loadingLabel,
  emptyLabel,
  retry,
  children,
}: {
  loading: boolean;
  error: Error | null;
  empty: boolean;
  loadingLabel: string;
  emptyLabel: string;
  retry: (activatedElement: HTMLButtonElement) => void;
  children: React.ReactNode;
}) {
  const [showSkeleton, setShowSkeleton] = useState(false);
  useEffect(() => {
    if (!loading) {
      setShowSkeleton(false);
      return;
    }
    const timer = window.setTimeout(() => setShowSkeleton(true), 300);
    return () => window.clearTimeout(timer);
  }, [loading]);
  if (loading)
    return (
      <div className="deployment-loading">
        <span>{loadingLabel}</span>
        {showSkeleton && (
          <div className="deployment-skeletons" aria-hidden="true">
            <i />
            <i />
          </div>
        )}
      </div>
    );
  if (error)
    return (
      <div className="callout danger deployment-query-error" role="alert">
        <strong>Could not load this section.</strong>
        <span>{error.message}</span>
        <button className="button small" type="button" onClick={(event) => retry(event.currentTarget)}>
          Retry
        </button>
      </div>
    );
  return empty ? (
    <p className="deployment-empty">{emptyLabel}</p>
  ) : (
    <>{children}</>
  );
}

function DeploymentRow({
  item,
  releases,
}: {
  item: Deployment;
  releases: Release[];
}) {
  const release = releases.find((candidate) => candidate.id === item.releaseId);
  const findings = item.findings.filter(
    (finding) =>
      finding.disposition === "approval_required" ||
      finding.disposition === "rejected",
  );
  return (
    <article className="deployment-row" aria-label={`Deployment ${item.id.slice(0, 8)}, ${statusLabel(item.status)}`}>
      <div className="deployment-row-primary">
        <Status value={item.status} />
        <strong className="mono">{sha(release)}</strong>
        <span className="muted">
          Release{" "}
          {item.releaseId ? item.releaseId.slice(0, 8) : "current source"}
        </span>
      </div>
      <div className="deployment-meta">
        <small>Configuration</small>
        <span>
          {item.configurationMode}, revision{" "}
          {item.actualConfigurationRevisionNumber}
        </span>
      </div>
      <div className="deployment-meta">
        <small>Started</small>
        <time dateTime={item.startedAt}>{stamp(item.startedAt)}</time>
      </div>
      <div className="deployment-meta">
        <small>Finished</small>
        <time dateTime={item.finishedAt}>{stamp(item.finishedAt)}</time>
      </div>
      <div className="deployment-meta deployment-result">
        <small>Result</small>
        <span>{deploymentResult(item)}</span>
        {findings.length > 0 && (
          <span className="deployment-findings">
            Policy:{" "}
            {findings
              .map(
                (finding) => `${finding.capability} (${finding.disposition})`,
              )
              .join(", ")}
          </span>
        )}
      </div>
      <div className="deployment-diagnostics">
        <div>
          <small>Deployment ID</small>
          <span className="mono">{item.id}</span>
        </div>
        <div>
          <small>Job ID</small>
          <span className="mono">{item.jobId || "Not recorded"}</span>
        </div>
        <div>
          <small>Release ID</small>
          <span className="mono">{item.releaseId || "Not recorded"}</span>
        </div>
        {releaseSha(release) && (
          <div>
            <small>Full commit SHA</small>
            <span className="mono">{releaseSha(release)}</span>
          </div>
        )}
      </div>
      <ReleaseProvenance release={release} />
    </article>
  );
}

const pauseDisposition = (value?: string): PauseDisposition | undefined =>
  value === "approval_required" ||
  value === "migration_approval_required" ||
  value === "insufficient_replacement_capacity" ||
  value === "route_reconciliation_required"
    ? value
    : undefined;
const waiting = (job: Job, appId: string) =>
  job.type === "deploy" &&
  job.resourceType === "application" &&
  job.resourceId === appId &&
  job.status === "waiting_user" &&
  Boolean(pauseDisposition(job.pauseDisposition));

type AppMutation = { appId: string };
type PriorDeployMutation = AppMutation & { release: Release; mode: Mode };
type GrantMutation = AppMutation & { fingerprint: string };
type RevokeMutation = AppMutation & { approvalId: string };
type ResumeMutation = AppMutation & { jobId: string; successMessage: string };

const activeJobStatuses = new Set([
  "queued",
  "assigned",
  "running",
  "waiting_external",
  "waiting_user",
]);
const transientDeploymentStatuses = new Set([
  "preparing",
  "applying",
  "waiting_health",
]);

function deploymentStatusUpdate(appId: string, deployments: Deployment[], jobs: Job[]) {
  const currentDeployment = deployments[0];
  const applicationJobs = jobs.filter(
    (job) =>
      job.type === "deploy" &&
      job.resourceType === "application" &&
      job.resourceId === appId,
  );
  const currentJob = applicationJobs.find((job) => job.id === currentDeployment?.jobId) ??
    applicationJobs.find((job) => activeJobStatuses.has(job.status)) ??
    applicationJobs[0];
  const deploymentSignature = currentDeployment
    ? `${currentDeployment.id}:${currentDeployment.status}:${currentDeployment.diagnosticCode ?? ""}`
    : "none";
  const jobSignature = currentJob
    ? `${currentJob.id}:${currentJob.status}:${currentJob.pauseDisposition ?? ""}`
    : "none";
  const parts = [
    currentDeployment
      ? `Current deployment ${currentDeployment.id.slice(0, 8)} is ${statusLabel(currentDeployment.status)}.`
      : "No current deployment is recorded.",
  ];
  if (currentJob) {
    parts.push(`Deployment job ${currentJob.id.slice(0, 8)} is ${statusLabel(currentJob.status)}.`);
    if (currentJob.pauseDisposition === "approval_required") parts.push("Runtime approval is required.");
    if (currentJob.pauseDisposition === "migration_approval_required") parts.push("Database migration approval is required.");
    if (currentJob.pauseDisposition === "insufficient_replacement_capacity") parts.push("Temporary replacement capacity is required.");
    if (currentJob.pauseDisposition === "route_reconciliation_required") parts.push("Rig preserved both slots because the active route could not be verified. Retry once the local Docker and Caddy runtime is available.");
  }
  return {
    signature: `${appId}:${deploymentSignature}:${jobSignature}`,
    message: parts.join(" "),
  };
}

export function DeploymentHistoryPanel({
  appId,
  composeRuntime,
  fakeRuntime,
  generatedRuntime,
}: {
  appId: string;
  composeRuntime: boolean;
  fakeRuntime: boolean;
  generatedRuntime: boolean;
}) {
  const client = useQueryClient();
  const panelHeadingRef = useRef<HTMLHeadingElement>(null);
  const currentHeadingRef = useRef<HTMLHeadingElement>(null);
  const releasesHeadingRef = useRef<HTMLHeadingElement>(null);
  const approvalsHeadingRef = useRef<HTMLHeadingElement>(null);
  const recentHeadingRef = useRef<HTMLHeadingElement>(null);
  const errorRef = useRef<HTMLDivElement>(null);
  const inFlight = useRef(new Set<string>());
  const currentAppId = useRef(appId);
  currentAppId.current = appId;
  const priorErrorRef = useRef<HTMLDivElement>(null);
  const revokeErrorRef = useRef<HTMLDivElement>(null);
  const grantFocusOrigin = useRef<{ appId: string; element: HTMLButtonElement } | null>(null);
  const resumeFocusOrigin = useRef<{ appId: string; element: HTMLButtonElement } | null>(null);
  const lastStatusSignature = useRef("");
  const [prior, setPrior] = useState<Release | null>(null);
  const [mode, setMode] = useState<Mode>("current");
  const [revokeTarget, setRevokeTarget] = useState<RuntimeApproval | null>(
    null,
  );
  const [message, setMessage] = useState("");
  const restoreFocusAfterRemoval = (
    targetAppId: string,
    activatedElement: HTMLElement | undefined,
    fallback: React.RefObject<HTMLElement | null>,
  ) => {
    if (!activatedElement) return;
    window.setTimeout(() => {
      if (currentAppId.current === targetAppId && !activatedElement.isConnected) fallback.current?.focus();
    }, 0);
  };
  const retrySection = async (
    targetAppId: string,
    activatedElement: HTMLButtonElement,
    fallback: React.RefObject<HTMLElement | null>,
    refetch: () => Promise<unknown>,
  ) => {
    await refetch();
    restoreFocusAfterRemoval(targetAppId, activatedElement, fallback);
  };
  const cachedJobs = client.getQueryData<{ items: Job[] }>(["jobs"]);
  const cachedDeployments = client.getQueryData<{ items: Deployment[] }>([
    "deployments",
    appId,
  ]);
  const shouldConverge = Boolean(
    cachedJobs?.items.some(
      (job) =>
        job.type === "deploy" &&
        job.resourceType === "application" &&
        job.resourceId === appId &&
        activeJobStatuses.has(job.status),
    ) ||
      cachedDeployments?.items.some((item) =>
        transientDeploymentStatuses.has(item.status),
      ),
  );
  const refetchInterval = shouldConverge ? 2_000 : false;
  const deployments = useQuery({
    queryKey: ["deployments", appId],
    queryFn: () => api.deployments(appId),
    refetchInterval,
  });
  const releases = useQuery({
    queryKey: ["releases", appId],
    queryFn: () => api.releases(appId),
    refetchInterval,
  });
  const approvals = useQuery({
    queryKey: ["runtime-approvals", appId],
    queryFn: () => api.runtimeApprovals(appId),
    refetchInterval,
  });
  const jobs = useQuery({
    queryKey: ["jobs"],
    queryFn: api.jobs,
    refetchInterval,
  });
  const deploymentPlan = useQuery({
    queryKey: ["deployment-plan", appId],
    queryFn: () => deploymentPlanOrLegacy(appId),
    refetchInterval,
  });
  const invalidate = (targetAppId: string) =>
    Promise.all(
      [
        ["deployments", targetAppId],
        ["releases", targetAppId],
        ["runtime-approvals", targetAppId],
        ["jobs"],
      ].map((queryKey) => client.invalidateQueries({ queryKey })),
    );
  const current = useMutation({
    mutationFn: ({ appId: targetAppId }: AppMutation) =>
      api.deployApplication(targetAppId),
    onSuccess: async (result, variables) => {
      if (variables.appId === currentAppId.current) {
        setMessage(`Deployment job ${result.job.id} queued.`);
      }
      await invalidate(variables.appId);
    },
    onSettled: (_, __, variables) => {
      inFlight.current.delete(`latest:${variables.appId}`);
    },
  });
  const priorDeploy = useMutation({
    mutationFn: (args: PriorDeployMutation) =>
      api.deployRelease(args.appId, args.release.id, {
        configurationMode: args.mode,
      }),
    onSuccess: async (result, variables) => {
      if (variables.appId === currentAppId.current) {
        setPrior(null);
        setMessage(`Deployment job ${result.job.id} queued.`);
      }
      await invalidate(variables.appId);
    },
    onSettled: (_, __, variables) => {
      inFlight.current.delete(`prior:${variables.appId}`);
    },
  });
  const grant = useMutation({
    mutationFn: ({ appId: targetAppId, fingerprint }: GrantMutation) =>
      api.grantRuntimeApproval(targetAppId, { fingerprint }),
    onSuccess: async (_, variables) => {
      if (variables.appId === currentAppId.current) {
        setMessage(
          "Runtime approval recorded. Resume after every required fingerprint is active.",
        );
      }
      await invalidate(variables.appId);
      const origin = grantFocusOrigin.current;
      if (origin?.appId === variables.appId) restoreFocusAfterRemoval(variables.appId, origin.element, approvalsHeadingRef);
    },
    onSettled: (_, __, variables) => {
      inFlight.current.delete(`grant:${variables.appId}`);
      if (grantFocusOrigin.current?.appId === variables.appId) grantFocusOrigin.current = null;
    },
  });
  const revoke = useMutation({
    mutationFn: ({ appId: targetAppId, approvalId }: RevokeMutation) =>
      api.revokeRuntimeApproval(targetAppId, approvalId),
    onSuccess: async (_, variables) => {
      if (variables.appId === currentAppId.current) {
        setRevokeTarget(null);
        setMessage("Runtime approval revoked.");
      }
      await invalidate(variables.appId);
    },
    onSettled: (_, __, variables) => {
      inFlight.current.delete(`revoke:${variables.appId}`);
    },
  });
  const resume = useMutation({
    mutationFn: ({ jobId }: ResumeMutation) => api.resumeJob(jobId),
    onSuccess: async (_, variables) => {
      if (variables.appId === currentAppId.current) {
        setMessage(variables.successMessage);
      }
      await invalidate(variables.appId);
      const origin = resumeFocusOrigin.current;
      if (origin?.appId === variables.appId) restoreFocusAfterRemoval(variables.appId, origin.element, approvalsHeadingRef);
    },
    onSettled: (_, __, variables) => {
      inFlight.current.delete(`resume:${variables.appId}`);
      if (resumeFocusOrigin.current?.appId === variables.appId) resumeFocusOrigin.current = null;
    },
  });
  useEffect(() => {
    setPrior(null);
    setRevokeTarget(null);
    setMessage("");
    lastStatusSignature.current = "";
    grantFocusOrigin.current = null;
    resumeFocusOrigin.current = null;
    current.reset();
    priorDeploy.reset();
    grant.reset();
    revoke.reset();
    resume.reset();
  }, [appId]);
  const hasActiveVariables = (variables?: AppMutation) =>
    variables?.appId === appId;
  const errors = [current, grant, resume]
    .filter((mutation) => hasActiveVariables(mutation.variables))
    .map((mutation) => mutation.error)
    .filter((error): error is Error => error instanceof Error);
  const priorError =
    hasActiveVariables(priorDeploy.variables) &&
    priorDeploy.error instanceof Error
      ? priorDeploy.error
      : null;
  const revokeError =
    hasActiveVariables(revoke.variables) && revoke.error instanceof Error
      ? revoke.error
      : null;
  useEffect(() => {
    if (errors[0]) errorRef.current?.focus();
  }, [errors[0]]);
  useEffect(() => {
    if (priorError) priorErrorRef.current?.focus();
  }, [priorError]);
  useEffect(() => {
    if (revokeError) revokeErrorRef.current?.focus();
  }, [revokeError]);
  const history = deployments.data?.items ?? [],
    releaseList = releases.data?.items ?? [],
    approvalList = approvals.data?.items ?? [];
  const statusUpdate = deployments.isLoading || jobs.isLoading
    ? { signature: `${appId}:loading`, message: "Loading deployment status." }
    : deploymentStatusUpdate(appId, history, jobs.data?.items ?? []);
  useEffect(() => {
    if (lastStatusSignature.current === statusUpdate.signature) return;
    lastStatusSignature.current = statusUpdate.signature;
    setMessage(statusUpdate.message);
  }, [statusUpdate.message, statusUpdate.signature]);
  const activeApprovalList = approvalList.filter(
    (approval) => !approval.revokedAt,
  );
  const active = new Set(
    activeApprovalList.map((approval) => approval.fingerprint),
  );
  const job = jobs.data?.items.find((candidate) => waiting(candidate, appId));
  const matched = job
    ? history.find((item) => item.jobId === job.id)
    : undefined;
  const required = useMemo(
    () =>
      matched && job?.pauseDisposition === "approval_required"
        ? Array.from(
            new Map(
              matched.findings
                .filter(
                  (finding) => finding.disposition === "approval_required",
                )
                .map((finding) => [finding.fingerprint, finding]),
            ).values(),
          )
        : [],
    [job?.pauseDisposition, matched],
  );
  const capabilities: RuntimeCapabilities = {
    compose: composeRuntime,
    fake: fakeRuntime,
    generated: generatedRuntime,
  };
  const currentStrategy = deploymentPlan.isSuccess
    ? deploymentPlan.data === null
      ? "compose"
      : runtimeStrategy(deploymentPlan.data.strategy)
    : undefined;
  const latestRuntimeAvailable = runtimeAvailable(
    currentStrategy,
    capabilities,
    true,
  );
  const disposition = pauseDisposition(job?.pauseDisposition);
  const matchedStrategy = runtimeStrategy(matched?.runtimeStrategy);
  const resumeRuntimeAvailable = runtimeAvailable(
    matchedStrategy,
    capabilities,
    false,
  );
  const migrationPlanMatches = Boolean(
    matched?.deploymentPlanRevisionId &&
      deploymentPlan.data?.revisionId ===
        matched.deploymentPlanRevisionId &&
      deploymentPlan.data.revisionNumber ===
        matched.deploymentPlanRevisionNumber,
  );
  const migrationApproved = Boolean(
    migrationPlanMatches &&
      deploymentPlan.data?.migration.present &&
      deploymentPlan.data.migration.approvalStatus === "approved",
  );
  const canResume = Boolean(
    job &&
      matched &&
      resumeRuntimeAvailable &&
      (disposition === "approval_required"
        ? required.length > 0 &&
          required.every((finding) => active.has(finding.fingerprint))
        : disposition === "migration_approval_required"
          ? migrationApproved
          : disposition === "insufficient_replacement_capacity" ||
            disposition === "route_reconciliation_required"),
  );
  const hasPendingMutationForCurrentApp = () =>
    [...inFlight.current].some((key) => key.endsWith(`:${appId}`));
  const deployLatest = () => {
    const key = `latest:${appId}`;
    if (
      inFlight.current.has(key) ||
      hasPendingMutationForCurrentApp() ||
      !latestRuntimeAvailable
    )
      return;
    inFlight.current.add(key);
    current.mutate({ appId });
  };
  const deployPrior = (release: Release) => {
    const key = `prior:${appId}`;
    if (
      inFlight.current.has(key) ||
      hasPendingMutationForCurrentApp() ||
      !runtimeAvailable(
        pinnedReleaseStrategy(release),
        capabilities,
        false,
      )
    )
      return;
    inFlight.current.add(key);
    priorDeploy.mutate({ appId, release, mode });
  };
  const grantApproval = (fingerprint: string, activatedElement: HTMLButtonElement) => {
    const key = `grant:${appId}`;
    if (inFlight.current.has(key) || hasPendingMutationForCurrentApp()) return;
    inFlight.current.add(key);
    grantFocusOrigin.current = { appId, element: activatedElement };
    grant.mutate({ appId, fingerprint });
  };
  const revokeApproval = (approvalId: string) => {
    const key = `revoke:${appId}`;
    if (inFlight.current.has(key) || hasPendingMutationForCurrentApp()) return;
    inFlight.current.add(key);
    revoke.mutate({ appId, approvalId });
  };
  const resumeWaitingJob = (jobId: string, successMessage: string, activatedElement: HTMLButtonElement) => {
    const key = `resume:${appId}`;
    if (
      inFlight.current.has(key) ||
      hasPendingMutationForCurrentApp() ||
      !canResume
    )
      return;
    inFlight.current.add(key);
    resumeFocusOrigin.current = { appId, element: activatedElement };
    resume.mutate({ appId, jobId, successMessage });
  };
  const currentPending =
    current.isPending && hasActiveVariables(current.variables);
  const priorPending =
    priorDeploy.isPending && hasActiveVariables(priorDeploy.variables);
  const grantPending = grant.isPending && hasActiveVariables(grant.variables);
  const revokePending =
    revoke.isPending && hasActiveVariables(revoke.variables);
  const resumePending =
    resume.isPending && hasActiveVariables(resume.variables);
  const panelMutationPending =
    currentPending ||
    priorPending ||
    grantPending ||
    revokePending ||
    resumePending;
  const selectedPriorStrategy = prior
    ? pinnedReleaseStrategy(prior)
    : undefined;
  const selectedPriorRuntimeAvailable = runtimeAvailable(
    selectedPriorStrategy,
    capabilities,
    false,
  );
  const selectedPriorRuntimeUnavailableMessage = !selectedPriorStrategy
    ? "Rig cannot verify the runtime pinned to this release."
    : selectedPriorStrategy === "generated_node"
      ? "The generated runtime pinned to this release is not available on this controller."
      : "The Compose runtime pinned to this release is not available on this controller.";
  const latestUnavailableMessage = deploymentPlan.isLoading
    ? "Checking which runtime this application requires."
    : currentStrategy === "generated_node"
      ? "Deploy latest requires the generated runtime on this controller."
      : currentStrategy === "compose"
        ? "Deploy latest requires the Compose runtime or the development fake runtime."
        : "Rig could not verify the runtime required by the current deployment plan.";
  return (
    <section
      className="deployment-panel"
      aria-labelledby="deployment-history-title"
    >
      <div className="deployment-heading">
        <div>
          <h2 id="deployment-history-title" ref={panelHeadingRef} tabIndex={-1}>Deployment history</h2>
          <p>
            Release provenance, policy decisions, and recoverable deployment
            work.
          </p>
        </div>
        <button
          type="button"
          className="button primary"
          disabled={panelMutationPending || !latestRuntimeAvailable}
          aria-describedby={
            !latestRuntimeAvailable ? "deploy-latest-availability" : undefined
          }
          onClick={deployLatest}
        >
          {currentPending ? "Queuing..." : "Deploy latest"}
        </button>
      </div>
      {deploymentPlan.isError ? (
        <div
          id="deploy-latest-availability"
          className="callout danger deployment-query-error"
          role="alert"
        >
          <strong>Could not verify the deployment runtime.</strong>
          <span>Deployment actions remain disabled until Rig reloads the accepted plan.</span>
          <button
            className="button small"
            type="button"
            onClick={(event) => void retrySection(appId, event.currentTarget, panelHeadingRef, deploymentPlan.refetch)}
          >
            Retry runtime check
          </button>
        </div>
      ) : !latestRuntimeAvailable ? (
        <p id="deploy-latest-availability" className="deployment-empty">
          {latestUnavailableMessage}
        </p>
      ) : null}
      <p
        className="deployment-message"
        role="status"
        aria-live="polite"
        aria-atomic="true"
      >
        {message}
      </p>
      {errors.length > 0 && (
        <div
          ref={errorRef}
          className="callout danger"
          tabIndex={-1}
          role="alert"
        >
          {errors[0].message}
        </div>
      )}
      <div className="deployment-section">
        <div className="deployment-section-heading">
          <h3 ref={currentHeadingRef} tabIndex={-1}>Current deployment</h3>
          <span>
            {history[0] ? (
              <Status value={history[0].status} />
            ) : (
              "No deployment recorded"
            )}
          </span>
        </div>
        <Section
          loading={deployments.isLoading}
          error={deployments.error}
          empty={!history.length}
          loadingLabel="Loading current deployment..."
          emptyLabel="No deployment has been recorded yet."
          retry={(activatedElement) => void retrySection(appId, activatedElement, currentHeadingRef, deployments.refetch)}
        >
          <DeploymentRow item={history[0]!} releases={releaseList} />
        </Section>
      </div>
      <div className="deployment-section">
        <div className="deployment-section-heading">
          <h3 ref={releasesHeadingRef} tabIndex={-1}>Release history</h3>
          <span>{releaseList.length} recorded</span>
        </div>
        <Section
          loading={releases.isLoading}
          error={releases.error}
          empty={!releaseList.length}
          loadingLabel="Loading release history..."
          emptyLabel="No releases are available yet."
          retry={(activatedElement) => void retrySection(appId, activatedElement, releasesHeadingRef, releases.refetch)}
        >
          {releaseList.map((release) => {
            const ready = release.workspaceState === "ready";
            const strategy = pinnedReleaseStrategy(release);
            const strategyAvailable = runtimeAvailable(
              strategy,
              capabilities,
              false,
            );
            const unavailable = !ready || !strategyAvailable;
            const unavailableMessage = !ready
              ? "This release is not ready for deployment."
              : !strategy
                ? "Rig cannot yet verify this release's pinned runtime."
                : strategy === "generated_node"
                  ? "This release requires the generated runtime."
                  : "This release requires the Compose runtime.";
            return (
              <article className="release-row" key={release.id} aria-label={`Release ${sha(release)}, revision ${release.configurationRevisionNumber}`}>
                <div>
                  <strong className="mono">{sha(release)}</strong>
                  <span>
                    Revision {release.configurationRevisionNumber},{" "}
                    {stamp(release.createdAt)}
                  </span>
                  <div className="release-diagnostics">
                    <span>
                      <small>Release ID</small>
                      <b className="mono">{release.id}</b>
                    </span>
                    {releaseSha(release) && (
                      <span>
                        <small>Full commit SHA</small>
                        <b className="mono">{releaseSha(release)}</b>
                      </span>
                    )}
                  </div>
                  <ReleaseProvenance release={release} />
                </div>
                <div className="release-actions">
                  <Status value={release.workspaceState || "unavailable"} />
                  <button
                    type="button"
                    className="button small"
                    disabled={unavailable || panelMutationPending}
                    aria-describedby={
                      unavailable ? `${release.id}-availability` : undefined
                    }
                    onClick={() => {
                      setMode("current");
                      setPrior(release);
                    }}
                  >
                    Deploy release
                  </button>
                  {unavailable && (
                    <span className="sr-only" id={`${release.id}-availability`}>
                      {unavailableMessage}
                    </span>
                  )}
                </div>
              </article>
            );
          })}
        </Section>
      </div>
      <div className="deployment-section">
        <div className="deployment-section-heading">
          <h3 ref={approvalsHeadingRef} tabIndex={-1}>Runtime approvals</h3>
          <span>{activeApprovalList.length} active</span>
        </div>
        <Section
          loading={approvals.isLoading}
          error={approvals.error}
          empty={
            approvals.isSuccess &&
            jobs.isSuccess &&
            !required.length &&
            !activeApprovalList.length &&
            !job
          }
          loadingLabel="Loading runtime approvals..."
          emptyLabel="No runtime approvals or waiting policy findings."
          retry={(activatedElement) => void retrySection(appId, activatedElement, approvalsHeadingRef, approvals.refetch)}
        >
          {required.map((finding) => (
            <article className="approval-row" key={finding.fingerprint} aria-label={`Runtime approval required for ${finding.capability}, ${finding.scope}`}>
              <div>
                <strong>{finding.capability}</strong>
                <Fingerprint value={finding.fingerprint} />
                <span>{finding.scope}</span>
              </div>
              {active.has(finding.fingerprint) ? (
                <span className="approval-state">Approved</span>
              ) : (
                <button
                  type="button"
                  className="button small"
                  disabled={panelMutationPending || jobs.isLoading}
                  onClick={(event) => grantApproval(finding.fingerprint, event.currentTarget)}
                >
                  {grantPending &&
                  grant.variables?.fingerprint === finding.fingerprint
                    ? "Approving..."
                    : "Grant approval"}
                </button>
              )}
            </article>
          ))}
          {activeApprovalList
            .filter(
              (approval) =>
                !required.some(
                  (finding) => finding.fingerprint === approval.fingerprint,
                ),
            )
            .map((approval) => (
              <article
                className="approval-row active-approval"
                key={approval.id}
                aria-label={`Active runtime approval for ${approval.capability}`}
              >
                <div>
                  <strong>{approval.capability}</strong>
                  <Fingerprint value={approval.fingerprint} />
                  <span>Granted {stamp(approval.grantedAt)}</span>
                </div>
                <button
                  type="button"
                  className="button small"
                  disabled={panelMutationPending}
                  onClick={() => setRevokeTarget(approval)}
                >
                  Revoke approval
                </button>
              </article>
            ))}
          <Section
            loading={jobs.isLoading}
            error={jobs.error}
            empty={!job}
            loadingLabel="Checking waiting deployment jobs..."
            emptyLabel="No waiting deployment requires action."
            retry={(activatedElement) => void retrySection(appId, activatedElement, approvalsHeadingRef, jobs.refetch)}
          >
            {job && disposition && (
              <>
                <div className="callout warning">
                <Status value={job.status} />
                  {disposition === "approval_required" && (
                    <>
                      <strong>Deployment is waiting for approval.</strong>
                      <span>
                        {matched
                          ? "Grant every listed matching fingerprint before resuming this job."
                          : "The matching deployment record is not available yet."}
                      </span>
                      {canResume && (
                        <button
                          type="button"
                          className="button small"
                          disabled={panelMutationPending}
                          onClick={(event) =>
                            resumeWaitingJob(
                              job.id,
                              "Waiting deployment resumed.",
                              event.currentTarget,
                            )
                          }
                        >
                          {resumePending ? "Resuming..." : "Resume waiting job"}
                        </button>
                      )}
                    </>
                  )}
                  {disposition === "migration_approval_required" && (
                    <>
                      <strong>Database migration approval required.</strong>
                      <span>
                        {migrationApproved
                          ? "The migration is approved for this deployment plan. Resume the deployment when ready."
                          : "Review and approve the database migration in the deployment plan panel before resuming this deployment."}
                      </span>
                      {!resumeRuntimeAvailable && (
                        <span>The runtime pinned to this deployment is not available on this controller.</span>
                      )}
                      {canResume && (
                        <button
                          type="button"
                          className="button small"
                          disabled={panelMutationPending}
                          onClick={(event) =>
                            resumeWaitingJob(
                              job.id,
                              "Deployment resumed after migration approval.",
                              event.currentTarget,
                            )
                          }
                        >
                          {resumePending
                            ? "Resuming deployment..."
                            : "Resume after migration approval"}
                        </button>
                      )}
                    </>
                  )}
                  {disposition === "insufficient_replacement_capacity" && (
                    <>
                      <strong>Deployment needs temporary replacement capacity.</strong>
                      <span>
                        Blue/green replacement briefly runs both versions and needs enough free memory and disk. Free capacity, then retry.
                      </span>
                      {!resumeRuntimeAvailable && (
                        <span>The runtime pinned to this deployment is not available on this controller.</span>
                      )}
                      {canResume && (
                        <button
                          type="button"
                          className="button small"
                          disabled={panelMutationPending}
                          onClick={(event) =>
                            resumeWaitingJob(
                              job.id,
                              "Replacement capacity retry queued.",
                              event.currentTarget,
                            )
                          }
                        >
                          {resumePending
                            ? "Retrying replacement capacity..."
                            : "Retry replacement capacity"}
                        </button>
                      )}
                    </>
                  )}
                  {disposition === "route_reconciliation_required" && (
                    <>
                      <strong>Deployment route state needs reconciliation.</strong>
                      <span>
                        Rig could not prove whether the old or new slot is serving, so it preserved both. Confirm the local Docker runtime and Caddy are available, then retry; Rig will attest the candidate and reconcile the route before changing the active slot.
                      </span>
                      {!resumeRuntimeAvailable && (
                        <span>The generated runtime pinned to this deployment is not available on this controller.</span>
                      )}
                      {canResume && (
                        <button
                          type="button"
                          className="button small"
                          disabled={panelMutationPending}
                          onClick={(event) =>
                            resumeWaitingJob(
                              job.id,
                              "Route reconciliation retry queued.",
                              event.currentTarget,
                            )
                          }
                        >
                          {resumePending
                            ? "Retrying route reconciliation..."
                            : "Retry route reconciliation"}
                        </button>
                      )}
                    </>
                  )}
                </div>
              </>
            )}
          </Section>
        </Section>
      </div>
      <div className="deployment-section">
        <div className="deployment-section-heading">
          <h3 ref={recentHeadingRef} tabIndex={-1}>Recent deployments</h3>
          <span>{history.length} recorded</span>
        </div>
        <Section
          loading={deployments.isLoading}
          error={deployments.error}
          empty={!history.length}
          loadingLabel="Loading deployment history..."
          emptyLabel="No deployments have been recorded yet."
          retry={(activatedElement) => void retrySection(appId, activatedElement, recentHeadingRef, deployments.refetch)}
        >
          <div className="deployment-list">
            {history.map((item) => (
              <DeploymentRow key={item.id} item={item} releases={releaseList} />
            ))}
          </div>
        </Section>
      </div>
      {prior && (
        <Dialog
          title="Deploy prior release"
          close={() => setPrior(null)}
          pending={priorPending}
        >
          {priorError && (
            <div
              ref={priorErrorRef}
              className="callout danger"
              tabIndex={-1}
              role="alert"
            >
              {priorError.message}
            </div>
          )}
          <p>
            Choose the configuration revision for{" "}
            <span className="mono">{sha(prior)}</span>.
          </p>
          <fieldset className="deployment-mode">
            <legend>Configuration revision</legend>
            <label>
              <input
                type="radio"
                name="configuration-mode"
                checked={mode === "current"}
                onChange={() => setMode("current")}
              />{" "}
              Current configuration
            </label>
            <label>
              <input
                type="radio"
                name="configuration-mode"
                checked={mode === "original"}
                onChange={() => setMode("original")}
              />{" "}
              Original release configuration
            </label>
          </fieldset>
          <div className="deployment-dialog-actions">
            <button
              type="button"
              className="button"
              disabled={panelMutationPending}
              onClick={() => setPrior(null)}
            >
              Cancel
            </button>
            <button
              type="button"
              className="button primary"
              disabled={priorPending || !selectedPriorRuntimeAvailable}
              aria-describedby={
                !selectedPriorRuntimeAvailable
                  ? "selected-prior-runtime-unavailable"
                  : undefined
              }
              onClick={() => deployPrior(prior)}
            >
              {priorPending ? "Queuing..." : "Deploy release"}
            </button>
            {!selectedPriorRuntimeAvailable && (
              <span id="selected-prior-runtime-unavailable" className="sr-only">
                {selectedPriorRuntimeUnavailableMessage}
              </span>
            )}
          </div>
        </Dialog>
      )}
      {revokeTarget && (
        <Dialog
          title="Revoke runtime approval"
          close={() => setRevokeTarget(null)}
          pending={revokePending}
        >
          {revokeError && (
            <div
              ref={revokeErrorRef}
              className="callout danger"
              tabIndex={-1}
              role="alert"
            >
              {revokeError.message}
            </div>
          )}
          <p>
            Revoke the active approval for{" "}
            <Fingerprint value={revokeTarget.fingerprint} />? A deployment
            already using it cannot be changed.
          </p>
          <div className="deployment-dialog-actions">
            <button
              type="button"
              className="button"
              disabled={panelMutationPending}
              onClick={() => setRevokeTarget(null)}
            >
              Cancel
            </button>
            <button
              type="button"
              className="button danger-button"
              disabled={revokePending}
              onClick={() => revokeApproval(revokeTarget.id)}
            >
              {revokePending ? "Revoking..." : "Revoke approval"}
            </button>
          </div>
        </Dialog>
      )}
    </section>
  );
}
