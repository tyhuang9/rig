import { useEffect, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  APIError,
  api,
  type ApplicationAutoDeployStatus,
  type RelayStatus,
  type SourceConnection,
} from "./api";

type UpdateMutation = { appId: string; expectedRevision: number; enabled: boolean };
type ResumeMutation = { appId: string; expectedRevision: number };

const knownAutoDeployStates = new Set([
  "disabled",
  "idle",
  "dispatching",
  "deploying",
  "paused",
  "retry_wait",
]);
const relayAvailabilities = new Set(["initializing", "available", "unavailable"]);

const formatTime = (value?: string) => {
  if (!value) return "Not recorded";
  const date = new Date(value);
  return Number.isNaN(date.valueOf())
    ? "Not recorded"
    : date.toLocaleString([], { dateStyle: "medium", timeStyle: "short" });
};

const shortSHA = (value: string) => value.slice(0, 12);

function SHA({ label, value }: { label: string; value?: string }) {
  const full = value?.trim();
  return <div>
    <dt>{label}</dt>
    <dd className="mono auto-deploy-sha" title={full}>
      {full ? <><span aria-hidden="true">{shortSHA(full)}</span><span className="sr-only">{full}</span></> : "Not recorded"}
    </dd>
  </div>;
}

function isAutoDeployStatus(value: ApplicationAutoDeployStatus | undefined): value is ApplicationAutoDeployStatus {
  return Boolean(
    value &&
      typeof value.applicationId === "string" &&
      typeof value.revision === "number" &&
      typeof value.enabled === "boolean" &&
      typeof value.state === "string" &&
      value.state.trim().length > 0 &&
      value.source &&
      typeof value.source.type === "string",
  );
}

function isRelayStatus(value: RelayStatus | undefined): value is RelayStatus {
  return Boolean(value && relayAvailabilities.has(value.availability));
}

function pauseDescription(code?: string) {
  switch (code) {
    case "approval_required":
      return "A deployment approval is required. Review Deployment history below; auto-deploy cannot resume this approval step.";
    case "deployment_failed":
      return "The previous auto-deployment failed. After resolving the failure, choose Resume to ask Rig to revalidate and retry.";
    case "missing_configuration":
      return "Deployment configuration is missing. After adding the required configuration, choose Resume to ask Rig to revalidate and retry.";
    case "source_access_lost":
      return "GitHub access has been lost. Reconnect GitHub in the existing source connection flow, return here, choose Retry connection check, then Resume to ask Rig to revalidate and retry.";
    case "invalid_source":
      return "The tracked GitHub source is invalid. After correcting the source, choose Resume to ask Rig to revalidate and retry.";
    case "provider_unavailable":
      return "GitHub is currently unavailable. After GitHub recovers, choose Resume to ask Rig to revalidate and retry.";
    case "relay_unavailable":
      return "The relay is unavailable. After the relay recovers, choose Resume to ask Rig to revalidate and retry.";
    default:
      return `Auto-deploy is paused${code ? ` (${code})` : ""}. After resolving the issue, choose Resume to ask Rig to revalidate and retry.`;
  }
}

function mutationMessage(error: Error) {
  if (!(error instanceof APIError)) return "We could not update auto-deploy. Reload the status and try again.";
  switch (error.code) {
    case "auto_deploy_conflict":
      return "Auto-deploy changed elsewhere. Reload status before making another change.";
    case "application_busy":
      return "This application is busy. Wait for the current operation to finish, then try again.";
    case "auto_deploy_prerequisite_missing":
      return "Rig could not verify all controller-side prerequisites, including repository relay authorization and controller access. Complete the required setup, then reload status; auto-deploy was not changed.";
    case "capability_unavailable":
      return "This controller does not currently support auto-deploy. Check runtime and GitHub capabilities.";
    case "auto_deploy_state_conflict":
    case "invalid_source":
    case "source_access_lost":
      return "The application source or state is not ready for auto-deploy. Review the source and reload status.";
    default:
      return "We could not update auto-deploy. Reload the status and try again.";
  }
}

