import { useEffect, useId, useMemo, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  api,
  type Deployment,
  type Job,
  type Release,
  type RuntimeApproval,
} from "./api";

type Mode = "current" | "original";
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
  retry: () => void;
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
      <div className="deployment-loading" role="status" aria-live="polite">
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
        <button className="button small" type="button" onClick={retry}>
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

function Dialog({
  title,
  close,
  pending = false,
  children,
}: {
  title: string;
  close: () => void;
  pending?: boolean;
  children: React.ReactNode;
}) {
  const ref = useRef<HTMLDivElement>(null);
  const titleId = useId();
  const restore = useRef<HTMLElement | null>(
    document.activeElement instanceof HTMLElement
      ? document.activeElement
      : null,
  );
  const closeRef = useRef(close);
  closeRef.current = close;
  const pendingRef = useRef(pending);
  pendingRef.current = pending;
  useEffect(() => {
    const element = ref.current;
    const root = document.getElementById("root");
    const previousAriaHidden = root?.getAttribute("aria-hidden");
    const previousInert = root?.inert;
    root?.setAttribute("aria-hidden", "true");
    if (root) root.inert = true;
    const focusable = () => [
      ...(element?.querySelectorAll<HTMLElement>(
        "button:not([disabled]), input:not([disabled])",
      ) ?? []),
    ];
    (focusable()[0] ?? element)?.focus();
    const keydown = (event: KeyboardEvent) => {
      if (event.key === "Escape" && !pendingRef.current) {
        event.preventDefault();
        closeRef.current();
        return;
      }
      if (event.key !== "Tab") return;
      const targets = focusable();
      if (!targets.length) {
        event.preventDefault();
        element?.focus();
        return;
      }
      const first = targets[0],
        last = targets.at(-1)!;
      const activeIndex = targets.indexOf(document.activeElement as HTMLElement);
      if (activeIndex === -1) {
        event.preventDefault();
        (event.shiftKey ? last : first).focus();
        return;
      }
      if (event.shiftKey && activeIndex === 0) {
        event.preventDefault();
        last.focus();
      }
      if (!event.shiftKey && activeIndex === targets.length - 1) {
        event.preventDefault();
        first.focus();
      }
    };
    document.addEventListener("keydown", keydown);
    return () => {
      document.removeEventListener("keydown", keydown);
      if (root) {
        if (previousAriaHidden === null) root.removeAttribute("aria-hidden");
        else root.setAttribute("aria-hidden", previousAriaHidden ?? "");
        root.inert = previousInert ?? false;
      }
      restore.current?.focus();
    };
  }, []);
  return createPortal(
    <div className="deployment-dialog-backdrop" role="presentation">
      <div
        ref={ref}
        className="deployment-dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        tabIndex={-1}
      >
        <h2 id={titleId}>{title}</h2>
        {children}
      </div>
    </div>,
    document.body,
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
    <article className="deployment-row">
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
        <span>
          {item.failureSummary || item.diagnosticCode || "No failure recorded"}
        </span>
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

const waiting = (job: Job, appId: string) =>
  job.type === "deploy" &&
  job.resourceType === "application" &&
  job.resourceId === appId &&
  job.status === "waiting_user" &&
  job.pauseDisposition === "approval_required";

type AppMutation = { appId: string };
type PriorDeployMutation = AppMutation & { release: Release; mode: Mode };
type GrantMutation = AppMutation & { fingerprint: string };
type RevokeMutation = AppMutation & { approvalId: string };
type ResumeMutation = AppMutation & { jobId: string };

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

export function DeploymentHistoryPanel({
  appId,
  composeRuntime,
  fakeRuntime,
}: {
  appId: string;
  composeRuntime: boolean;
  fakeRuntime: boolean;
}) {
  const client = useQueryClient();
  const errorRef = useRef<HTMLDivElement>(null);
  const inFlight = useRef(new Set<string>());
  const currentAppId = useRef(appId);
  currentAppId.current = appId;
  const priorErrorRef = useRef<HTMLDivElement>(null);
  const revokeErrorRef = useRef<HTMLDivElement>(null);
  const [prior, setPrior] = useState<Release | null>(null);
  const [mode, setMode] = useState<Mode>("current");
  const [revokeTarget, setRevokeTarget] = useState<RuntimeApproval | null>(
    null,
  );
  const [message, setMessage] = useState("");
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
    },
    onSettled: (_, __, variables) => {
      inFlight.current.delete(`grant:${variables.appId}`);
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
        setMessage("Waiting deployment resumed.");
      }
      await invalidate(variables.appId);
    },
    onSettled: (_, __, variables) => {
      inFlight.current.delete(`resume:${variables.appId}`);
    },
  });
  useEffect(() => {
    setPrior(null);
    setRevokeTarget(null);
    setMessage("");
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
      matched
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
    [matched],
  );
  const runtimeAvailable = composeRuntime || fakeRuntime;
  const hasPendingMutationForCurrentApp = () =>
    [...inFlight.current].some((key) => key.endsWith(`:${appId}`));
  const deployLatest = () => {
    const key = `latest:${appId}`;
    if (
      inFlight.current.has(key) ||
      hasPendingMutationForCurrentApp() ||
      !runtimeAvailable
    )
      return;
    inFlight.current.add(key);
    current.mutate({ appId });
  };
  const deployPrior = (release: Release) => {
    const key = `prior:${appId}`;
    if (inFlight.current.has(key) || hasPendingMutationForCurrentApp()) return;
    inFlight.current.add(key);
    priorDeploy.mutate({ appId, release, mode });
  };
  const grantApproval = (fingerprint: string) => {
    const key = `grant:${appId}`;
    if (inFlight.current.has(key) || hasPendingMutationForCurrentApp()) return;
    inFlight.current.add(key);
    grant.mutate({ appId, fingerprint });
  };
  const revokeApproval = (approvalId: string) => {
    const key = `revoke:${appId}`;
    if (inFlight.current.has(key) || hasPendingMutationForCurrentApp()) return;
    inFlight.current.add(key);
    revoke.mutate({ appId, approvalId });
  };
  const resumeWaitingJob = (jobId: string) => {
    const key = `resume:${appId}`;
    if (inFlight.current.has(key) || hasPendingMutationForCurrentApp()) return;
    inFlight.current.add(key);
    resume.mutate({ appId, jobId });
  };
  const canResume = Boolean(
    composeRuntime &&
      job &&
      matched &&
      required.length &&
      required.every((finding) => active.has(finding.fingerprint)),
  );
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
  return (
    <section
      className="deployment-panel"
      aria-labelledby="deployment-history-title"
    >
      <div className="deployment-heading">
        <div>
          <h2 id="deployment-history-title">Deployment history</h2>
          <p>
            Release provenance, policy decisions, and recoverable deployment
            work.
          </p>
        </div>
        <button
          type="button"
          className="button primary"
          disabled={panelMutationPending || !runtimeAvailable}
          onClick={deployLatest}
        >
          {currentPending ? "Queuing..." : "Deploy latest"}
        </button>
      </div>
      {!runtimeAvailable && (
        <p className="deployment-empty">
          Deployment actions require a configured runtime.
        </p>
      )}
      {message && (
        <p className="deployment-message" role="status">
          {message}
        </p>
      )}
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
          <h3>Current deployment</h3>
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
          retry={() => deployments.refetch()}
        >
          <DeploymentRow item={history[0]!} releases={releaseList} />
        </Section>
      </div>
      <div className="deployment-section">
        <div className="deployment-section-heading">
          <h3>Release history</h3>
          <span>{releaseList.length} recorded</span>
        </div>
        <Section
          loading={releases.isLoading}
          error={releases.error}
          empty={!releaseList.length}
          loadingLabel="Loading release history..."
          emptyLabel="No releases are available yet."
          retry={() => releases.refetch()}
        >
          {releaseList.map((release) => {
            const ready = release.workspaceState === "ready";
            return (
              <article className="release-row" key={release.id}>
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
                    disabled={!ready || panelMutationPending || !composeRuntime}
                    aria-describedby={
                      !ready || !composeRuntime
                        ? `${release.id}-availability`
                        : undefined
                    }
                    onClick={() => {
                      setMode("current");
                      setPrior(release);
                    }}
                  >
                    Deploy release
                  </button>
                  {(!ready || !composeRuntime) && (
                    <span className="sr-only" id={`${release.id}-availability`}>
                      {!ready
                        ? "This release is not ready for deployment."
                        : "Prior-release deployment requires the compose runtime."}
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
          <h3>Runtime approvals</h3>
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
          retry={() => approvals.refetch()}
        >
          {required.map((finding) => (
            <article className="approval-row" key={finding.fingerprint}>
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
                  onClick={() => grantApproval(finding.fingerprint)}
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
            emptyLabel="No waiting deployment requires approval."
            retry={() => jobs.refetch()}
          >
            {job && (
              <div className="callout warning">
                <Status value={job.status} />
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
                    onClick={() => resumeWaitingJob(job.id)}
                  >
                    {resumePending ? "Resuming..." : "Resume waiting job"}
                  </button>
                )}
              </div>
            )}
          </Section>
        </Section>
      </div>
      <div className="deployment-section">
        <div className="deployment-section-heading">
          <h3>Recent deployments</h3>
          <span>{history.length} recorded</span>
        </div>
        <Section
          loading={deployments.isLoading}
          error={deployments.error}
          empty={!history.length}
          loadingLabel="Loading deployment history..."
          emptyLabel="No deployments have been recorded yet."
          retry={() => deployments.refetch()}
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
              disabled={priorPending}
              onClick={() => deployPrior(prior)}
            >
              {priorPending ? "Queuing..." : "Deploy release"}
            </button>
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
