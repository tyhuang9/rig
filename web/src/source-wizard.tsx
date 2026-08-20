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

function usePageControls(page: number, setPage: (page: number) => void, hasNext: boolean) {
  return <div className="wizard-pagination" aria-label="Pagination">
    <button type="button" className="button small" disabled={page === 1} onClick={() => setPage(page - 1)}>Previous</button>
    <span>Page {page}</span>
    <button type="button" className="button small" disabled={!hasNext} onClick={() => setPage(page + 1)}>Next</button>
  </div>;
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
  const [formError, setFormError] = useState("");
  const [inspection, setInspection] = useState<InspectResponse | null>(null);
  const [inspectedKey, setInspectedKey] = useState("");
  const pollInFlight = useRef(false);
  const priorConnection = useRef<{ id: string; status: string }>({ id: "", status: "" });

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
  };
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

  useEffect(() => {
    const prior = priorConnection.current;
    const knownStatus = selectedConnection !== undefined || pendingStatus !== undefined;
    priorConnection.current = { id: selectedConnectionId, status: selectedStatus };
    if (kind === "github" && selectedConnectionId && prior.id === selectedConnectionId && prior.status === "connected" && knownStatus && selectedStatus !== "connected") resetAfterConnection();
  }, [kind, pendingStatus, selectedConnection, selectedConnectionId, selectedStatus]);

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
      setSourceError("");
    },
    onError: (error) => setSourceError(safeMessage(error, "Could not inspect this source.")),
  });
  const create = useMutation({
    mutationFn: api.createApp,
    onSuccess: async (application) => {
      await queryClient.invalidateQueries({ queryKey: ["apps"] });
      onCreated(application.id);
    },
    onError: (error) => setFormError(safeMessage(error, "Could not save the application.")),
  });

  useEffect(() => {
    if (!deviceAuthorization || selectedConnectionId !== deviceAuthorization.connectionId || deviceAuthorization.expiresAt <= new Date().toISOString()) return;
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
          setPendingStatus(undefined);
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
          setPendingStatus(undefined);
        } else if (error instanceof APIError && error.code === "authorization_expired") {
          setPendingStatus("expired");
          setDeviceAuthorization(null);
          setSourceError(error.detail);
          await queryClient.invalidateQueries({ queryKey: ["source-connections"] });
          setPendingStatus(undefined);
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
    if (!name.trim()) {
      setFormError("Enter an application name.");
      return;
    }
    if (name.trim().length > 100 || description.length > 300) {
      setFormError("Use 100 characters or fewer for the name and 300 for the description.");
      return;
    }
    let request: CreateApplicationRequest;
    if (kind === "local") {
      if (!localPath.trim()) {
        setFormError("Enter a local source path.");
        return;
      }
      request = { name: name.trim(), description, sourcePath: localPath.trim() };
    } else {
      if (!source || !source.composePath || !exactInspection) {
        setFormError("Select a Compose file and complete a clean exact-source inspection before saving.");
        return;
      }
      request = { name: name.trim(), description, githubSource: source };
    }
    create.mutate(request);
  };

  const githubEnabled = capability.data?.capabilities.githubConnections === true;
  const sourceIsBusy = beginConnection.isPending || refreshConnection.isPending || disconnectConnection.isPending;
  return <div className="wizard source-wizard">
    <ol aria-label="Setup progress"><li aria-current="step">Source and review</li><li>Development deployment</li></ol>
    <form onSubmit={(event) => { event.preventDefault(); save(); }} noValidate>
      <h2>Application source</h2>
      <p>Save a local project draft, or connect a GitHub repository without keeping a checkout on this computer.</p>
      {formError && <div className="error-summary" role="alert" tabIndex={-1}>{formError}</div>}
      <div className="field"><label htmlFor="wizard-name">Application name <span aria-hidden="true">*</span></label><input id="wizard-name" required value={name} onChange={(event) => setName(event.target.value)} /></div>
      <div className="field"><label htmlFor="wizard-description">Description</label><input id="wizard-description" value={description} onChange={(event) => setDescription(event.target.value)} /></div>

      <fieldset className="source-choice">
        <legend>Source type</legend>
        <label><input type="radio" name="source-kind" checked={kind === "local"} onChange={() => { setKind("local"); setFormError(""); }} /> Local folder</label>
        <label><input type="radio" name="source-kind" checked={kind === "github"} onChange={() => { setKind("github"); setFormError(""); }} /> GitHub repository</label>
      </fieldset>

      {kind === "local" ? <section className="source-panel" aria-labelledby="local-source-title">
        <h3 id="local-source-title">Local folder</h3>
        <div className="field"><label htmlFor="wizard-source-path">Local source path <span aria-hidden="true">*</span></label><input id="wizard-source-path" required placeholder="C:\projects\my-app" value={localPath} onChange={(event) => { setLocalPath(event.target.value); resetInspection(); }} /></div>
        <button type="button" className="button" disabled={!localPath.trim() || inspectSource.isPending} onClick={() => inspectSource.mutate({ request: { sourcePath: localPath.trim() }, key: `local:${localPath.trim()}` })}>{inspectSource.isPending ? "Checking…" : "Check source"}</button>
        <InspectionSummary inspection={inspection} />
      </section> : <section className="source-panel" aria-labelledby="github-source-title">
        <h3 id="github-source-title">GitHub repository</h3>
        {capability.isLoading ? <div className="callout info" role="status">Checking GitHub connection capability…</div> : !githubEnabled ? <div className="callout warning" role="status"><strong>GitHub connections are unavailable</strong><span>This controller has not enabled the GitHub App client configuration.</span></div> : <>
          <div className="connection-actions">
            <button type="button" className="button" disabled={sourceIsBusy} onClick={() => beginConnection.mutate()}>{beginConnection.isPending ? "Starting…" : "Connect GitHub"}</button>
            {selectedConnectionId && selectedStatus === "connected" && <button type="button" className="button" disabled={refreshConnection.isPending} onClick={() => refreshConnection.mutate(selectedConnectionId)}>{refreshConnection.isPending ? "Refreshing…" : "Refresh connection"}</button>}
            {selectedConnectionId && <button type="button" className="button" disabled={disconnectConnection.isPending} onClick={() => disconnectConnection.mutate(selectedConnectionId)}>{disconnectConnection.isPending ? "Disconnecting…" : "Disconnect"}</button>}
          </div>
          <div className="wizard-status" role="status" aria-live="polite">{deviceAuthorization ? "Waiting for GitHub authorization." : selectedConnectionId ? `Connection status: ${selectedStatus.replaceAll("_", " ")}.` : "Choose an existing connection or start a new one."}</div>
          {deviceAuthorization && <div className="callout info">
            <strong>Authorize this controller</strong><span>Enter code <code>{deviceAuthorization.userCode}</code> at GitHub, then install the app for the repository you want to deploy.</span>
            <span><a href={deviceAuthorization.verificationUri} target="_blank" rel="noreferrer">Open GitHub device authorization</a> · <a href={deviceAuthorization.installUrl} target="_blank" rel="noreferrer">Install or configure the Rig GitHub App</a></span>
          </div>}
          {sourceError && <div className="callout danger" role="alert">{sourceError}</div>}
          {connections.isError ? <div className="callout danger" role="alert">{safeMessage(connections.error, "Could not load GitHub connections.")}</div> : <div className="field"><label htmlFor="github-connection">GitHub connection</label><select id="github-connection" value={selectedConnectionId} onChange={(event) => { setSelectedConnectionId(event.target.value); setPendingStatus(undefined); setDeviceAuthorization(null); resetAfterConnection(); }}>{<option value="">Choose a connection</option>}{connections.data?.items.map((connection) => <option key={connection.id} value={connection.id}>{connectionLabel(connection)}</option>)}</select></div>}
          {selectedConnectionId && !isConnected && !deviceAuthorization && <div className="callout warning" role="status"><strong>Connection needs attention</strong><span>{selectedStatus === "access_lost" ? "GitHub access was lost. Reconnect before browsing repositories." : `This connection is ${selectedStatus.replaceAll("_", " ")}. Reconnect or choose another connection.`}</span></div>}
          {isConnected && <div className="source-selects">
            <SourceSelect label="GitHub App installation" id="github-installation" value={installationId?.toString() ?? ""} onChange={(value) => { setInstallationId(value ? Number(value) : null); resetAfterInstallation(); }} loading={installations.isLoading} error={installations.error} disabled={installations.isLoading} placeholder="Choose an installation" items={installations.data?.items.map((item) => ({ value: String(item.id), label: `${item.accountLogin} (${item.repositorySelection} repositories)` })) ?? []} />
            {usePageControls(installationPage, setInstallationPage, (installations.data?.page ?? 0) * (installations.data?.perPage ?? pageSize) < (installations.data?.totalCount ?? 0))}
            {installationId !== null && <><SourceSelect label="Repository" id="github-repository" value={repositoryId?.toString() ?? ""} onChange={(value) => { setRepositoryId(value ? Number(value) : null); resetAfterRepository(); }} loading={repositories.isLoading} error={repositories.error} disabled={repositories.isLoading} placeholder="Choose a repository" items={repositories.data?.items.filter((item) => !item.archived && !item.disabled).map((item) => ({ value: String(item.id), label: `${item.owner}/${item.name}${item.private ? " (private)" : ""}` })) ?? []} />
            {usePageControls(repositoryPage, setRepositoryPage, (repositories.data?.page ?? 0) * (repositories.data?.perPage ?? pageSize) < (repositories.data?.totalCount ?? 0))}</>}
            {repositoryId !== null && <><SourceSelect label="Tracked branch" id="github-branch" value={branch} onChange={(value) => { setBranch(value); resetAfterBranch(); }} loading={branches.isLoading} error={branches.error} disabled={branches.isLoading} placeholder="Choose a branch" items={branches.data?.items.map((item) => ({ value: item.name, label: item.protected ? `${item.name} (protected)` : item.name })) ?? []} />
            {usePageControls(branchPage, setBranchPage, (branches.data?.items.length ?? 0) === (branches.data?.perPage ?? pageSize))}</>}
          </div>}
          {source && !composePath && <button type="button" className="button" disabled={inspectSource.isPending} onClick={() => inspectSource.mutate({ request: { githubSource: source }, key: "" })}>{inspectSource.isPending ? "Finding Compose files…" : "Find Compose files"}</button>}
          {inspection && source && !composePath && <div className="field"><label htmlFor="github-compose-path">Compose file</label><select id="github-compose-path" value={composePath} onChange={(event) => { setComposePath(event.target.value); resetInspection(); }}><option value="">Choose a Compose file</option>{inspection.composeCandidates.map((candidate) => <option key={candidate} value={candidate}>{candidate}</option>)}</select></div>}
          {source?.composePath && <button type="button" className="button" disabled={inspectSource.isPending} onClick={() => inspectSource.mutate({ request: { githubSource: source }, key: JSON.stringify(source) })}>{inspectSource.isPending ? "Inspecting…" : "Inspect selected Compose file"}</button>}
          <InspectionSummary inspection={inspection} />
        </>}
      </section>}
      <footer><button className="button" type="button" onClick={onCancel}>Back</button><button className="button primary" disabled={create.isPending || (kind === "github" && (!githubEnabled || !exactInspection))}>{create.isPending ? "Saving…" : "Save application"}</button></footer>
    </form>
  </div>;
}

