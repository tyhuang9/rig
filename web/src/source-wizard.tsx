import { useEffect, useMemo, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  APIError,
  api,
  type AcceptDeploymentPlanRequest,
  type CreateApplicationRequest,
  type DeploymentPlanRevision,
  type GitHubDeviceAuthorization,
  type GitHubSource,
  type InspectResponse,
  type SourceConnection,
} from "./api";
import { DeploymentPlanReview } from "./deployment-plan-review";

const pageSize = 30;
type SourceKind = "local" | "github";
type WizardStage = "source" | "review" | "ready";
type ConnectionContext = { generation: number; kind: SourceKind; selectedConnectionId: string };
type InstallationAction = { connectionId: string; url: string };
type AuthorizationSession = { context: ConnectionContext; expiresAt: string; nextPollAt: string; reconcilingExpiry?: boolean };

function sameConnectionContext(left: ConnectionContext, right: ConnectionContext) {
  return left.generation === right.generation && left.kind === right.kind && left.selectedConnectionId === right.selectedConnectionId;
}

function safeMessage(error: unknown, fallback: string) {
  return error instanceof APIError || error instanceof Error ? error.message : fallback;
}

function connectionLabel(connection: SourceConnection) {
  return connection.providerLogin ? `@${connection.providerLogin} (${connection.status})` : `GitHub connection (${connection.status})`;
}

function connectionStatus(connection?: SourceConnection, pendingStatus?: SourceConnection["status"]) {
  return pendingStatus ?? connection?.status ?? "disconnected";
}

export function isDeviceAuthorizationExpired(expiresAt: string, now = Date.now()) {
  const expiration = Date.parse(expiresAt);
  return !Number.isFinite(expiration) || expiration <= now;
}

function PaginationControls({ label, page, onPageChange, hasNext, loading, statusId }: { label: string; page: number; onPageChange: (page: number) => void; hasNext: boolean; loading: boolean; statusId: string }) {
  if (page === 1 && !hasNext && !loading) return null;
  const previousUnavailable = loading || page === 1;
  const nextUnavailable = loading || !hasNext;
  const changePage = (nextPage: number, unavailable: boolean) => {
    if (!unavailable) onPageChange(nextPage);
  };
  return <nav className="wizard-pagination" aria-label={`${label} pagination`} aria-busy={loading} aria-describedby={statusId}>
    <button type="button" className="button small" aria-label={`Previous ${label} page`} aria-disabled={previousUnavailable} onClick={() => changePage(page - 1, previousUnavailable)}>Previous</button>
    <span>Page {page}</span>
    <button type="button" className="button small" aria-label={`Next ${label} page`} aria-disabled={nextUnavailable} onClick={() => changePage(page + 1, nextUnavailable)}>Next</button>
  </nav>;
}

function CollectionStatus({ id, label, page, loading, error, count }: { id: string; label: string; page: number; loading: boolean; error: unknown; count: number }) {
  const message = loading ? `Loading ${label} page ${page}.` : error ? "" : `${label} page ${page} loaded. ${count} result${count === 1 ? "" : "s"}.`;
  return <span id={id} className="sr-only" role="status" aria-live="polite" aria-atomic="true">{message}</span>;
}

