import { useEffect, useMemo, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  APIError,
  api,
  type CreateApplicationRequest,
  type GitHubDeviceAuthorization,
  type GitHubSource,
  type InspectResponse,
  type SourceConnection,
} from "./api";

const pageSize = 30;
type SourceKind = "local" | "github";

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

function PaginationControls({ label, page, onPageChange, hasNext, hasResults }: { label: string; page: number; onPageChange: (page: number) => void; hasNext: boolean; hasResults: boolean }) {
  if (!hasResults || (page === 1 && !hasNext)) return null;
  return <nav className="wizard-pagination" aria-label={`${label} pagination`}>
    <button type="button" className="button small" aria-label={`Previous ${label} page`} disabled={page === 1} onClick={() => onPageChange(page - 1)}>Previous</button>
    <span>Page {page}</span>
    <button type="button" className="button small" aria-label={`Next ${label} page`} disabled={!hasNext} onClick={() => onPageChange(page + 1)}>Next</button>
  </nav>;
}

export function SourceWizard({ onCancel, onCreated }: { onCancel: () => void; onCreated: (id: string) => void }) {
  const queryClient = useQueryClient();
  const [kind, setKind] = useState<SourceKind>("local");
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
  const [pendingStatus, setPendingStatus] = useState<SourceConnection["status"] | undefined>();
  const [sourceError, setSourceError] = useState("");
  const [inspectionError, setInspectionError] = useState("");
  const [formError, setFormError] = useState("");
  const [fieldErrors, setFieldErrors] = useState<{ name?: string; description?: string; localPath?: string }>({});
  const [inspection, setInspection] = useState<InspectResponse | null>(null);
  const [inspectedKey, setInspectedKey] = useState("");
  const pollInFlight = useRef(false);
  const priorConnection = useRef<{ id: string; status: string }>({ id: "", status: "" });
  const errorSummary = useRef<HTMLDivElement>(null);

  const capability = useQuery({ queryKey: ["system-status"], queryFn: api.status });
  const connections = useQuery({ queryKey: ["source-connections"], queryFn: api.sourceConnections, enabled: kind === "github" && capability.data?.capabilities.githubConnections === true });
  const selectedConnection = connections.data?.items.find((connection) => connection.id === selectedConnectionId);
  const selectedStatus = connectionStatus(selectedConnection, pendingStatus);
  const isConnected = selectedStatus === "connected";

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
  const exactSourceKey = source?.composePath ? JSON.stringify(source) : "";
  const exactInspection = Boolean(source?.composePath) && inspection !== null && inspectedKey === exactSourceKey && inspection.findings.length === 0;

  const resetInspection = () => {
    setInspection(null);
    setInspectedKey("");
    setInspectionError("");
  };
  const focusErrorSummary = () => window.setTimeout(() => errorSummary.current?.focus(), 0);
  const resetAfterConnection = () => {
    setInstallationId(null);
    setRepositoryId(null);
    setBranch("");
    setComposePath("");
    setInstallationPage(1);
    setRepositoryPage(1);
    setBranchPage(1);
    resetInspection();
  };
  const resetAfterInstallation = () => {
    setRepositoryId(null);
    setBranch("");
    setComposePath("");
    setRepositoryPage(1);
    setBranchPage(1);
    resetInspection();
  };
  const resetAfterRepository = () => {
    setBranch("");
    setComposePath("");
    setBranchPage(1);
    resetInspection();
  };
  const resetAfterBranch = () => {
    setComposePath("");
    resetInspection();
  };
  const changeInstallationPage = (page: number) => {
    setInstallationPage(page);
    setInstallationId(null);
    setRepositoryId(null);
    setBranch("");
    setComposePath("");
    setRepositoryPage(1);
    setBranchPage(1);
    resetInspection();
  };
  const changeRepositoryPage = (page: number) => {
    setRepositoryPage(page);
    setRepositoryId(null);
    setBranch("");
    setComposePath("");
    setBranchPage(1);
    resetInspection();
  };
  const changeBranchPage = (page: number) => {
    setBranchPage(page);
    setBranch("");
    setComposePath("");
    resetInspection();
  };

  useEffect(() => {
    const prior = priorConnection.current;
    const knownStatus = selectedConnection !== undefined || pendingStatus !== undefined;
    priorConnection.current = { id: selectedConnectionId, status: selectedStatus };
    if (kind === "github" && selectedConnectionId && prior.id === selectedConnectionId && prior.status === "connected" && knownStatus && selectedStatus !== "connected") resetAfterConnection();
  }, [kind, pendingStatus, selectedConnection, selectedConnectionId, selectedStatus]);

  useEffect(() => {
    if (!deviceAuthorization && pendingStatus && selectedConnection && selectedConnection.status !== "pending") setPendingStatus(undefined);
  }, [deviceAuthorization, pendingStatus, selectedConnection]);

  const beginConnection = useMutation({
    mutationFn: api.startGitHubConnection,
    onSuccess: async (authorization) => {
      setSourceError("");
      setDeviceAuthorization(authorization);
      setPendingStatus("pending");
      setSelectedConnectionId(authorization.connectionId);
      resetAfterConnection();
      await queryClient.invalidateQueries({ queryKey: ["source-connections"] });
    },
    onError: (error) => setSourceError(safeMessage(error, "Could not start GitHub authorization.")),
  });
  const refreshConnection = useMutation({
    mutationFn: api.refreshSourceConnection,
    onSuccess: async (connection) => {
      setPendingStatus(connection.status);
      setSourceError("");
      await queryClient.invalidateQueries({ queryKey: ["source-connections"] });
      setPendingStatus(undefined);
    },
    onError: (error) => setSourceError(safeMessage(error, "Could not refresh this connection.")),
  });
  const disconnectConnection = useMutation({
    mutationFn: api.disconnectSourceConnection,
    onSuccess: async () => {
      setDeviceAuthorization(null);
      setPendingStatus("disconnected");
      setSelectedConnectionId("");
      resetAfterConnection();
      await queryClient.invalidateQueries({ queryKey: ["source-connections"] });
    },
    onError: (error) => setSourceError(safeMessage(error, "Could not disconnect this connection.")),
  });
  const inspectSource = useMutation({
    mutationFn: (request: { request: { sourcePath?: string; githubSource?: GitHubSource }; key: string }) => api.inspect(request.request).then((result) => ({ result, key: request.key })),
    onSuccess: ({ result, key }) => {
      setInspection(result);
      setInspectedKey(key);
      setInspectionError("");
    },
    onError: (error) => {
      resetInspection();
      setInspectionError(safeMessage(error, "Could not inspect this source."));
    },
  });
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

  useEffect(() => {
    if (!deviceAuthorization || selectedConnectionId !== deviceAuthorization.connectionId) return;
    if (isDeviceAuthorizationExpired(deviceAuthorization.expiresAt)) {
      setPendingStatus("expired");
      setDeviceAuthorization(null);
      setSourceError("GitHub authorization expired. Start a new connection.");
      void queryClient.invalidateQueries({ queryKey: ["source-connections"] });
      return;
    }
    let cancelled = false;
    let timer: number | undefined;
    const schedule = (seconds: number) => {
      if (!cancelled) timer = window.setTimeout(poll, Math.max(1, seconds) * 1000);
    };
    const poll = async () => {
      if (cancelled || pollInFlight.current) return;
      pollInFlight.current = true;
      try {
        const connection = await api.pollGitHubConnection(deviceAuthorization.connectionId);
        if (cancelled) return;
        setPendingStatus(connection.status);
        await queryClient.invalidateQueries({ queryKey: ["source-connections"] });
        if (connection.status === "pending") schedule(deviceAuthorization.pollIntervalSeconds);
        else {
          setDeviceAuthorization(null);
        }
      } catch (error) {
        if (cancelled) return;
        if (error instanceof APIError && error.status === 429 && error.retryAfterSeconds) {
          schedule(error.retryAfterSeconds);
        } else if (error instanceof APIError && error.code === "authorization_denied") {
          setPendingStatus("denied");
          setDeviceAuthorization(null);
          setSourceError(error.detail);
          await queryClient.invalidateQueries({ queryKey: ["source-connections"] });
        } else if (error instanceof APIError && error.code === "authorization_expired") {
          setPendingStatus("expired");
          setDeviceAuthorization(null);
          setSourceError(error.detail);
          await queryClient.invalidateQueries({ queryKey: ["source-connections"] });
        } else {
          setSourceError(safeMessage(error, "Could not check GitHub authorization."));
          setDeviceAuthorization(null);
        }
      } finally {
        pollInFlight.current = false;
      }
    };
    schedule(deviceAuthorization.pollIntervalSeconds);
    return () => {
      cancelled = true;
      if (timer !== undefined) window.clearTimeout(timer);
    };
  }, [deviceAuthorization, queryClient, selectedConnectionId]);

  const save = () => {
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
      return;
    }
    let request: CreateApplicationRequest;
    if (kind === "local") {
      request = { name: name.trim(), description, sourcePath: localPath.trim() };
    } else {
      if (!source || !source.composePath || !exactInspection) {
        setFormError("Complete the GitHub source steps and a clean exact-source inspection before saving.");
        focusErrorSummary();
        return;
      }
      request = { name: name.trim(), description, githubSource: source };
    }
    create.mutate(request);
  };

  const githubEnabled = capability.data?.capabilities.githubConnections === true;
  const sourceIsBusy = beginConnection.isPending || refreshConnection.isPending || disconnectConnection.isPending;
  const githubSaveHelp = !githubEnabled
    ? "GitHub connections must be available before this application can be saved."
    : deviceAuthorization
      ? "Finish GitHub device authorization before choosing the application source."
      : !isConnected
        ? "Choose a connected GitHub account before saving."
        : installationId === null
          ? "Choose a GitHub App installation before saving."
          : repositoryId === null
            ? "Choose a repository before saving."
            : !branch
              ? "Choose a tracked branch before saving."
              : !composePath
                ? inspection?.composeCandidates.length
                  ? "Choose a Compose file, then inspect the exact source before saving."
                  : inspection
                    ? "Add a Compose file to the tracked branch, then inspect again before saving."
                    : "Find and choose a Compose file before saving."
                : !exactInspection
                  ? inspection?.findings.length
                    ? "Resolve the source findings, then inspect the exact source again before saving."
                    : "Inspect the selected Compose file before saving."
                  : "The exact source inspection is clean. This application is ready to save.";
  return <div className="wizard source-wizard">
    <ol aria-label="Setup progress"><li aria-current="step">Source and review</li><li>Review and save</li></ol>
    <form onSubmit={(event) => { event.preventDefault(); save(); }} noValidate>
      <h2>Application source</h2>
      <p>Save a local project draft, or connect a GitHub repository without keeping a checkout on this computer.</p>
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
        <label><input type="radio" name="source-kind" checked={kind === "local"} onChange={() => { setKind("local"); setFormError(""); setSourceError(""); resetInspection(); }} /> Local folder</label>
        <label><input type="radio" name="source-kind" checked={kind === "github"} onChange={() => { setKind("github"); setFormError(""); setSourceError(""); setFieldErrors((current) => ({ ...current, localPath: undefined })); resetInspection(); }} /> GitHub repository</label>
      </fieldset>

      {kind === "local" ? <section className="source-panel" aria-labelledby="local-source-title">
        <h3 id="local-source-title">Local folder</h3>
        <div className="field">
          <label htmlFor="wizard-source-path">Local source path <span aria-hidden="true">*</span></label>
          <input id="wizard-source-path" required placeholder="C:\projects\my-app" value={localPath} aria-invalid={Boolean(fieldErrors.localPath)} aria-describedby={fieldErrors.localPath ? "wizard-source-path-error" : undefined} onChange={(event) => { setLocalPath(event.target.value); setFieldErrors((current) => ({ ...current, localPath: undefined })); setFormError(""); resetInspection(); }} />
          {fieldErrors.localPath && <p id="wizard-source-path-error" className="form-error">{fieldErrors.localPath}</p>}
        </div>
        <button type="button" className="button" disabled={!localPath.trim() || inspectSource.isPending} onClick={() => inspectSource.mutate({ request: { sourcePath: localPath.trim() }, key: `local:${localPath.trim()}` })}>{inspectSource.isPending ? "Checking…" : "Check source"}</button>
        {inspectionError && <div className="callout danger" role="alert">{inspectionError}</div>}
        <InspectionSummary inspection={inspection} />
      </section> : <section className="source-panel" aria-labelledby="github-source-title">
        <h3 id="github-source-title">GitHub repository</h3>
        {capability.isLoading ? <div className="callout info" role="status">Checking GitHub connection capability…</div> : capability.isError ? <div className="callout danger" role="alert"><strong>Could not check GitHub capability</strong><span>{safeMessage(capability.error, "The controller status could not be loaded.")}</span><button type="button" className="button small" onClick={() => void capability.refetch()}>Retry capability check</button></div> : !githubEnabled ? <div className="callout warning"><strong>GitHub connections are unavailable</strong><span>This controller has not enabled the GitHub App client configuration.</span></div> : <>
          <div className="connection-actions">
            <button type="button" className="button" disabled={sourceIsBusy} onClick={() => beginConnection.mutate()}>{beginConnection.isPending ? "Starting…" : "Connect GitHub"}</button>
            {selectedConnectionId && selectedStatus === "connected" && <button type="button" className="button" disabled={sourceIsBusy} onClick={() => refreshConnection.mutate(selectedConnectionId)}>{refreshConnection.isPending ? "Refreshing…" : "Refresh connection"}</button>}
            {selectedConnectionId && <button type="button" className="button" disabled={sourceIsBusy} onClick={() => disconnectConnection.mutate(selectedConnectionId)}>{disconnectConnection.isPending ? "Disconnecting…" : "Disconnect"}</button>}
          </div>
          <div className={deviceAuthorization ? "callout info device-authorization" : sourceError ? "callout danger connection-status" : selectedConnectionId && !isConnected ? "callout warning connection-status" : "wizard-status connection-status"} role="status" aria-live="polite" aria-atomic="true">
            {deviceAuthorization ? <>
              <strong>Authorize this controller</strong><span>Enter code <code>{deviceAuthorization.userCode}</code> at GitHub, then install the app for the repository you want to deploy.</span>
              <span><a href={deviceAuthorization.verificationUri} target="_blank" rel="noreferrer">Open GitHub device authorization (opens in a new tab)</a> · <a href={deviceAuthorization.installUrl} target="_blank" rel="noreferrer">Install or configure the Rig GitHub App (opens in a new tab)</a></span>
            </> : selectedConnectionId ? <>
              <strong>{isConnected ? "GitHub connection ready" : "GitHub connection needs attention"}</strong>
              <span>{`Connection status: ${selectedStatus.replaceAll("_", " ")}.`}</span>
              {sourceError && <span>{sourceError}</span>}
              {!sourceError && selectedStatus === "access_lost" && <span>GitHub access was lost. Start a new connection before browsing repositories.</span>}
              {!sourceError && !isConnected && selectedStatus !== "access_lost" && <span>Start a new connection or choose another connection.</span>}
            </> : sourceError ? <><strong>GitHub connection failed</strong><span>{sourceError}</span></> : <span>Choose an existing connection or start a new one.</span>}
          </div>
          <div className="field">
            <label htmlFor="github-connection">GitHub connection</label>
            <select id="github-connection" value={selectedConnectionId} disabled={connections.isLoading || connections.isError} onChange={(event) => { setSelectedConnectionId(event.target.value); setPendingStatus(undefined); setDeviceAuthorization(null); setSourceError(""); resetAfterConnection(); }}>
              <option value="">{connections.isLoading ? "Loading connections…" : connections.isError ? "Connections unavailable" : "Choose a connection"}</option>
              {connections.data?.items.map((connection) => <option key={connection.id} value={connection.id}>{connectionLabel(connection)}</option>)}
            </select>
          </div>
          {connections.isError && <div className="callout danger" role="alert"><strong>Could not load GitHub connections</strong><span>{safeMessage(connections.error, "The connection list could not be loaded.")}</span><button type="button" className="button small" onClick={() => void connections.refetch()}>Retry connections</button></div>}
          {!connections.isLoading && !connections.isError && connections.data?.items.length === 0 && <div className="callout info"><strong>No GitHub connections yet</strong><span>Start a GitHub connection to authorize this controller.</span></div>}
          {isConnected && <div className="source-selects">
            <SourceSelect label="GitHub App installation" id="github-installation" value={installationId?.toString() ?? ""} onChange={(value) => { setInstallationId(value ? Number(value) : null); resetAfterInstallation(); }} loading={installations.isLoading} error={installations.error} disabled={installations.isLoading} placeholder="Choose an installation" emptyTitle="No GitHub App installations found" emptyMessage="No GitHub App installations are available. Install or configure the Rig GitHub App, then retry." onRetry={() => void installations.refetch()} items={installations.data?.items.map((item) => ({ value: String(item.id), label: `${item.accountLogin} (${item.repositorySelection} repositories)` })) ?? []} />
            <PaginationControls label="GitHub App installations" page={installationPage} onPageChange={changeInstallationPage} hasNext={(installations.data?.page ?? 0) * (installations.data?.perPage ?? pageSize) < (installations.data?.totalCount ?? 0)} hasResults={!installations.isLoading && !installations.isError && (installations.data?.items.length ?? 0) > 0} />
            {installationId !== null && <><SourceSelect label="Repository" id="github-repository" value={repositoryId?.toString() ?? ""} onChange={(value) => { setRepositoryId(value ? Number(value) : null); resetAfterRepository(); }} loading={repositories.isLoading} error={repositories.error} disabled={repositories.isLoading} placeholder="Choose a repository" emptyTitle="No repositories found" emptyMessage="No accessible repositories are available. Update the GitHub App repository access, then retry." onRetry={() => void repositories.refetch()} items={repositories.data?.items.filter((item) => !item.archived && !item.disabled).map((item) => ({ value: String(item.id), label: `${item.owner}/${item.name}${item.private ? " (private)" : ""}` })) ?? []} />
            <PaginationControls label="repositories" page={repositoryPage} onPageChange={changeRepositoryPage} hasNext={(repositories.data?.page ?? 0) * (repositories.data?.perPage ?? pageSize) < (repositories.data?.totalCount ?? 0)} hasResults={!repositories.isLoading && !repositories.isError && (repositories.data?.items.filter((item) => !item.archived && !item.disabled).length ?? 0) > 0} /></>}
            {repositoryId !== null && <><SourceSelect label="Tracked branch" id="github-branch" value={branch} onChange={(value) => { setBranch(value); resetAfterBranch(); }} loading={branches.isLoading} error={branches.error} disabled={branches.isLoading} placeholder="Choose a branch" emptyTitle="No branches found" emptyMessage="No branches are available. Push a tracked branch or choose another repository, then retry." onRetry={() => void branches.refetch()} items={branches.data?.items.map((item) => ({ value: item.name, label: item.protected ? `${item.name} (protected)` : item.name })) ?? []} />
            <PaginationControls label="branches" page={branchPage} onPageChange={changeBranchPage} hasNext={(branches.data?.items.length ?? 0) === (branches.data?.perPage ?? pageSize)} hasResults={!branches.isLoading && !branches.isError && (branches.data?.items.length ?? 0) > 0} /></>}
          </div>}
          {source && !composePath && <button type="button" className="button" disabled={inspectSource.isPending} onClick={() => inspectSource.mutate({ request: { githubSource: source }, key: "" })}>{inspectSource.isPending ? "Finding Compose files…" : "Find Compose files"}</button>}
          {inspectionError && <div className="callout danger" role="alert">{inspectionError}</div>}
          {inspection && source && !composePath && inspection.composeCandidates.length > 0 && <div className="field"><label htmlFor="github-compose-path">Compose file</label><select id="github-compose-path" value={composePath} onChange={(event) => { setComposePath(event.target.value); resetInspection(); }}><option value="">Choose a Compose file</option>{inspection.composeCandidates.map((candidate) => <option key={candidate} value={candidate}>{candidate}</option>)}</select></div>}
          {source?.composePath && <button type="button" className="button" disabled={inspectSource.isPending} onClick={() => inspectSource.mutate({ request: { githubSource: source }, key: JSON.stringify(source) })}>{inspectSource.isPending ? "Inspecting…" : "Inspect selected Compose file"}</button>}
          <InspectionSummary inspection={inspection} discovery={Boolean(source && !composePath)} />
        </>}
      </section>}
      {kind === "github" && <p id="github-save-help" className="save-help">{githubSaveHelp}</p>}
      <footer><button className="button" type="button" onClick={onCancel}>Back</button><button className="button primary" aria-describedby={kind === "github" ? "github-save-help" : undefined} disabled={create.isPending || (kind === "github" && (!githubEnabled || !exactInspection))}>{create.isPending ? "Saving…" : "Save application"}</button></footer>
    </form>
  </div>;
}

function SourceSelect({ label, id, value, onChange, loading, error, disabled, placeholder, emptyTitle, emptyMessage, onRetry, items }: { label: string; id: string; value: string; onChange: (value: string) => void; loading: boolean; error: unknown; disabled: boolean; placeholder: string; emptyTitle: string; emptyMessage: string; onRetry: () => void; items: Array<{ value: string; label: string }> }) {
  return <div className="field">
    <label htmlFor={id}>{label}</label>
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