function statePresentation(status: ApplicationAutoDeployStatus) {
  switch (status.state) {
    case "disabled": return { label: "Off", detail: "Automatic deployments are off." };
    case "idle": return { label: "Watching", detail: "Watching the tracked GitHub ref for changes." };
    case "dispatching": return { label: "Preparing deployment", detail: "A deployment is being prepared. Settings are temporarily locked." };
    case "deploying": return { label: "Deploying", detail: "An auto-deployment is in progress. Settings are temporarily locked." };
    case "retry_wait": {
      const schedule = status.nextRetryAt ? ` at ${formatTime(status.nextRetryAt)}` : "";
      const attempt = status.retryAttempt && status.retryAttempt > 0
        ? `Retry attempt ${status.retryAttempt}`
        : "A retry";
      return { label: "Retry scheduled", detail: `${attempt} is scheduled${schedule}.` };
    }
    case "paused": return { label: "Paused", detail: pauseDescription(status.pauseCode) };
    default: return { label: "Status unavailable", detail: "Auto-deploy returned an unsupported state." };
  }
}

function stateAnnouncement(status: ApplicationAutoDeployStatus) {
  const pauseCode = status.pauseCode?.trim();
  const retryAttempt = status.state === "retry_wait" && status.retryAttempt && status.retryAttempt > 0
    ? `, attempt ${status.retryAttempt}`
    : "";
  return `Auto-deploy state: ${statePresentation(status).label}${pauseCode ? ` (${pauseCode})` : ""}${retryAttempt}.`;
}

function stateSignature(appId: string, status: ApplicationAutoDeployStatus) {
  const retryAttempt = status.state === "retry_wait" ? `:${status.retryAttempt ?? 0}` : "";
  return `${appId}:${status.state}:${status.pauseCode?.trim() ?? ""}${retryAttempt}`;
}

function panelAnnouncement(
  appId: string,
  status: ApplicationAutoDeployStatus,
  relayError: boolean,
  sourceError: boolean,
) {
  const notices = [
    relayError ? "Relay status is unavailable." : "",
    sourceError ? "GitHub connection status is unavailable." : "",
  ].filter(Boolean);
  return {
    signature: `${stateSignature(appId, status)}:relay-error=${relayError}:source-error=${sourceError}`,
    message: [stateAnnouncement(status), ...notices].join(" "),
  };
}