export function SourceWizard({ onCancel, onCreated }: { onCancel: () => void; onCreated: (id: string) => void }) {
  const queryClient = useQueryClient();
  const [kind, setKind] = useState<SourceKind>("local");
  const [stage, setStage] = useState<WizardStage>("source");
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [localPath, setLocalPath] = useState("");
  const [selectedConnectionId, setSelectedConnectionId] = useState("");
  const [installationId, setInstallationId] = useState<number | null>(null);
  const [repositoryId, setRepositoryId] = useState<number | null>(null);
  const [branch, setBranch] = useState("");
  const [composePath, setComposePath] = useState("");
  const [installationPage, setInstallationPage] = useState(1);
  const [repositoryPage, setRepositoryPage] = useState(1);
  const [branchPage, setBranchPage] = useState(1);
  const [deviceAuthorization, setDeviceAuthorization] = useState<GitHubDeviceAuthorization | null>(null);
  const [authorizationSession, setAuthorizationSession] = useState<AuthorizationSession | null>(null);
  const [expiryRetryConnectionId, setExpiryRetryConnectionId] = useState("");
  const [installationAction, setInstallationAction] = useState<InstallationAction | null>(null);
  const [pendingStatus, setPendingStatus] = useState<SourceConnection["status"] | undefined>();
  const [sourceError, setSourceError] = useState("");
  const [inspectionError, setInspectionError] = useState("");
  const [formError, setFormError] = useState("");
  const [fieldErrors, setFieldErrors] = useState<{ name?: string; description?: string; localPath?: string }>({});
  const [inspection, setInspection] = useState<InspectResponse | null>(null);
  const [inspectedKey, setInspectedKey] = useState("");
  const [draftApplicationId, setDraftApplicationId] = useState("");
  const [acceptedRevision, setAcceptedRevision] = useState<DeploymentPlanRevision | null>(null);
  const connectionContext = useRef<ConnectionContext>({ generation: 0, kind: "local", selectedConnectionId: "" });
  const connectionSelect = useRef<HTMLSelectElement>(null);
  const resumeAuthorizationButton = useRef<HTMLButtonElement>(null);
  const expiryRetryButton = useRef<HTMLButtonElement>(null);
  const installActionLink = useRef<HTMLAnchorElement>(null);
  const authorizationFocusOrigin = useRef(false);
  const focusAfterAuthorization = useRef<"connection" | "expiryRetry" | "install" | null>(null);
  const expiryReconciliationAttempts = useRef(new Set<string>());
  const inspectionGeneration = useRef(0);
  const inspectionRequest = useRef<{ generation: number; key: string } | null>(null);
  const priorConnection = useRef<{ id: string; status: string }>({ id: "", status: "" });
  const draftApplicationIdRef = useRef("");
  const priorStage = useRef<WizardStage>("source");
  const errorSummary = useRef<HTMLDivElement>(null);
  const sourceHeading = useRef<HTMLHeadingElement>(null);
  const readyHeading = useRef<HTMLHeadingElement>(null);

  const capability = useQuery({ queryKey: ["system-status"], queryFn: api.status });
  const connections = useQuery({ queryKey: ["source-connections"], queryFn: api.sourceConnections, enabled: kind === "github" && capability.data?.capabilities.githubConnections === true });
  const selectedConnection = connections.data?.items.find((connection) => connection.id === selectedConnectionId);
  const selectedStatus = pendingStatus ?? (selectedConnection?.status === "pending" && selectedConnection.pendingExpiresAt && isDeviceAuthorizationExpired(selectedConnection.pendingExpiresAt) ? "expired" : connectionStatus(selectedConnection));
  const isConnected = selectedStatus === "connected";
  const canResumeAuthorization = selectedStatus === "pending" && selectedConnection?.status === "pending" && Boolean(selectedConnection.pendingExpiresAt) && !isDeviceAuthorizationExpired(selectedConnection.pendingExpiresAt!) && !deviceAuthorization;
  const canRetryExpiryReconciliation = expiryRetryConnectionId === selectedConnectionId && selectedStatus === "expired" && selectedConnection?.status === "pending" && !deviceAuthorization;

  const installations = useQuery({
    queryKey: ["github-installations", selectedConnectionId, installationPage, pageSize],
    queryFn: () => api.githubInstallations(selectedConnectionId, installationPage, pageSize),
    enabled: kind === "github" && isConnected && selectedConnectionId.length > 0,
  });
  const repositories = useQuery({
    queryKey: ["github-repositories", selectedConnectionId, installationId, repositoryPage, pageSize],
    queryFn: () => api.githubRepositories(selectedConnectionId, installationId!, repositoryPage, pageSize),
    enabled: kind === "github" && isConnected && selectedConnectionId.length > 0 && installationId !== null,
  });
  const branches = useQuery({
    queryKey: ["github-branches", selectedConnectionId, installationId, repositoryId, branchPage, pageSize],
    queryFn: () => api.githubBranches(selectedConnectionId, installationId!, repositoryId!, branchPage, pageSize),
    enabled: kind === "github" && isConnected && selectedConnectionId.length > 0 && installationId !== null && repositoryId !== null,
  });

  const source = useMemo<GitHubSource | null>(() => {
    if (!selectedConnectionId || installationId === null || repositoryId === null || !branch) return null;
    return { connectionId: selectedConnectionId, installationId, repositoryId, branch, ...(composePath ? { composePath } : {}) };
  }, [selectedConnectionId, installationId, repositoryId, branch, composePath]);
  const exactSourceKey = source ? JSON.stringify(source) : "";
  const exactInspection = Boolean(exactSourceKey) && inspection !== null && inspectedKey === exactSourceKey && inspection.findings.length === 0;
  const composeSelected = kind === "github" && Boolean(composePath) && exactInspection;
  const generatedCandidates = inspection?.analysis.candidates.filter((candidate) => candidate.kind === "javascript" && candidate.status !== "unsupported" && candidate.components.length > 0) ?? [];

  const clearInspection = () => {
    setInspection(null);
    setInspectedKey("");
    setInspectionError("");
  };
  const invalidateInspection = () => {
    inspectionGeneration.current += 1;
    inspectionRequest.current = null;
    clearInspection();
    setAcceptedRevision(null);
    if (!draftApplicationId) setStage("source");
  };
  const advanceConnectionContext = (nextKind: SourceKind, nextConnectionId: string) => {
    const next = { generation: connectionContext.current.generation + 1, kind: nextKind, selectedConnectionId: nextConnectionId };
    connectionContext.current = next;
    setAuthorizationSession(null);
    setExpiryRetryConnectionId("");
    setInstallationAction(null);
    authorizationFocusOrigin.current = false;
    focusAfterAuthorization.current = null;
    return next;
  };
  const markExpiryReconciliationAttempt = (connectionId: string, expiresAt: string) => expiryReconciliationAttempts.current.add(`${connectionId}:${expiresAt}`);
  const focusErrorSummary = () => window.setTimeout(() => errorSummary.current?.focus(), 0);
  const resetAfterConnection = () => {
    setInstallationId(null);
    setRepositoryId(null);
    setBranch("");
    setComposePath("");
    setInstallationPage(1);
    setRepositoryPage(1);
    setBranchPage(1);
    invalidateInspection();
  };
  const resetAfterInstallation = () => {
    setRepositoryId(null);
    setBranch("");
    setComposePath("");
    setRepositoryPage(1);
    setBranchPage(1);
    invalidateInspection();
  };
  const resetAfterRepository = () => {
    setBranch("");
    setComposePath("");
    setBranchPage(1);
    invalidateInspection();
  };
  const resetAfterBranch = () => {
    setComposePath("");
    invalidateInspection();
  };
  const changeInstallationPage = (page: number) => {
    setInstallationPage(page);
    setInstallationId(null);
    setRepositoryId(null);
    setBranch("");
    setComposePath("");
    setRepositoryPage(1);
    setBranchPage(1);
    invalidateInspection();
  };
  const changeRepositoryPage = (page: number) => {
    setRepositoryPage(page);
    setRepositoryId(null);
    setBranch("");
    setComposePath("");
    setBranchPage(1);
    invalidateInspection();
  };
  const changeBranchPage = (page: number) => {
    setBranchPage(page);
    setBranch("");
    setComposePath("");
    invalidateInspection();
  };

  useEffect(() => {
    const prior = priorConnection.current;
    const knownStatus = selectedConnection !== undefined || pendingStatus !== undefined;
    priorConnection.current = { id: selectedConnectionId, status: selectedStatus };
    if (kind === "github" && selectedConnectionId && prior.id === selectedConnectionId && prior.status === "connected" && knownStatus && selectedStatus !== "connected") {
      advanceConnectionContext(kind, selectedConnectionId);
      resetAfterConnection();
    }
  }, [kind, pendingStatus, selectedConnection, selectedConnectionId, selectedStatus]);

  useEffect(() => {
    if (!deviceAuthorization && pendingStatus && selectedConnection && selectedConnection.status !== "pending") setPendingStatus(undefined);
  }, [deviceAuthorization, pendingStatus, selectedConnection]);

  useEffect(() => {
    if (!installations.data?.items.length) return;
    if (document.activeElement === installActionLink.current) focusAfterAuthorization.current = "connection";
    setInstallationAction((current) => current?.connectionId === selectedConnectionId ? null : current);
  }, [installations.data, selectedConnectionId]);

  useEffect(() => {
    const previous = priorStage.current;
    priorStage.current = stage;
    if (previous === stage) return;
    window.setTimeout(() => {
      if (stage === "source") (formError ? errorSummary.current : sourceHeading.current)?.focus();
      if (stage === "ready") readyHeading.current?.focus();
    }, 0);
  }, [stage]);

  useEffect(() => {
    const target = focusAfterAuthorization.current;
    if (!target) return;
    const destination = target === "install" ? installActionLink.current ?? connectionSelect.current : target === "expiryRetry" ? expiryRetryButton.current ?? connectionSelect.current : connectionSelect.current;
    if (!destination) return;
    focusAfterAuthorization.current = null;
    destination.focus();
  });

  useEffect(() => {
    if (kind !== "github" || !selectedConnection || selectedConnection.status !== "pending" || !selectedConnection.pendingExpiresAt || !isDeviceAuthorizationExpired(selectedConnection.pendingExpiresAt) || authorizationSession || expiryRetryConnectionId === selectedConnection.id) return;
    const attemptKey = `${selectedConnection.id}:${selectedConnection.pendingExpiresAt}`;
    if (expiryReconciliationAttempts.current.has(attemptKey)) return;
    expiryReconciliationAttempts.current.add(attemptKey);
    setPendingStatus("expired");
    setAuthorizationSession({
      context: connectionContext.current,
      expiresAt: selectedConnection.pendingExpiresAt,
      nextPollAt: new Date().toISOString(),
      reconcilingExpiry: true,
    });
  }, [authorizationSession, expiryRetryConnectionId, kind, selectedConnection]);

  const beginConnection = useMutation({
    mutationFn: (_context: ConnectionContext) => api.startGitHubConnection(),
    onSuccess: async (authorization, operation) => {
      if (sameConnectionContext(operation, connectionContext.current)) {
        advanceConnectionContext("github", authorization.connectionId);
        setSourceError("");
        setDeviceAuthorization(authorization);
        setAuthorizationSession({
          context: connectionContext.current,
          expiresAt: authorization.expiresAt,
          nextPollAt: new Date(Date.now() + authorization.pollIntervalSeconds * 1000).toISOString(),
        });
        setInstallationAction({ connectionId: authorization.connectionId, url: authorization.installUrl });
        setPendingStatus("pending");
        setSelectedConnectionId(authorization.connectionId);
        resetAfterConnection();
      }
      await queryClient.invalidateQueries({ queryKey: ["source-connections"] });
    },
    onError: (error, operation) => {
      if (sameConnectionContext(operation, connectionContext.current)) setSourceError(safeMessage(error, "Could not start GitHub authorization."));
    },
  });
  const refreshConnection = useMutation({
    mutationFn: ({ connectionId }: { connectionId: string; context: ConnectionContext }) => api.refreshSourceConnection(connectionId),
    onSuccess: async (connection, operation) => {
      if (sameConnectionContext(operation.context, connectionContext.current)) {
        setPendingStatus(connection.status);
        setSourceError("");
      }
      await queryClient.invalidateQueries({ queryKey: ["source-connections"] });
      if (sameConnectionContext(operation.context, connectionContext.current)) setPendingStatus(undefined);
    },
    onError: (error, operation) => {
      if (sameConnectionContext(operation.context, connectionContext.current)) setSourceError(safeMessage(error, "Could not refresh this connection."));
    },
  });
  const disconnectConnection = useMutation({
    mutationFn: ({ connectionId }: { connectionId: string; context: ConnectionContext }) => api.disconnectSourceConnection(connectionId),
    onSuccess: async (_, operation) => {
      if (sameConnectionContext(operation.context, connectionContext.current)) {
        advanceConnectionContext("github", "");
        setDeviceAuthorization(null);
        setPendingStatus("disconnected");
        setSelectedConnectionId("");
        resetAfterConnection();
      }
      await queryClient.invalidateQueries({ queryKey: ["source-connections"] });
    },
    onError: (error, operation) => {
      if (sameConnectionContext(operation.context, connectionContext.current)) setSourceError(safeMessage(error, "Could not disconnect this connection."));
    },
  });
  const inspectSource = useMutation({
    mutationFn: (operation: { request: { sourcePath?: string; githubSource?: GitHubSource }; key: string; generation: number }) => api.inspect(operation.request).then((result) => ({ result, ...operation })),
    onSuccess: ({ result, request, key, generation }) => {
      const currentRequest = inspectionRequest.current;
      if (!currentRequest || generation !== inspectionGeneration.current || currentRequest.generation !== generation || currentRequest.key !== key) return;
      setInspection(result);
      setInspectedKey(key);
      setInspectionError("");
      const explicitlySelectedCompose = Boolean(request.githubSource?.composePath);
      if (!explicitlySelectedCompose && result.analysis.candidates.some((candidate) => candidate.kind === "javascript" && candidate.status !== "unsupported" && candidate.components.length > 0)) setStage("review");
    },
    onError: (error, operation) => {
      const currentRequest = inspectionRequest.current;
      if (!currentRequest || operation.generation !== inspectionGeneration.current || currentRequest.generation !== operation.generation || currentRequest.key !== operation.key) return;
      clearInspection();
      setInspectionError(safeMessage(error, "Could not inspect this source."));
    },
  });
  const runInspection = (request: { sourcePath?: string; githubSource?: GitHubSource }, key: string) => {
    const generation = inspectionGeneration.current + 1;
    inspectionGeneration.current = generation;
    inspectionRequest.current = { generation, key };
    inspectSource.mutate({ request, key, generation });
  };
  const create = useMutation({
    mutationFn: api.createApp,
    onSuccess: async (application) => {
      await queryClient.invalidateQueries({ queryKey: ["apps"] });
      onCreated(application.id);
    },
    onError: (error) => {
      setFormError(safeMessage(error, "Could not save the application."));
      focusErrorSummary();
    },
  });
  const acceptPlan = useMutation({
    mutationFn: async (plan: AcceptDeploymentPlanRequest) => {
      let applicationId = draftApplicationIdRef.current;
      if (!applicationId) {
        const application = await api.createApp(createRequest());
        applicationId = application.id;
        draftApplicationIdRef.current = applicationId;
        setDraftApplicationId(applicationId);
        await queryClient.invalidateQueries({ queryKey: ["apps"] });
      }
      const revision = await api.acceptDeploymentPlan(applicationId, plan);
      return { applicationId, revision };
    },
    onSuccess: async ({ applicationId, revision }) => {
      setAcceptedRevision(revision);
      setStage("ready");
      await queryClient.invalidateQueries({ queryKey: ["apps"] });
      await queryClient.invalidateQueries({ queryKey: ["deployment-plan", applicationId] });
    },
    onError: async (error) => {
      const applicationId = draftApplicationIdRef.current;
      if (applicationId) await queryClient.invalidateQueries({ queryKey: ["apps"] });
      if (error instanceof APIError && error.code === "deployment_plan_review_required") {
        setFormError("The project setup changed while you were reviewing it. Review the updated setup before trying again.");
      } else if (error instanceof APIError && error.code === "deployment_plan_conflict") {
        if (applicationId) {
          try {
            const revision = await api.deploymentPlan(applicationId);
            setAcceptedRevision(revision);
            setFormError("");
            setStage("ready");
            await queryClient.invalidateQueries({ queryKey: ["deployment-plan", applicationId] });
            return;
          } catch (reloadError) {
            setFormError(safeMessage(reloadError, "This setup was updated in another session, but Rig could not load the accepted revision."));
          }
        } else {
          setFormError("This setup was updated in another session, but the saved application draft is unavailable.");
        }
      } else {
        setFormError(safeMessage(error, "Could not accept the deployment setup."));
      }
      focusErrorSummary();
    },
  });
  const approveMigration = useMutation({
    mutationFn: async () => {
      if (!draftApplicationId || !acceptedRevision?.revisionId) throw new Error("The accepted setup is unavailable.");
      return api.approveDeploymentPlanMigration(draftApplicationId, {
        revisionId: acceptedRevision.revisionId,
        revisionNumber: acceptedRevision.revisionNumber,
        expectedApprovalRevision: 0,
      });
    },
    onSuccess: (revision) => {
      setFormError("");
      setAcceptedRevision(revision);
      window.setTimeout(() => readyHeading.current?.focus(), 0);
    },
    onError: (error) => {
      setFormError(safeMessage(error, "Could not approve the database migration."));
      focusErrorSummary();
    },
  });

  useEffect(() => {
    if (!authorizationSession || !sameConnectionContext(authorizationSession.context, connectionContext.current)) return;
    const { context, expiresAt, nextPollAt, reconcilingExpiry = false } = authorizationSession;
    const expiration = Date.parse(expiresAt);
    const nextPoll = Date.parse(nextPollAt);
    const queueFocusAfterAuthorization = (target: "connection" | "expiryRetry" | "install") => {
      if (authorizationFocusOrigin.current && (document.activeElement === resumeAuthorizationButton.current || document.activeElement === expiryRetryButton.current)) focusAfterAuthorization.current = target;
      authorizationFocusOrigin.current = false;
    };
    const finish = (status: SourceConnection["status"], message: string) => {
      if (!sameConnectionContext(context, connectionContext.current)) return;
      queueFocusAfterAuthorization("connection");
      setPendingStatus(status);
      setAuthorizationSession(null);
      setExpiryRetryConnectionId("");
      setDeviceAuthorization(null);
      setSourceError(message);
      void queryClient.invalidateQueries({ queryKey: ["source-connections"] });
    };
    const pauseExpiredReconciliation = (error: unknown) => {
      if (!sameConnectionContext(context, connectionContext.current)) return;
      queueFocusAfterAuthorization("expiryRetry");
      setAuthorizationSession(null);
      setDeviceAuthorization(null);
      setPendingStatus("expired");
      setExpiryRetryConnectionId(context.selectedConnectionId);
      setSourceError(`${safeMessage(error, "Could not confirm the expired GitHub authorization.")} Select Retry authorization status to try again.`);
      void queryClient.invalidateQueries({ queryKey: ["source-connections"] });
    };
    if ((!Number.isFinite(expiration) || expiration <= Date.now()) && !reconcilingExpiry) {
      markExpiryReconciliationAttempt(context.selectedConnectionId, expiresAt);
      queueFocusAfterAuthorization("connection");
      setPendingStatus("expired");
      setDeviceAuthorization(null);
      setAuthorizationSession({ ...authorizationSession, nextPollAt: new Date().toISOString(), reconcilingExpiry: true });
      return;
    }

    let cancelled = false;
    let pollInFlight = false;
    const delay = reconcilingExpiry ? 0 : Math.max(0, Math.min(Number.isFinite(nextPoll) ? nextPoll : Date.now(), expiration) - Date.now());
    const timer = window.setTimeout(async () => {
      if (cancelled || pollInFlight || !sameConnectionContext(context, connectionContext.current)) return;
      if ((!Number.isFinite(expiration) || Date.now() >= expiration) && !reconcilingExpiry) {
        markExpiryReconciliationAttempt(context.selectedConnectionId, expiresAt);
        queueFocusAfterAuthorization("connection");
        setPendingStatus("expired");
        setDeviceAuthorization(null);
        setAuthorizationSession({ ...authorizationSession, nextPollAt: new Date().toISOString(), reconcilingExpiry: true });
        return;
      }
      pollInFlight = true;
      try {
        const connection = await api.pollGitHubConnection(context.selectedConnectionId);
        if (cancelled || !sameConnectionContext(context, connectionContext.current)) return;
        if (connection.status !== "pending") queueFocusAfterAuthorization(connection.status === "connected" && Boolean(connection.installUrl) ? "install" : "connection");
        setPendingStatus(connection.status);
        if (connection.status !== "pending") setExpiryRetryConnectionId("");
        if (connection.installUrl) setInstallationAction({ connectionId: connection.id, url: connection.installUrl });
        await queryClient.invalidateQueries({ queryKey: ["source-connections"] });
        if (cancelled || !sameConnectionContext(context, connectionContext.current)) return;
        if (connection.status === "pending") {
          const pendingExpiration = connection.pendingExpiresAt ?? expiresAt;
          if (isDeviceAuthorizationExpired(pendingExpiration)) {
            if (reconcilingExpiry) pauseExpiredReconciliation(new Error("The controller still reports this authorization as pending."));
            else {
              markExpiryReconciliationAttempt(context.selectedConnectionId, pendingExpiration);
              setDeviceAuthorization(null);
              setPendingStatus("expired");
              setAuthorizationSession({ context, expiresAt: pendingExpiration, nextPollAt: new Date().toISOString(), reconcilingExpiry: true });
            }
          } else {
            setAuthorizationSession({
              context,
              expiresAt: pendingExpiration,
              nextPollAt: connection.nextPollAt ?? new Date(Date.now() + 1000).toISOString(),
            });
          }
        } else {
          setAuthorizationSession(null);
          setDeviceAuthorization(null);
          setSourceError("");
        }
      } catch (error) {
        if (cancelled || !sameConnectionContext(context, connectionContext.current)) return;
        if (error instanceof APIError && error.status === 429 && error.retryAfterSeconds) {
          if (reconcilingExpiry || !Number.isFinite(expiration) || Date.now() >= expiration) {
            pauseExpiredReconciliation(error);
            return;
          }
          setAuthorizationSession({
            context,
            expiresAt,
            nextPollAt: new Date(Math.min(Date.now() + error.retryAfterSeconds * 1000, expiration)).toISOString(),
          });
          return;
        }
        if (error instanceof APIError && error.code === "authorization_denied") {
          finish("denied", error.detail);
          return;
        }
        if (error instanceof APIError && error.code === "authorization_expired") {
          finish("expired", error.detail);
          return;
        }
        if (error instanceof APIError && error.code === "identity_already_connected") {
          finish("access_lost", error.detail);
          return;
        }
        if (error instanceof APIError && (error.status === 404 || error.code === "invalid_connection_state" || error.code === "github_connections_disabled")) {
          finish("access_lost", error.detail);
          return;
        }
        if (reconcilingExpiry || !Number.isFinite(expiration) || Date.now() >= expiration) {
          pauseExpiredReconciliation(error);
          return;
        }
        setAuthorizationSession(null);
        setDeviceAuthorization(null);
        setPendingStatus("pending");
        setSourceError(`${safeMessage(error, "Could not check GitHub authorization.")} Select Resume authorization check to try again.`);
        void queryClient.invalidateQueries({ queryKey: ["source-connections"] });
      } finally {
        pollInFlight = false;
      }
    }, delay);
    return () => {
      cancelled = true;
      window.clearTimeout(timer);
    };
  }, [authorizationSession, queryClient]);

  const resumeAuthorization = () => {
    if (!canResumeAuthorization || !selectedConnection?.pendingExpiresAt) {
      setSourceError("Authorization timing is unavailable. Start a new connection.");
      return;
    }
    const context = advanceConnectionContext("github", selectedConnection.id);
    authorizationFocusOrigin.current = document.activeElement === resumeAuthorizationButton.current;
    setDeviceAuthorization(null);
    setPendingStatus("pending");
    setSourceError("");
    if (selectedConnection.installUrl) setInstallationAction({ connectionId: selectedConnection.id, url: selectedConnection.installUrl });
    setAuthorizationSession({
      context,
      expiresAt: selectedConnection.pendingExpiresAt,
      nextPollAt: selectedConnection.nextPollAt ?? new Date().toISOString(),
    });
  };

  const retryExpiryReconciliation = () => {
    if (!canRetryExpiryReconciliation || !selectedConnection?.pendingExpiresAt || authorizationSession) return;
    authorizationFocusOrigin.current = document.activeElement === expiryRetryButton.current;
    markExpiryReconciliationAttempt(selectedConnection.id, selectedConnection.pendingExpiresAt);
    setSourceError("");
    setAuthorizationSession({
      context: connectionContext.current,
      expiresAt: selectedConnection.pendingExpiresAt,
      nextPollAt: new Date().toISOString(),
      reconcilingExpiry: true,
    });
  };

  const validateApplicationDetails = () => {
    setFormError("");
    const errors: { name?: string; description?: string; localPath?: string } = {};
    if (!name.trim()) errors.name = "Enter an application name.";
    else if (name.trim().length > 100) errors.name = "Application name must be 100 characters or fewer.";
    if (description.length > 300) errors.description = "Description must be 300 characters or fewer.";
    if (kind === "local" && !localPath.trim()) errors.localPath = "Enter a local source path.";
    setFieldErrors(errors);
    if (Object.keys(errors).length > 0) {
      setFormError("Check the highlighted fields.");
      focusErrorSummary();
      return false;
    }
    return true;
  };

  const createRequest = (): CreateApplicationRequest => {
    if (kind === "local") {
      return { name: name.trim(), description, sourcePath: localPath.trim() };
    }
    if (!source) throw new Error("Complete the GitHub source selection before continuing.");
    return { name: name.trim(), description, githubSource: source };
  };

  const saveCompose = () => {
    if (!validateApplicationDetails()) return;
    if (kind === "github" && (!source?.composePath || !exactInspection)) {
      setFormError("Choose and inspect a Compose file before saving.");
      focusErrorSummary();
      return;
    }
    create.mutate(createRequest());
  };

  const acceptGeneratedPlan = (request: AcceptDeploymentPlanRequest) => {
    if (!validateApplicationDetails()) {
      setStage("source");
      return;
    }
    setFormError("");
    acceptPlan.mutate(request);
  };

  const githubEnabled = capability.data?.capabilities.githubConnections === true;
  const sourceIsBusy = beginConnection.isPending || refreshConnection.isPending || disconnectConnection.isPending;
  const resumeUnavailable = sourceIsBusy || authorizationSession !== null;
  const installUrl = installationAction?.connectionId === selectedConnectionId ? installationAction.url : "";
  const githubSaveHelp = !githubEnabled
    ? "GitHub connections must be enabled before this application can be saved."
    : deviceAuthorization || authorizationSession
      ? "Finish GitHub device authorization before choosing the application source."
      : !isConnected
        ? "Choose a connected GitHub account before saving."
        : installUrl
          ? "Install or configure repository access, then choose the GitHub App installation before saving."
        : installationId === null
          ? "Choose a GitHub App installation before saving."
          : repositoryId === null
            ? "Choose a repository before saving."
            : !branch
              ? "Choose a tracked branch before saving."
              : composePath
                ? !exactInspection
                  ? inspection?.findings.length
                    ? "Resolve the source findings, then inspect the exact source again before saving."
                    : "Inspect the selected Compose file before saving."
                  : "The exact source inspection is clean. This application is ready to save."
                : !inspection
                  ? "Analyze the selected repository before continuing."
                  : generatedCandidates.length > 0
                    ? "Review how Rig will build and run the detected project."
                    : inspection.composeCandidates.length
                      ? "Choose a Compose file, then inspect the exact source before saving."
                      : "Add a supported JavaScript project or a Compose file, then analyze again.";
  const analyzeCurrentSource = () => {
    setFormError("");
    if (kind === "local") {
      if (!localPath.trim()) {
        setFieldErrors((current) => ({ ...current, localPath: "Enter a local source path." }));
        setFormError("Check the highlighted fields.");
        focusErrorSummary();
        return;
      }
      runInspection({ sourcePath: localPath.trim() }, `local:${localPath.trim()}`);
      return;
    }
    if (!source) {
      setFormError("Choose a connected GitHub repository and tracked branch before analysis.");
      focusErrorSummary();
      return;
    }
    runInspection({ githubSource: source }, JSON.stringify(source));
  };

  if (stage === "review" && inspection) return <div className="wizard source-wizard">
    <ol aria-label="Setup progress"><li>Source</li><li aria-current="step">How Rig will run it</li><li>Ready to deploy</li></ol>
    <form onSubmit={(event) => event.preventDefault()} noValidate>
      <DeploymentPlanReview
        inspection={inspection}
        expectedRevisionNumber={acceptedRevision?.revisionNumber ?? 0}
        pending={acceptPlan.isPending}
        error={formError}
        onBack={() => { setFormError(""); setStage("source"); }}
        onRefresh={analyzeCurrentSource}
        onAccept={acceptGeneratedPlan}
        onUseCompose={inspection.composeCandidates.length > 0 && !draftApplicationId ? () => { setFormError(""); setStage("source"); } : undefined}
        draftSaved={Boolean(draftApplicationId)}
        onOpenSavedDraft={draftApplicationId ? () => onCreated(draftApplicationId) : undefined}
      />
    </form>
  </div>;

  if (stage === "ready" && acceptedRevision && draftApplicationId) {
    const migrationPending = acceptedRevision.migration.present && acceptedRevision.migration.approvalStatus !== "approved";
    const readyAnnouncement = approveMigration.isPending
      ? "Approving database migration."
      : acceptedRevision.migration.present
        ? migrationPending
          ? "Deployment setup accepted. Database migration approval is required before deployment."
          : "Database migration approved. Deployment setup is ready."
        : "Deployment setup accepted. The application is ready to open.";
    return <div className="wizard source-wizard">
      <ol aria-label="Setup progress"><li>Source</li><li>How Rig will run it</li><li aria-current="step">Ready to deploy</li></ol>
      <form onSubmit={(event) => event.preventDefault()} noValidate>
        <section className="plan-ready" aria-labelledby="plan-ready-title">
          <h2 id="plan-ready-title" ref={readyHeading} tabIndex={-1}>Setup accepted</h2>
          <span className="sr-only" role="status" aria-live="polite" aria-atomic="true">{readyAnnouncement}</span>
          <p>Rig saved this immutable setup as revision {acceptedRevision.revisionNumber}. It will reuse it until relevant project metadata changes.</p>
          {formError && <div ref={errorSummary} className="error-summary" role="alert" tabIndex={-1}>{formError}</div>}
          {acceptedRevision.migration.present && <div className={migrationPending ? "migration-review" : "callout success"}>
            <strong>{migrationPending ? "Database migration needs separate approval" : "Database migration approved"}</strong>
            {migrationPending ? <><p>This command can change persistent data. The old and new app versions briefly share the migrated database, and Rig will not automatically roll it back.</p><button className="button" type="button" disabled={approveMigration.isPending} onClick={() => { setFormError(""); approveMigration.mutate(); }}>{approveMigration.isPending ? "Approving migration…" : "Approve migration before the next deployment"}</button></> : <span>The migration is approved for this plan revision.</span>}
          </div>}
          <div className="callout success"><strong>Ready to deploy</strong><span>Analysis did not execute repository code. Open the application and choose Deploy latest to build and start this accepted setup in containers.</span></div>
          <footer><button className="button primary" type="button" onClick={() => onCreated(draftApplicationId)}>Open application</button></footer>
        </section>
      </form>
    </div>;
  }

  return <div className="wizard source-wizard">
    <ol aria-label="Setup progress"><li aria-current="step">Source</li><li>How Rig will run it</li><li>Ready to deploy</li></ol>
    <form onSubmit={(event) => { event.preventDefault(); composeSelected ? saveCompose() : generatedCandidates.length > 0 ? setStage("review") : saveCompose(); }} noValidate>
      <h2 ref={sourceHeading} tabIndex={-1}>Application source</h2>
      <p>Choose a project for Rig to analyze. Rig reads project files to suggest setup; it won’t run your code.</p>
      {formError && <div ref={errorSummary} className="error-summary" role="alert" tabIndex={-1}>{formError}</div>}
      <div className="field">
        <label htmlFor="wizard-name">Application name <span aria-hidden="true">*</span></label>
        <input id="wizard-name" required value={name} aria-invalid={Boolean(fieldErrors.name)} aria-describedby={fieldErrors.name ? "wizard-name-error" : undefined} onChange={(event) => { setName(event.target.value); setFieldErrors((current) => ({ ...current, name: undefined })); setFormError(""); }} />
        {fieldErrors.name && <p id="wizard-name-error" className="form-error">{fieldErrors.name}</p>}
      </div>
      <div className="field">
        <label htmlFor="wizard-description">Description</label>
        <input id="wizard-description" value={description} aria-invalid={Boolean(fieldErrors.description)} aria-describedby={fieldErrors.description ? "wizard-description-error" : undefined} onChange={(event) => { setDescription(event.target.value); setFieldErrors((current) => ({ ...current, description: undefined })); setFormError(""); }} />
        {fieldErrors.description && <p id="wizard-description-error" className="form-error">{fieldErrors.description}</p>}
      </div>

      <fieldset className="source-choice">
        <legend>Source type</legend>
        <label><input type="radio" name="source-kind" checked={kind === "local"} onChange={() => { advanceConnectionContext("local", selectedConnectionId); setKind("local"); setDeviceAuthorization(null); setPendingStatus(undefined); setFormError(""); setSourceError(""); invalidateInspection(); }} /> Local folder</label>
        <label><input type="radio" name="source-kind" checked={kind === "github"} onChange={() => { advanceConnectionContext("github", selectedConnectionId); setKind("github"); setFormError(""); setSourceError(""); setFieldErrors((current) => ({ ...current, localPath: undefined })); invalidateInspection(); }} /> GitHub repository</label>
      </fieldset>

      {kind === "local" ? <section className="source-panel" aria-labelledby="local-source-title">
        <h3 id="local-source-title">Local folder</h3>
        <div className="field">
          <label htmlFor="wizard-source-path">Local source path <span aria-hidden="true">*</span></label>
          <input id="wizard-source-path" required placeholder="C:\projects\my-app" value={localPath} aria-invalid={Boolean(fieldErrors.localPath)} aria-describedby={fieldErrors.localPath ? "wizard-source-path-error" : undefined} onChange={(event) => { setLocalPath(event.target.value); setFieldErrors((current) => ({ ...current, localPath: undefined })); setFormError(""); invalidateInspection(); }} />
          {fieldErrors.localPath && <p id="wizard-source-path-error" className="form-error">{fieldErrors.localPath}</p>}
        </div>
        <button type="button" className="button primary" disabled={!localPath.trim() || inspectSource.isPending} onClick={analyzeCurrentSource}>{inspectSource.isPending ? "Analyzing…" : "Analyze project"}</button>
        {inspectionError && <div className="callout danger" role="alert">{inspectionError}</div>}
        <InspectionSummary inspection={inspection} />
      </section> : <section className="source-panel" aria-labelledby="github-source-title">
        <h3 id="github-source-title">GitHub repository</h3>
        <span className="sr-only capability-status" role="status" aria-live="polite" aria-atomic="true">{capability.isFetching ? "Checking GitHub connection capability." : capability.isError ? "GitHub connection capability check failed." : githubEnabled ? "GitHub connections are available." : "GitHub connections are disabled."}</span>
        {capability.isLoading ? <div className="callout info">Checking GitHub connection capability…</div> : capability.isError ? <div className="callout danger"><strong>Could not check GitHub capability</strong><span>{safeMessage(capability.error, "The controller status could not be loaded.")}</span><button type="button" className="button small" onClick={() => void capability.refetch()}>Retry capability check</button></div> : !githubEnabled ? <div className="callout warning"><strong>GitHub connections are disabled</strong><span>The administrator disabled GitHub connections on this controller.</span></div> : <>
          <div className="connection-actions">
            <button type="button" className="button" disabled={sourceIsBusy || authorizationSession !== null} onClick={() => beginConnection.mutate(connectionContext.current)}>{beginConnection.isPending ? "Starting…" : "Sign in to GitHub"}</button>
            {canResumeAuthorization && <button ref={resumeAuthorizationButton} type="button" className="button primary" aria-disabled={resumeUnavailable} onClick={() => { if (!resumeUnavailable) resumeAuthorization(); }}>{authorizationSession ? "Checking authorization…" : "Resume authorization check"}</button>}
            {canRetryExpiryReconciliation && <button ref={expiryRetryButton} type="button" className="button primary" aria-disabled={resumeUnavailable} onClick={() => { if (!resumeUnavailable) retryExpiryReconciliation(); }}>{authorizationSession?.reconcilingExpiry ? "Checking authorization status…" : "Retry authorization status"}</button>}
            {selectedConnectionId && selectedStatus === "connected" && <button type="button" className="button" disabled={sourceIsBusy} onClick={() => refreshConnection.mutate({ connectionId: selectedConnectionId, context: connectionContext.current })}>{refreshConnection.isPending ? "Refreshing…" : "Refresh connection"}</button>}
            {selectedConnectionId && <button type="button" className="button" disabled={sourceIsBusy} onClick={() => disconnectConnection.mutate({ connectionId: selectedConnectionId, context: connectionContext.current })}>{disconnectConnection.isPending ? "Disconnecting…" : "Disconnect"}</button>}
          </div>
          <div className={deviceAuthorization ? "callout info device-authorization connection-status" : sourceError ? "callout danger connection-status" : selectedConnectionId && !isConnected ? "callout warning connection-status" : "wizard-status connection-status"} role="status" aria-live="polite" aria-atomic="true">
            {deviceAuthorization ? <>
              <strong>Step 1 of 2: Sign in to GitHub</strong>
              <span>Enter code <code>{deviceAuthorization.userCode}</code> at GitHub to sign in and authorize Rig.</span>
              <span>Use an account that can manage GitHub App access for the personal account or organization that owns the repository.</span>
              <span><a className="button primary" href={deviceAuthorization.verificationUri} target="_blank" rel="noreferrer">Sign in to GitHub (opens in a new tab)</a></span>
            </> : isConnected && installUrl ? <>
              <strong>Step 1 complete: Signed in to GitHub</strong>
              <span>Step 2 of 2: Install or configure repository access.</span>
              <span>Choose the personal account or organization that owns the repository, then grant Rig access to the repositories you want to deploy.</span>
              <span><a ref={installActionLink} className="button primary" href={installUrl} target="_blank" rel="noreferrer">Install or configure repository access (opens in a new tab)</a></span>
            </> : selectedConnectionId ? <>
              <strong>{isConnected ? "GitHub connection ready" : selectedStatus === "pending" ? "GitHub authorization pending" : "GitHub connection needs attention"}</strong>
              <span>{`Connection status: ${selectedStatus.replaceAll("_", " ")}.`}</span>
              {sourceError && <span>{sourceError}</span>}
              {!sourceError && selectedStatus === "pending" && <span>{authorizationSession ? "Rig is checking this authorization. Keep this page open." : canResumeAuthorization ? "Resume authorization check is now available. Select it to continue checking the authorization started earlier." : "This authorization can no longer be resumed. Start a new connection."}</span>}
              {!sourceError && selectedStatus === "expired" && authorizationSession?.reconcilingExpiry && <span>Rig is confirming the expired authorization with the controller.</span>}
              {!sourceError && selectedStatus === "access_lost" && <span>GitHub access was lost. Start a new connection before browsing repositories.</span>}
              {!sourceError && !isConnected && selectedStatus !== "access_lost" && selectedStatus !== "pending" && !authorizationSession?.reconcilingExpiry && <span>Start a new connection or choose another connection.</span>}
            </> : sourceError ? <><strong>GitHub connection failed</strong><span>{sourceError}</span></> : connections.isFetching ? <span>Loading GitHub connections.</span> : connections.isError ? <span>GitHub connections could not be loaded.</span> : <span>{`GitHub connections loaded. ${connections.data?.items.length ? "Choose an existing connection or start a new one." : "Start a new connection."}`}</span>}
          </div>
          <div className="field">
            <label htmlFor="github-connection">GitHub connection</label>
            <select ref={connectionSelect} id="github-connection" value={selectedConnectionId} disabled={connections.isFetching || connections.isError} onChange={(event) => { advanceConnectionContext("github", event.target.value); setSelectedConnectionId(event.target.value); setPendingStatus(undefined); setDeviceAuthorization(null); setSourceError(""); resetAfterConnection(); }}>
              <option value="">{connections.isFetching ? "Loading connections…" : connections.isError ? "Connections unavailable" : "Choose a connection"}</option>
              {connections.data?.items.map((connection) => <option key={connection.id} value={connection.id}>{connectionLabel(connection)}</option>)}
            </select>
          </div>
          {connections.isError && <div className="callout danger"><strong>Could not load GitHub connections</strong><span>{safeMessage(connections.error, "The connection list could not be loaded.")}</span><button type="button" className="button small" onClick={() => void connections.refetch()}>Retry connections</button></div>}
          {!connections.isFetching && !connections.isError && connections.data?.items.length === 0 && <div className="callout info"><strong>No GitHub accounts connected</strong><span>Select Sign in to GitHub to authorize Rig.</span></div>}
          {isConnected && <div className="source-selects">
            <SourceSelect label="GitHub App installation" collectionLabel="GitHub App installations" page={installationPage} id="github-installation" value={installationId?.toString() ?? ""} onChange={(value) => { setInstallationId(value ? Number(value) : null); resetAfterInstallation(); }} loading={installations.isFetching} error={installations.error} disabled={installations.isFetching} placeholder="Choose an installation" emptyTitle="No GitHub App installations found" emptyMessage="No GitHub App installations are available. Sign in to GitHub again to install or configure repository access, then retry." onRetry={() => void installations.refetch()} items={installations.data?.items.map((item) => ({ value: String(item.id), label: `${item.accountLogin} (${item.repositorySelection} repositories)` })) ?? []} />
            <PaginationControls label="GitHub App installations" page={installationPage} onPageChange={changeInstallationPage} hasNext={(installations.data?.page ?? 0) * (installations.data?.perPage ?? pageSize) < (installations.data?.totalCount ?? 0)} loading={installations.isFetching} statusId="github-installation-status" />
            {installationId !== null && <><SourceSelect label="Repository" collectionLabel="Repositories" page={repositoryPage} id="github-repository" value={repositoryId?.toString() ?? ""} onChange={(value) => { setRepositoryId(value ? Number(value) : null); resetAfterRepository(); }} loading={repositories.isFetching} error={repositories.error} disabled={repositories.isFetching} placeholder="Choose a repository" emptyTitle="No repositories found" emptyMessage="No accessible repositories are available. Update the GitHub App repository access, then retry." onRetry={() => void repositories.refetch()} items={repositories.data?.items.filter((item) => !item.archived && !item.disabled).map((item) => ({ value: String(item.id), label: `${item.owner}/${item.name}${item.private ? " (private)" : ""}` })) ?? []} />
            <PaginationControls label="repositories" page={repositoryPage} onPageChange={changeRepositoryPage} hasNext={(repositories.data?.page ?? 0) * (repositories.data?.perPage ?? pageSize) < (repositories.data?.totalCount ?? 0)} loading={repositories.isFetching} statusId="github-repository-status" /></>}
            {repositoryId !== null && <><SourceSelect label="Tracked branch" collectionLabel="Branches" page={branchPage} id="github-branch" value={branch} onChange={(value) => { setBranch(value); resetAfterBranch(); }} loading={branches.isFetching} error={branches.error} disabled={branches.isFetching} placeholder="Choose a branch" emptyTitle="No branches found" emptyMessage="No branches are available. Push a tracked branch or choose another repository, then retry." onRetry={() => void branches.refetch()} items={branches.data?.items.map((item) => ({ value: item.name, label: item.protected ? `${item.name} (protected)` : item.name })) ?? []} />
            <PaginationControls label="branches" page={branchPage} onPageChange={changeBranchPage} hasNext={(branches.data?.items.length ?? 0) === (branches.data?.perPage ?? pageSize)} loading={branches.isFetching} statusId="github-branch-status" /></>}
          </div>}
          {source && !composePath && <button type="button" className="button primary" disabled={inspectSource.isPending} onClick={analyzeCurrentSource}>{inspectSource.isPending ? "Analyzing…" : "Analyze project"}</button>}
          {inspectionError && <div className="callout danger" role="alert">{inspectionError}</div>}
          {inspection && source && !composePath && inspection.composeCandidates.length > 0 && <div className="field"><label htmlFor="github-compose-path">Compose file</label><select id="github-compose-path" value={composePath} onChange={(event) => { setComposePath(event.target.value); invalidateInspection(); }}><option value="">Choose a Compose file</option>{inspection.composeCandidates.map((candidate) => <option key={candidate} value={candidate}>{candidate}</option>)}</select></div>}
          {source?.composePath && <button type="button" className="button" disabled={inspectSource.isPending} onClick={() => runInspection({ githubSource: source }, JSON.stringify(source))}>{inspectSource.isPending ? "Inspecting…" : "Inspect selected Compose file"}</button>}
          <InspectionSummary inspection={inspection} discovery={Boolean(source && !composePath)} />
        </>}
      </section>}
      {kind === "github" && <p id="github-save-help" className="save-help">{githubSaveHelp}</p>}
      <footer><button className="button" type="button" onClick={onCancel}>Back</button>{composeSelected ? <button className="button primary" aria-describedby="github-save-help" disabled={create.isPending}>{create.isPending ? "Saving…" : "Save application"}</button> : generatedCandidates.length > 0 ? <button className="button primary" type="button" onClick={() => setStage("review")}>Review setup</button> : inspection?.composeCandidates.length ? <button className="button primary" aria-describedby={kind === "github" ? "github-save-help" : undefined} disabled={create.isPending || (kind === "github" && (!composePath || !exactInspection))}>{create.isPending ? "Saving…" : "Save application"}</button> : <button className="button primary" type="button" disabled aria-describedby={kind === "github" ? "github-save-help" : undefined}>Save application</button>}</footer>
    </form>
  </div>;
}

