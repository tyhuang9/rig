import { useEffect, useMemo, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { api, type Application, type ApplicationConfiguration, type Deployment, type DeploymentPlanRevision } from "./api";
import { ApplicationConfigurationPanel } from "./application-configuration";

type SetupStep = "configuration" | "access" | "review" | "result";
type RevisionPins = { planId: string; planNumber: number; configurationId: string; configurationNumber: number };
type DeploymentAttempt = { signature: string; key: string; jobId?: string; reviewedPins?: RevisionPins };

const terminalStatuses = new Set(["succeeded", "failed", "cancelled", "interrupted", "needs_attention"]);

function attemptStorageKey(appId: string) {
  return `rig-setup-deployment:${appId}`;
}

function readAttempt(appId: string): DeploymentAttempt | null {
  try {
    const value = JSON.parse(window.sessionStorage.getItem(attemptStorageKey(appId)) || "null");
    return value && typeof value.signature === "string" && typeof value.key === "string" &&
      (value.jobId === undefined || typeof value.jobId === "string") ? value as DeploymentAttempt : null;
  } catch {
    return null;
  }
}

function saveAttempt(appId: string, attempt: DeploymentAttempt) {
  window.sessionStorage.setItem(attemptStorageKey(appId), JSON.stringify(attempt));
}

function configurationMatchesPlan(configuration: ApplicationConfiguration, plan: DeploymentPlanRevision) {
  if (configuration.revisionNumber === 0 && configuration.entries.length === 0) return true;
  return configuration.formatVersion === 2 &&
    configuration.deploymentPlanRevisionId === plan.revisionId &&
    configuration.deploymentPlanRevisionNumber === plan.revisionNumber;
}

function revisionSignature(plan: DeploymentPlanRevision, configuration: ApplicationConfiguration) {
  return `${plan.revisionId}:${plan.revisionNumber}:${configuration.revisionId ?? "initial"}:${configuration.revisionNumber}`;
}

function deploymentMatchesReviewedPins(deployment: Deployment | undefined, attempt: DeploymentAttempt | null): boolean | null {
  if (!deployment || !attempt?.reviewedPins) return null;
  const pins = attempt.reviewedPins;
  return deployment.deploymentPlanRevisionId === pins.planId &&
    deployment.deploymentPlanRevisionNumber === pins.planNumber &&
    (deployment.actualConfigurationRevisionId ?? "") === pins.configurationId &&
    deployment.actualConfigurationRevisionNumber === pins.configurationNumber;
}

export function ApplicationDeploymentSetup({ app }: { app: Application }) {
  return <ApplicationDeploymentSetupContent key={app.id} app={app}/>;
}

function ApplicationDeploymentSetupContent({ app }: { app: Application }) {
  const client = useQueryClient();
  const attempt = useRef<DeploymentAttempt | null>(readAttempt(app.id));
  const [step, setStep] = useState<SetupStep>(attempt.current?.jobId ? "result" : "configuration");
  const [jobId, setJobId] = useState(attempt.current?.jobId ?? "");
  const [requestError, setRequestError] = useState("");
  const [continuePending, setContinuePending] = useState(false);
  const [continueError, setContinueError] = useState("");
  const [recoveryOpen, setRecoveryOpen] = useState(false);
  const [recoveryAcknowledged, setRecoveryAcknowledged] = useState(false);
  const [persistenceError, setPersistenceError] = useState("");
  const heading = useRef<HTMLHeadingElement>(null);
  const plan = useQuery({ queryKey: ["deployment-plan", app.id], queryFn: () => api.deploymentPlan(app.id), retry: false });
  const configuration = useQuery({ queryKey: ["app-configuration", app.id], queryFn: () => api.applicationConfiguration(app.id), retry: false });
  const status = useQuery({ queryKey: ["system-status"], queryFn: api.status, retry: false });
  const signature = plan.data && configuration.data ? revisionSignature(plan.data, configuration.data) : "";
  const jobQuery = useQuery({ queryKey: ["job", jobId], queryFn: () => api.job(jobId), enabled: step === "result" && Boolean(jobId), refetchInterval: (query) => step === "result" && jobId && !terminalStatuses.has(query.state.data?.status ?? "") ? 1500 : false, retry: false });
  const job = jobQuery.data;
  const deployments = useQuery({ queryKey: ["deployments", app.id], queryFn: () => api.deployments(app.id), enabled: step === "result" && Boolean(jobId), refetchInterval: (query) => step === "result" && jobId && !(job && terminalStatuses.has(job.status) && job.status !== "succeeded") && !query.state.data?.items.some((item) => item.jobId === jobId && terminalStatuses.has(item.status)) ? 1500 : false, retry: false });
  const deployment = deployments.data?.items.find((item) => item.jobId === jobId);
  const reviewedPinMatch = deploymentMatchesReviewedPins(deployment, attempt.current);
  const migrationPending = Boolean(plan.data?.migration.present && plan.data.migration.approvalStatus !== "approved");
  const configurationReady = Boolean(plan.data && configuration.data && configurationMatchesPlan(configuration.data, plan.data));
  const uncertainPreviousAttempt = Boolean(attempt.current && !attempt.current.jobId && attempt.current.signature !== signature);
  const canDeploy = !uncertainPreviousAttempt && !plan.isError && !configuration.isError && !status.isError && plan.data?.state === "accepted" && plan.data?.strategy === "generated_node" && Boolean(plan.data.revisionId) && configurationReady && !migrationPending && status.data?.capabilities.generatedRuntime === true && status.data.capabilities.fakeRuntime !== true;

  useEffect(() => {
    if (!signature) return;
    const saved = attempt.current;
    if (!saved) {
      setJobId("");
      setStep("configuration");
      return;
    }
    if (saved.jobId) {
      setJobId(saved.jobId);
      setStep("result");
    } else {
      setStep("review");
    }
  }, [app.id, signature]);

  useEffect(() => {
    heading.current?.focus();
  }, [step]);

  const deploy = useMutation({
    mutationFn: async () => {
      if (!canDeploy || !signature) throw new Error("Review the current plan and configuration before deploying.");
      const [latestPlan, latestConfiguration, latestStatus] = await Promise.all([plan.refetch(), configuration.refetch(), status.refetch()]);
      if (latestPlan.isError || latestConfiguration.isError || latestStatus.isError ||
          !latestPlan.data || !latestConfiguration.data || !latestStatus.data ||
          latestPlan.data.state !== "accepted" || latestPlan.data.strategy !== "generated_node" || !latestPlan.data.revisionId ||
          !configurationMatchesPlan(latestConfiguration.data, latestPlan.data) ||
          latestPlan.data.migration.present && latestPlan.data.migration.approvalStatus !== "approved" ||
          !latestStatus.data.capabilities.generatedRuntime || latestStatus.data.capabilities.fakeRuntime ||
          revisionSignature(latestPlan.data, latestConfiguration.data) !== signature) {
        throw new Error("The plan, configuration, or runtime changed. Review the current setup before deploying.");
      }
      if (!attempt.current || attempt.current.signature !== signature) {
        attempt.current = {
          signature,
          key: crypto.randomUUID(),
          reviewedPins: {
            planId: latestPlan.data.revisionId,
            planNumber: latestPlan.data.revisionNumber,
            configurationId: latestConfiguration.data.revisionId ?? "",
            configurationNumber: latestConfiguration.data.revisionNumber,
          },
        };
        saveAttempt(app.id, attempt.current);
      }
      const submittedAttempt = attempt.current;
      const result = await api.deployApplication(app.id, submittedAttempt.key);
      return { result, submittedAttempt };
    },
    onSuccess: ({ result, submittedAttempt }) => {
      attempt.current = { ...submittedAttempt, jobId: result.job.id };
      try {
        saveAttempt(app.id, attempt.current);
      } catch {
        setPersistenceError("This browser could not save the job ID for reload. Keep this page open or use application history to find the job.");
      }
      setRequestError("");
      setJobId(result.job.id);
      client.setQueryData(["job", result.job.id], result.job);
      setStep("result");
      void Promise.allSettled([client.invalidateQueries({ queryKey: ["job", result.job.id] }), client.invalidateQueries({ queryKey: ["deployments", app.id] }), client.invalidateQueries({ queryKey: ["jobs"] })]);
    },
    onError: (error) => setRequestError(error instanceof Error ? error.message : "Rig could not confirm the deployment request."),
  });

  const proceedFromConfiguration = async () => {
    if (continuePending) return;
    setContinuePending(true);
    setContinueError("");
    try {
      const [latestPlan, latestConfiguration] = await Promise.all([plan.refetch(), configuration.refetch()]);
      if (latestPlan.isError || latestConfiguration.isError || !latestPlan.data || !latestConfiguration.data) {
        setContinueError("Rig could not verify the saved plan and configuration. Retry the check before continuing.");
      } else if (latestPlan.data.state !== "accepted" || latestPlan.data.strategy !== "generated_node" || !configurationMatchesPlan(latestConfiguration.data, latestPlan.data)) {
        setContinueError("The accepted plan or configuration changed. Review its scopes and save a matching revision, then retry.");
      } else {
        setStep("access");
      }
    } catch {
      setContinueError("Rig could not verify the saved plan and configuration. Retry the check before continuing.");
    } finally {
      setContinuePending(false);
    }
  };
  const startNewAttempt = () => {
    attempt.current = null;
    window.sessionStorage.removeItem(attemptStorageKey(app.id));
    setJobId("");
    setRequestError("");
    setRecoveryOpen(false);
    setRecoveryAcknowledged(false);
    setStep("review");
  };
  const resetUnresolvedAttempt = () => {
    if (!recoveryAcknowledged || deploy.isPending) return;
    startNewAttempt();
    setStep("configuration");
  };
  const sourceLabel = app.source.type === "github"
    ? `${app.source.repositoryOwner ?? ""}/${app.source.repositoryName ?? ""}${app.source.trackedBranch ? ` · ${app.source.trackedBranch}` : ""}`
    : app.source.path || "Local source";
  const resultMessage = useMemo(() => {
    if (jobQuery.isError || deployments.isError) return "Rig could not load the latest job and deployment records. Retry the status check.";
    if (!job) return "Checking the durable deployment job.";
    if (job.status === "succeeded" && deployment?.status === "succeeded") {
      if (reviewedPinMatch === false) return "The controller recorded success, but the deployed plan or configuration differs from the revision reviewed here. Check deployment history before relying on this route.";
      if (reviewedPinMatch === null) return "The controller recorded success, but this setup cannot verify which plan and configuration revisions were deployed. Check deployment history.";
      return "The controller recorded a successful job and deployment for the reviewed revisions. An attested access URL is not available from this API.";
    }
    if (terminalStatuses.has(job.status) && job.status !== "succeeded") return job.errorDetail || deployment?.failureSummary || `The job ended with status ${job.status}.`;
    if (job.status === "waiting_user") return "Deployment needs your action. Open the application to review the required approval or recovery step.";
    if (deployment && deployment.status !== "succeeded") return `The matching deployment is ${deployment.status}. Review its record in application history before treating the route as active.`;
    if (job.status === "succeeded") return "The job succeeded. Waiting for the matching deployment record before reporting success.";
    return `Deployment job is ${job.status}.`;
  }, [deployment, deployments.isError, job, jobQuery.isError, reviewedPinMatch]);

  const showingKnownJob = step === "result" && Boolean(jobId);
  if (!showingKnownJob && (plan.isLoading || configuration.isLoading || status.isLoading)) return <div role="status">Loading saved setup…</div>;
  if (!showingKnownJob && (plan.isError || configuration.isError || status.isError)) return <div className="callout danger" role="alert"><strong>{continueError ? "Could not verify the saved setup." : "Could not load the saved setup."}</strong><span>{continueError || plan.error?.message || configuration.error?.message || status.error?.message}</span><button className="button small" type="button" onClick={() => void Promise.all([plan.refetch(), configuration.refetch(), status.refetch()])}>Retry loading</button></div>;
  if (!showingKnownJob && (!plan.data || !configuration.data || !status.data || plan.data.state !== "accepted" || plan.data.strategy !== "generated_node")) return <div className="callout warning"><strong>Generated setup is unavailable.</strong><span>Open the application to review its current deployment plan.</span><Link className="button small" to={`/apps/${app.id}`}>Open application</Link></div>;

  return <div className="wizard setup-wizard">
    <ol aria-label="Deployment setup progress">
      <li aria-current={step === "configuration" ? "step" : undefined}>Configuration</li>
      <li aria-current={step === "access" ? "step" : undefined}>Access</li>
      <li aria-current={step === "review" ? "step" : undefined}>Review and deploy</li>
      <li aria-current={step === "result" ? "step" : undefined}>Job result</li>
    </ol>
    <div className="setup-content">
      {step === "configuration" && <>
        <h2 ref={heading} tabIndex={-1}>Configure application</h2>
        <p>Choose exact components and execution scopes. Server secrets stay on this controller and are not loaded back into the browser.</p>
        <ApplicationConfigurationPanel appId={app.id} onContinue={() => void proceedFromConfiguration()} continuing={continuePending}/>
        {continuePending && <p role="status" aria-live="polite">Checking the accepted plan and saved configuration…</p>}
        {continueError && <div className="callout danger" role="alert"><strong>Cannot continue to access.</strong><span>{continueError}</span><button className="button small" type="button" disabled={continuePending} onClick={() => void proceedFromConfiguration()}>Retry setup check</button></div>}
        {!configurationReady && <div className="callout warning" role="alert">The saved configuration belongs to another plan revision. Review and save its scopes before continuing.</div>}
      </>}
      {step === "access" && <section aria-labelledby="setup-access-title">
        <h2 id="setup-access-title" ref={heading} tabIndex={-1}>Access</h2>
        <div className="callout info"><strong>Local controller route</strong><span>Rig will route this deployment through the controller's local Caddy runtime. Public domain setup and an attested external URL are not available in this flow.</span></div>
        <p>Keep external database credentials in server runtime secrets for the selected component. Rig does not provision a database.</p>
        <div className="setup-actions"><button className="button" type="button" onClick={() => setStep("configuration")}>Back to configuration</button><button className="button primary" type="button" onClick={() => setStep("review")}>Review deployment</button></div>
      </section>}
      {step === "review" && <section aria-labelledby="setup-review-title">
        <h2 id="setup-review-title" ref={heading} tabIndex={-1}>Review and deploy</h2>
        <dl className="setup-facts"><div><dt>Application</dt><dd>{app.name}</dd></div><div><dt>Source</dt><dd>{sourceLabel}</dd></div><div><dt>Accepted plan</dt><dd>Revision {plan.data?.revisionNumber ?? "unavailable"}</dd></div><div><dt>Scoped configuration</dt><dd>Revision {configuration.data?.revisionNumber ?? "unavailable"}</dd></div><div><dt>Migration</dt><dd>{plan.data?.migration.present ? migrationPending ? "Approval required" : "Approved for this plan" : "None"}</dd></div><div><dt>Access</dt><dd>Local controller route</dd></div></dl>
        {!configurationReady && <div className="callout warning" role="alert">Configuration no longer matches this accepted plan. Review its scopes again before deploying.</div>}
        {uncertainPreviousAttempt && <div className="callout warning" role="alert"><strong>Previous deployment request is unresolved.</strong><span>The setup changed after Rig sent a request. Its request key is preserved. Review the application history before starting another deployment.</span><Link className="button small" to={`/apps/${app.id}`}>Open application history</Link>{!deploy.isPending && <button className="button small" type="button" onClick={() => setRecoveryOpen(true)}>Resolve uncertain request</button>}{recoveryOpen && <div className="setup-recovery"><p>Rig cannot prove from this page whether the earlier request created a job. Starting again may queue another deployment. Check durable activity and deployment history first.</p><label><input type="checkbox" checked={recoveryAcknowledged} onChange={(event) => setRecoveryAcknowledged(event.target.checked)}/> I checked application history and accept the risk of another deployment job.</label><button className="button small" type="button" disabled={!recoveryAcknowledged || deploy.isPending} onClick={resetUnresolvedAttempt}>Start a new setup request</button></div>}</div>}
        {migrationPending && <div className="callout warning" role="alert">The database migration needs separate approval in the application plan. Deployment is disabled until it is approved.</div>}
        {!status.data?.capabilities.generatedRuntime && <div className="callout warning" role="alert">The generated runtime is unavailable on this controller.</div>}
        {status.data?.capabilities.fakeRuntime && <div className="callout warning" role="alert">The development fake runtime cannot attest an application deployment.</div>}
        {requestError && <div className="callout danger" role="alert"><strong>Deployment request was not confirmed.</strong><span>{requestError}</span><span>Retry uses the same request key, so an uncertain response does not create a second job.</span></div>}
        <div className="setup-actions"><button className="button" type="button" disabled={deploy.isPending} onClick={() => setStep("access")}>Back to access</button><button className="button primary" type="button" disabled={!canDeploy || deploy.isPending} onClick={() => deploy.mutate()}>{deploy.isPending ? "Queuing…" : requestError ? "Retry deployment request" : "Deploy application"}</button></div>
      </section>}
      {step === "result" && <section aria-labelledby="setup-result-title">
        <h2 id="setup-result-title" ref={heading} tabIndex={-1}>Deployment job</h2>
        <p className="mono">Job {jobId}</p>
        {persistenceError && <div className="callout warning" role="alert">{persistenceError}</div>}
        <div className={reviewedPinMatch === false || job && (job.status === "waiting_user" || terminalStatuses.has(job.status) && job.status !== "succeeded") ? "callout warning" : "callout info"} role="status" aria-live="polite"><strong>{job ? `Job ${job.status}` : "Checking job"}</strong><span>{resultMessage}</span></div>
        {deployment && <dl className="setup-facts"><div><dt>Deployment ID</dt><dd className="mono">{deployment.id}</dd></div><div><dt>Deployment status</dt><dd>{deployment.status}</dd></div>{deployment.releaseId && <div><dt>Release ID</dt><dd className="mono">{deployment.releaseId}</dd></div>}<div><dt>Plan revision</dt><dd>{deployment.deploymentPlanRevisionNumber}</dd></div><div><dt>Configuration revision</dt><dd>{deployment.actualConfigurationRevisionNumber}</dd></div></dl>}
        {jobQuery.isError || deployments.isError ? <button className="button" type="button" onClick={() => void Promise.all([jobQuery.refetch(), deployments.refetch()])}>Retry status check</button> : null}
        <div className="setup-actions"><Link className="button primary" to={`/apps/${app.id}`}>Open application and history</Link>{job && terminalStatuses.has(job.status) && <button className="button" type="button" onClick={startNewAttempt}>Review a new deployment</button>}</div>
      </section>}
    </div>
  </div>;
}