export function AutoDeployPanel({
  appId,
  composeRuntime,
  githubConnections,
}: {
  appId: string;
  composeRuntime: boolean;
  githubConnections: boolean;
}) {
  const client = useQueryClient();
  const headingRef = useRef<HTMLHeadingElement>(null);
  const errorRef = useRef<HTMLDivElement>(null);
  const inFlight = useRef(new Set<string>());
  const reloadInFlight = useRef(new Set<string>());
  const lastAnnouncedSignature = useRef("");
  const currentAppId = useRef(appId);
  currentAppId.current = appId;
  const [message, setMessage] = useState("");
  const [pendingAppIds, setPendingAppIds] = useState(() => new Set<string>());
  const [reloadPendingAppIds, setReloadPendingAppIds] = useState(() => new Set<string>());
  const [reloadErrors, setReloadErrors] = useState(() => new Map<string, Error>());
  const autoDeploy = useQuery({
    queryKey: ["application-auto-deploy", appId],
    queryFn: () => api.getApplicationAutoDeploy(appId),
    retry: false,
    refetchInterval: (query) => {
      if (query.state.status === "error" || query.state.fetchFailureCount > 0) return false;
      const status = query.state.data;
      return status && knownAutoDeployStates.has(status.state) && ["dispatching", "deploying", "retry_wait"].includes(status.state) ||
        Boolean(status?.state === "paused" && status.activeJobId) ? 2_000 : false;
    },
  });
  const prerequisiteQueriesEnabled = isAutoDeployStatus(autoDeploy.data) &&
    autoDeploy.data.applicationId === appId &&
    autoDeploy.data.source.type === "github";
  const relay = useQuery({
    queryKey: ["relay-status"],
    queryFn: api.relayStatus,
    retry: false,
    enabled: prerequisiteQueriesEnabled,
    refetchInterval: (query) => {
      if (query.state.status === "error" || query.state.fetchFailureCount > 0) return false;
      return query.state.data?.availability === "initializing" ? 2_000 : false;
    },
  });
  const sourceConnections = useQuery({
    queryKey: ["source-connections"],
    queryFn: api.sourceConnections,
    retry: false,
    enabled: prerequisiteQueriesEnabled,
  });
  const relayQueryError = prerequisiteQueriesEnabled && relay.isError;
  const sourceConnectionsQueryError = prerequisiteQueriesEnabled && sourceConnections.isError;
  const setAppPending = (targetAppId: string, pending: boolean) => {
    if (pending) inFlight.current.add(`auto-deploy:${targetAppId}`);
    else inFlight.current.delete(`auto-deploy:${targetAppId}`);
    setPendingAppIds((current) => {
      const next = new Set(current);
      if (pending) next.add(targetAppId);
      else next.delete(targetAppId);
      return next;
    });
  };
  const requireManualReload = (targetAppId: string, error: Error) => {
    setReloadErrors((current) => new Map(current).set(targetAppId, error));
  };
  const clearManualReload = (targetAppId: string) => {
    setReloadErrors((current) => {
      const next = new Map(current);
      next.delete(targetAppId);
      return next;
    });
  };
  const invalidateStatus = async (targetAppId: string) => {
    const key = ["application-auto-deploy", targetAppId];
    await client.invalidateQueries({ queryKey: key, exact: true });
  };
  const update = useMutation({
    mutationFn: ({ appId: targetAppId, expectedRevision, enabled }: UpdateMutation) =>
      api.updateApplicationAutoDeploy(targetAppId, { expectedRevision, enabled }),
    onSuccess: async (result, variables) => {
      if (!isAutoDeployStatus(result) || result.applicationId !== variables.appId) {
        requireManualReload(variables.appId, new Error("The controller returned status for a different application. Reload status before making another change."));
      } else if (variables.appId === currentAppId.current) {
        client.setQueryData(["application-auto-deploy", variables.appId], result);
        lastAnnouncedSignature.current = panelAnnouncement(
          variables.appId,
          result,
          relayQueryError,
          sourceConnectionsQueryError,
        ).signature;
        setMessage("Auto-deploy status updated.");
      }
      await invalidateStatus(variables.appId);
    },
    onError: (error, variables) => requireManualReload(
      variables.appId,
      error instanceof Error ? error : new Error("Auto-deploy could not be updated."),
    ),
    onSettled: (_, __, variables) => {
      setAppPending(variables.appId, false);
    },
  });
  const resume = useMutation({
    mutationFn: ({ appId: targetAppId, expectedRevision }: ResumeMutation) =>
      api.resumeApplicationAutoDeploy(targetAppId, { expectedRevision }),
    onSuccess: async (result, variables) => {
      if (!isAutoDeployStatus(result) || result.applicationId !== variables.appId) {
        requireManualReload(variables.appId, new Error("The controller returned status for a different application. Reload status before making another change."));
      } else if (variables.appId === currentAppId.current) {
        client.setQueryData(["application-auto-deploy", variables.appId], result);
        lastAnnouncedSignature.current = panelAnnouncement(
          variables.appId,
          result,
          relayQueryError,
          sourceConnectionsQueryError,
        ).signature;
        setMessage("Auto-deploy resumed.");
      }
      await invalidateStatus(variables.appId);
    },
    onError: (error, variables) => requireManualReload(
      variables.appId,
      error instanceof Error ? error : new Error("Auto-deploy could not be resumed."),
    ),
    onSettled: (_, __, variables) => {
      setAppPending(variables.appId, false);
    },
  });
  useEffect(() => {
    setMessage("");
    lastAnnouncedSignature.current = "";
    update.reset();
    resume.reset();
  }, [appId]);
  const activeUpdate = update.variables?.appId === appId;
  const activeResume = resume.variables?.appId === appId;
  const hookMutationError = activeUpdate && update.error instanceof Error
    ? update.error
    : activeResume && resume.error instanceof Error ? resume.error : null;
  const storedMutationError = reloadErrors.get(appId) ?? null;
  const mutationError = storedMutationError ?? hookMutationError;
  useEffect(() => {
    if (mutationError) errorRef.current?.focus();
  }, [mutationError]);

  const status = autoDeploy.data;
  const statusValid = isAutoDeployStatus(status) && status.applicationId === appId;
  const currentAnnouncement = autoDeploy.isLoading
    ? { signature: `${appId}:loading`, message: "Loading auto-deploy status…" }
    : statusValid && status
      ? panelAnnouncement(appId, status, relayQueryError, sourceConnectionsQueryError)
      : { signature: "", message: "" };
  useEffect(() => {
    if (!currentAnnouncement.signature) {
      lastAnnouncedSignature.current = "";
      setMessage("");
      return;
    }
    if (lastAnnouncedSignature.current === currentAnnouncement.signature) return;
    lastAnnouncedSignature.current = currentAnnouncement.signature;
    setMessage(currentAnnouncement.message);
  }, [currentAnnouncement.message, currentAnnouncement.signature]);
  const knownState = statusValid && knownAutoDeployStates.has(status.state);
  const relayValid = isRelayStatus(relay.data);
  const relayStatus = relayValid ? relay.data : undefined;
  const matchingConnection: SourceConnection | undefined = statusValid
    ? sourceConnections.data?.items.find((connection) => connection.id === status.source.connectionId)
    : undefined;
  const hasConnectedSource = matchingConnection?.provider === "github" && matchingConnection.status === "connected";
  const panelPending = pendingAppIds.has(appId);
  const reloadPending = reloadPendingAppIds.has(appId);
  const manualReloadRequired = reloadErrors.has(appId);
  const activeForThisApp = () => [...inFlight.current].some((key) => key.endsWith(`:${appId}`));
  const busyState = statusValid && (["dispatching", "deploying"].includes(status.state) || Boolean(status.activeJobId?.trim()));
  const busyReason = !busyState ? "" : status.activeJobId?.trim()
    ? "Settings cannot change while an active auto-deploy job exists."
    : "Settings cannot change while a deployment is being prepared or deployed.";
  const unknownStateReason = "The controller returned an unsupported auto-deploy status. Reload status before changing auto-deploy.";
  const enableReason = !statusValid ? "Auto-deploy status has not loaded." :
    manualReloadRequired ? "Reload status successfully before changing auto-deploy." :
    panelPending ? "Auto-deploy is being updated." :
    !knownState ? unknownStateReason :
    busyReason ? busyReason :
    status.source.type !== "github" ? "Auto-deploy requires a GitHub source." :
    !composeRuntime ? "A compose runtime is required." :
    !githubConnections ? "GitHub connections are unavailable on this controller." :
    sourceConnections.isLoading ? "Checking the GitHub connection." :
    sourceConnectionsQueryError ? "The GitHub connection could not be checked." :
    !hasConnectedSource ? "Connect the GitHub source used by this application." :
    relay.isLoading ? "Checking relay availability." :
    relayQueryError || !relayStatus ? "Relay availability could not be confirmed." :
    relayStatus.availability !== "available" ? relayStatus.availability === "initializing" ? "The relay is still initializing." : "The relay is unavailable." : "";
  const canEnable = knownState && !status.enabled && !busyState && !panelPending && !enableReason;
  const canDisable = knownState && status.enabled && !busyState && !panelPending && !manualReloadRequired;
  const resumeMeaningful = knownState && status.state === "paused" && status.pauseCode !== "approval_required";
  const sourceAccessPause = statusValid && status.pauseCode === "source_access_lost";
  const sourceAccessResumeReason = !sourceAccessPause ? "" :
    sourceConnections.isLoading ? "Checking the GitHub connection before resuming." :
    sourceConnectionsQueryError ? "The GitHub connection could not be checked before resuming." :
    !hasConnectedSource ? "Reconnect GitHub in the existing source connection flow, return here, then choose Retry connection check." :
    !status.sourceScopeActive ? "Choose Retry connection check to confirm restored repository access before resuming." : "";
  const canResume = resumeMeaningful && !panelPending && !manualReloadRequired && !sourceAccessResumeReason;
  const performUpdate = (enabled: boolean) => {
    if (!statusValid || activeForThisApp() || panelPending || (enabled ? !canEnable : !canDisable)) return;
    setAppPending(appId, true);
    update.mutate({ appId, expectedRevision: status.revision, enabled });
  };
  const performResume = () => {
    if (!statusValid || activeForThisApp() || !canResume) return;
    setAppPending(appId, true);
    resume.mutate({ appId, expectedRevision: status.revision });
  };
  const reloadStatus = async (activatedElement: HTMLButtonElement) => {
    const targetAppId = appId;
    if (reloadInFlight.current.has(targetAppId)) return;
    reloadInFlight.current.add(targetAppId);
    setReloadPendingAppIds((current) => new Set(current).add(targetAppId));
    try {
      const result = await autoDeploy.refetch();
      if (result.isSuccess && isAutoDeployStatus(result.data) && result.data.applicationId === targetAppId) {
        clearManualReload(targetAppId);
        if (currentAppId.current === targetAppId) {
          update.reset();
          resume.reset();
        }
      }
    } finally {
      reloadInFlight.current.delete(targetAppId);
      setReloadPendingAppIds((current) => {
        const next = new Set(current);
        next.delete(targetAppId);
        return next;
      });
      restoreFocusAfterRetry(targetAppId, activatedElement);
    }
  };
  const restoreFocusAfterRetry = (targetAppId: string, activatedElement: HTMLButtonElement) => {
    window.setTimeout(() => {
      if (currentAppId.current === targetAppId && !activatedElement.isConnected) headingRef.current?.focus();
    }, 0);
  };
  const retryAutoDeployStatus = async (activatedElement: HTMLButtonElement) => {
    const targetAppId = appId;
    await autoDeploy.refetch();
    restoreFocusAfterRetry(targetAppId, activatedElement);
  };
  const retryRelayStatus = async (activatedElement: HTMLButtonElement) => {
    const targetAppId = appId;
    await relay.refetch();
    restoreFocusAfterRetry(targetAppId, activatedElement);
  };
  const retrySourceConnections = async (activatedElement: HTMLButtonElement) => {
    const targetAppId = appId;
    await sourceConnections.refetch();
    restoreFocusAfterRetry(targetAppId, activatedElement);
  };
  const refreshSourceAccessRecovery = async (activatedElement: HTMLButtonElement) => {
    const targetAppId = appId;
    await Promise.allSettled([
      sourceConnections.refetch(),
      autoDeploy.refetch(),
    ]);
    restoreFocusAfterRetry(targetAppId, activatedElement);
  };

  return <section className="auto-deploy-panel" aria-labelledby="auto-deploy-heading">
    <div className="auto-deploy-heading">
      <div><h2 id="auto-deploy-heading" tabIndex={-1} ref={headingRef}>Auto-deploy</h2><p>Watch a GitHub ref and deploy supported changes automatically.</p></div>
      {statusValid && <span className={`auto-deploy-state ${status.state}`}>{statePresentation(status).label}</span>}
    </div>
    <p className="auto-deploy-live" role="status" aria-live="polite" aria-atomic="true">{message}</p>
    {autoDeploy.isLoading ? <div className="auto-deploy-loading"><div className="auto-deploy-loading-skeleton" aria-hidden="true"><span/><span/><span/></div></div> : autoDeploy.isError || !statusValid ? <div className="callout danger auto-deploy-query-error" role="alert"><strong>Could not load auto-deploy status.</strong><span>{autoDeploy.isError ? autoDeploy.error.message : "The API returned an incomplete auto-deploy status."}</span><button className="button small" type="button" onClick={(event) => void retryAutoDeployStatus(event.currentTarget)}>Retry</button></div> : <>
      <p className="auto-deploy-description">{statePresentation(status).detail}</p>
      {!status.enabled && canEnable && <p className="auto-deploy-enable-note">Enable sends a request. Rig verifies repository relay authorization and controller access before making a change; if setup is incomplete, auto-deploy stays off.</p>}
      {relayQueryError && <div className="callout warning auto-deploy-prerequisite"><strong>Relay status is unavailable.</strong><span>Auto-deploy cannot be turned on until relay availability can be confirmed. Turning it off remains available.</span><button className="button small" type="button" onClick={(event) => void retryRelayStatus(event.currentTarget)}>Retry relay check</button></div>}
      {sourceConnectionsQueryError && !sourceAccessPause && <div className="callout warning auto-deploy-prerequisite"><strong>GitHub connection status is unavailable.</strong><span>Auto-deploy cannot be turned on until the source connection can be checked. Turning it off remains available.</span><button className="button small" type="button" onClick={(event) => void retrySourceConnections(event.currentTarget)}>Retry connection check</button></div>}
      {storedMutationError && <div className="callout danger auto-deploy-mutation-error" role="alert" tabIndex={-1} ref={errorRef}><strong>Auto-deploy was not updated.</strong><span>{mutationMessage(storedMutationError)}</span><button className="button small" type="button" disabled={reloadPending} onClick={(event) => void reloadStatus(event.currentTarget)}>{reloadPending ? "Reloading…" : "Reload status"}</button></div>}
      {!knownState && <div className="callout info auto-deploy-prerequisite"><strong>Unsupported auto-deploy status.</strong><span>{unknownStateReason}</span><button className="button small" type="button" onClick={(event) => void retryAutoDeployStatus(event.currentTarget)}>Reload status</button></div>}
      <div className="auto-deploy-actions" aria-busy={panelPending}>
        {!status.enabled && <><button className="button primary" type="button" onClick={() => performUpdate(true)} disabled={!canEnable} aria-describedby={canEnable ? undefined : "auto-deploy-enable-reason"}>{update.isPending && activeUpdate ? "Enabling…" : "Enable"}</button><span id="auto-deploy-enable-reason" className="auto-deploy-disabled-reason">{canEnable ? "" : enableReason}</span></>}
        {status.enabled && <button className="button" type="button" onClick={() => performUpdate(false)} disabled={!canDisable} aria-describedby={canDisable ? undefined : "auto-deploy-disable-reason"}>{update.isPending && activeUpdate ? "Turning off…" : "Turn off"}</button>}
        {status.enabled && !canDisable && <span id="auto-deploy-disable-reason" className="auto-deploy-disabled-reason">{!knownState ? unknownStateReason : manualReloadRequired ? "Reload status successfully before changing auto-deploy." : busyReason || "Auto-deploy is being updated."}</span>}
        {status.state === "paused" && status.pauseCode !== "approval_required" && <button className="button" type="button" onClick={performResume} disabled={!canResume} aria-describedby={canResume ? undefined : "auto-deploy-resume-reason"}>{resume.isPending && activeResume ? "Resuming…" : "Resume"}</button>}
        {status.state === "paused" && status.pauseCode !== "approval_required" && !canResume && <span id="auto-deploy-resume-reason" className="auto-deploy-disabled-reason">{manualReloadRequired ? "Reload status successfully before resuming auto-deploy." : sourceAccessResumeReason || "Auto-deploy is being updated."}</span>}
        {sourceAccessPause && sourceAccessResumeReason && <button className="button small" type="button" onClick={(event) => void refreshSourceAccessRecovery(event.currentTarget)}>Retry connection check</button>}
      </div>
      <dl className="auto-deploy-diagnostics" aria-label="Auto-deploy diagnostics">
        <div><dt>GitHub source</dt><dd>{status.source.repositoryOwner && status.source.repositoryName ? `${status.source.repositoryOwner}/${status.source.repositoryName}` : "Not recorded"}</dd></div>
        <div><dt>Branch / ref</dt><dd className="mono">{status.source.trackedBranch || status.source.trackedRef || "Not recorded"}</dd></div>
        <SHA label="Latest SHA" value={status.latestResolvedSha}/><SHA label="Active SHA" value={status.activeSha}/><SHA label="Last successful SHA" value={status.lastSuccessfulDeployedSha}/><SHA label="Paused SHA" value={status.pausedSha}/>
        <div><dt>Active job</dt><dd className="mono">{status.activeJobId || "None"}</dd></div>
        <div><dt>Source scope</dt><dd>{status.sourceScopeActive ? "Active" : !status.enabled ? "Not subscribed while off" : "Not subscribed"}</dd></div>
        <div><dt>Updated</dt><dd><time dateTime={status.updatedAt}>{formatTime(status.updatedAt)}</time></dd></div>
      </dl>
    </>}
  </section>;
}