function SourceSelect({ label, collectionLabel, page, id, value, onChange, loading, error, disabled, placeholder, emptyTitle, emptyMessage, onRetry, items }: { label: string; collectionLabel: string; page: number; id: string; value: string; onChange: (value: string) => void; loading: boolean; error: unknown; disabled: boolean; placeholder: string; emptyTitle: string; emptyMessage: string; onRetry: () => void; items: Array<{ value: string; label: string }> }) {
  return <div className="field" aria-busy={loading}>
    <label htmlFor={id}>{label}</label>
    <CollectionStatus id={`${id}-status`} label={collectionLabel} page={page} loading={loading} error={error} count={items.length} />
    <select id={id} value={value} disabled={disabled || Boolean(error) || (!loading && !error && items.length === 0)} onChange={(event) => onChange(event.target.value)}><option value="">{loading ? `Loading ${label.toLowerCase()}…` : error ? `${label} unavailable` : placeholder}</option>{items.map((item) => <option key={item.value} value={item.value}>{item.label}</option>)}</select>
    {Boolean(error) && <div className="callout danger" role="alert"><strong>{`Could not load ${label.toLowerCase()}`}</strong><span>{safeMessage(error, `The ${label.toLowerCase()} list could not be loaded.`)}</span><button type="button" className="button small" onClick={onRetry}>{`Retry ${label.toLowerCase()}`}</button></div>}
    {!loading && !error && items.length === 0 && <div className="callout info"><strong>{emptyTitle}</strong><span>{emptyMessage}</span><button type="button" className="button small" onClick={onRetry}>{`Retry ${label.toLowerCase()}`}</button></div>}
  </div>;
}

function InspectionSummary({ inspection, discovery = false }: { inspection: InspectResponse | null; discovery?: boolean }) {
  if (!inspection) return null;
  if (inspection.findings.length > 0) return <div className="callout warning" role="status" aria-live="polite" aria-atomic="true"><strong>Source requires changes before it can be saved</strong>{inspection.findings.map((finding) => <span key={`${finding.code}:${finding.path ?? ""}`}>{finding.message}</span>)}</div>;
  if (discovery && inspection.composeCandidates.length === 0) return <div className="callout warning" role="status" aria-live="polite" aria-atomic="true"><strong>No Compose files found</strong><span>Add a Compose file to the tracked branch, then inspect again.</span></div>;
  return <div className="callout success" role="status" aria-live="polite" aria-atomic="true"><strong>Source inspection completed</strong><span>{inspection.resolvedSha ? `Resolved ${inspection.resolvedSha.slice(0, 12)}. ` : ""}Found {inspection.services.length} service{inspection.services.length === 1 ? "" : "s"}.</span></div>;
}