function SourceSelect({ label, id, value, onChange, loading, error, disabled, placeholder, items }: { label: string; id: string; value: string; onChange: (value: string) => void; loading: boolean; error: unknown; disabled: boolean; placeholder: string; items: Array<{ value: string; label: string }> }) {
  return <div className="field"><label htmlFor={id}>{label}</label>{error ? <div className="callout danger" role="alert">{safeMessage(error, `Could not load ${label.toLowerCase()}.`)}</div> : <select id={id} value={value} disabled={disabled} onChange={(event) => onChange(event.target.value)}><option value="">{loading ? "Loading…" : placeholder}</option>{items.map((item) => <option key={item.value} value={item.value}>{item.label}</option>)}</select>}</div>;
}

function InspectionSummary({ inspection }: { inspection: InspectResponse | null }) {
  if (!inspection) return null;
  if (inspection.findings.length > 0) return <div className="callout warning" role="status"><strong>Source needs changes before deployment</strong>{inspection.findings.map((finding) => <span key={`${finding.code}:${finding.path ?? ""}`}>{finding.message}</span>)}</div>;
  return <div className="callout success" role="status"><strong>Source inspection completed</strong><span>{inspection.resolvedSha ? `Resolved ${inspection.resolvedSha.slice(0, 12)}. ` : ""}Found {inspection.services.length} service{inspection.services.length === 1 ? "" : "s"}.</span></div>;
}
