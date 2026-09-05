import { useEffect, useMemo, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { APIError, api, type CreateApplicationRequest, type ConnectedGitHubRepository, type GitHubSource, type InspectResponse } from "./api";
import { GitHubConnectionCard, GitHubRepositoryPicker, githubConnectionKey } from "./github-connection";

const pageSize = 30;
type SourceKind = "local" | "github";
function safeMessage(error: unknown, fallback: string) { return error instanceof APIError || error instanceof Error ? error.message : fallback; }

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
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [localPath, setLocalPath] = useState("");
  const [repository, setRepository] = useState<ConnectedGitHubRepository | null>(null);
  const [branch, setBranch] = useState("");
  const [composePath, setComposePath] = useState("");
  const [branchPage, setBranchPage] = useState(1);
  const [inspectionError, setInspectionError] = useState("");
  const [formError, setFormError] = useState("");
  const [fieldErrors, setFieldErrors] = useState<{ name?: string; description?: string; localPath?: string }>({});
  const [inspection, setInspection] = useState<InspectResponse | null>(null);
  const [inspectedKey, setInspectedKey] = useState("");
  const inspectionGeneration = useRef(0);
  const inspectionRequest = useRef<{ generation: number; key: string } | null>(null);
  const errorSummary = useRef<HTMLDivElement>(null);
  const capability = useQuery({ queryKey: ["system-status"], queryFn: api.status });
  const githubEnabled = capability.data?.capabilities.githubConnections === true;
  const connection = useQuery({ queryKey: githubConnectionKey, queryFn: api.defaultSourceConnection, enabled: kind === "github" && githubEnabled, retry: false });
  const isConnected = connection.data?.connection?.status === "connected";
  const selectedConnectionId = repository?.connectionId ?? "";
  const installationId = repository?.installationId ?? null;
  const repositoryId = repository?.id ?? null;
  const branches = useQuery({
    queryKey: ["github-branches", selectedConnectionId, installationId, repositoryId, branchPage, pageSize],
    queryFn: () => api.githubBranches(selectedConnectionId, installationId!, repositoryId!, branchPage, pageSize),
    enabled: kind === "github" && isConnected && Boolean(repository),
  });
  const source = useMemo<GitHubSource | null>(() => {
    if (!repository || !branch) return null;
    return { connectionId: repository.connectionId, installationId: repository.installationId, repositoryId: repository.id, branch, ...(composePath ? { composePath } : {}) };
  }, [repository, branch, composePath]);
  const exactSourceKey = source?.composePath ? JSON.stringify(source) : "";
  const exactInspection = isConnected && selectedConnectionId === connection.data?.connection?.id && Boolean(source?.composePath) && inspection !== null && inspectedKey === exactSourceKey && inspection.findings.length === 0;
  const clearInspection = () => { setInspection(null); setInspectedKey(""); setInspectionError(""); };
  const invalidateInspection = () => { inspectionGeneration.current += 1; inspectionRequest.current = null; clearInspection(); };
  const focusErrorSummary = () => window.setTimeout(() => errorSummary.current?.focus(), 0);
  const resetAfterRepository = () => { setBranch(""); setComposePath(""); setBranchPage(1); invalidateInspection(); };
  const resetAfterBranch = () => { setComposePath(""); invalidateInspection(); };
  const changeBranchPage = (page: number) => { setBranchPage(page); setBranch(""); setComposePath(""); invalidateInspection(); };
  useEffect(() => {
    if (connection.data && repository && (!isConnected || repository.connectionId !== connection.data.connection?.id)) {
      setRepository(null); resetAfterRepository();
    }
  }, [connection.data, isConnected, repository]);
  const inspectSource = useMutation({
    mutationFn: (operation: { request: { sourcePath?: string; githubSource?: GitHubSource }; key: string; generation: number }) => api.inspect(operation.request).then((result) => ({ result, key: operation.key, generation: operation.generation })),
    onSuccess: ({ result, key, generation }) => {
      const currentRequest = inspectionRequest.current;
      if (!currentRequest || generation !== inspectionGeneration.current || currentRequest.generation !== generation || currentRequest.key !== key) return;
      setInspection(result);
      setInspectedKey(key);
      setInspectionError("");
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

  const githubSaveHelp = !githubEnabled ? "GitHub connections must be enabled before this application can be saved."
    : !isConnected ? "Connect GitHub before saving."
    : !repository ? "Choose a repository before saving."
    : !branch ? "Choose a tracked branch before saving."
    : !composePath ? "Find and choose a Compose file before saving."
    : !exactInspection ? "Inspect the exact source and resolve its findings before saving."
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
        <label><input type="radio" name="source-kind" checked={kind === "local"} onChange={() => { setKind("local"); setFormError(""); invalidateInspection(); }} /> Local folder</label>
        <label><input type="radio" name="source-kind" checked={kind === "github"} onChange={() => { setKind("github"); setFormError(""); setFieldErrors((current) => ({ ...current, localPath: undefined })); invalidateInspection(); }} /> GitHub repository</label>
      </fieldset>

      {kind === "local" ? <section className="source-panel" aria-labelledby="local-source-title">
        <h3 id="local-source-title">Local folder</h3>
        <div className="field">
          <label htmlFor="wizard-source-path">Local source path <span aria-hidden="true">*</span></label>
          <input id="wizard-source-path" required placeholder={"C:\\projects\\my-app"} value={localPath} aria-invalid={Boolean(fieldErrors.localPath)} aria-describedby={fieldErrors.localPath ? "wizard-source-path-error" : undefined} onChange={(event) => { setLocalPath(event.target.value); setFieldErrors((current) => ({ ...current, localPath: undefined })); setFormError(""); invalidateInspection(); }} />
          {fieldErrors.localPath && <p id="wizard-source-path-error" className="form-error">{fieldErrors.localPath}</p>}
        </div>
        <button type="button" className="button" disabled={!localPath.trim() || inspectSource.isPending} onClick={() => runInspection({ sourcePath: localPath.trim() }, `local:${localPath.trim()}`)}>{inspectSource.isPending ? "Checking…" : "Check source"}</button>
        {inspectionError && <div className="callout danger" role="alert">{inspectionError}</div>}
        <InspectionSummary inspection={inspection} />
      </section> : <section className="source-panel" aria-labelledby="github-source-title">
        <h3 id="github-source-title">GitHub repository</h3>
        <span className="sr-only capability-status" role="status" aria-live="polite" aria-atomic="true">{capability.isFetching ? "Checking GitHub connection capability." : capability.isError ? "GitHub connection capability check failed." : githubEnabled ? "GitHub connections are available." : "GitHub connections are disabled."}</span>
        {capability.isLoading ? <div className="callout info">Checking GitHub connection capability…</div> : capability.isError ? <div className="callout danger"><strong>Could not check GitHub capability</strong><span>{safeMessage(capability.error, "The controller status could not be loaded.")}</span><button type="button" className="button small" onClick={() => void capability.refetch()}>Retry capability check</button></div> : !githubEnabled ? <div className="callout warning"><strong>GitHub connections are disabled</strong><span>The administrator disabled GitHub connections on this controller.</span></div> : <>
          <GitHubConnectionCard />
          {isConnected && <div className="source-selects">
            <GitHubRepositoryPicker id="github-repository" value={repository} onChange={(value) => { setRepository(value); resetAfterRepository(); }} />
            {repositoryId !== null && <><SourceSelect label="Tracked branch" collectionLabel="Branches" page={branchPage} id="github-branch" value={branch} onChange={(value) => { setBranch(value); resetAfterBranch(); }} loading={branches.isFetching} error={branches.error} disabled={branches.isFetching} placeholder="Choose a branch" emptyTitle="No branches found" emptyMessage="No branches are available. Push a tracked branch or choose another repository, then retry." onRetry={() => void branches.refetch()} items={branches.data?.items.map((item) => ({ value: item.name, label: item.protected ? `${item.name} (protected)` : item.name })) ?? []} />
            <PaginationControls label="branches" page={branchPage} onPageChange={changeBranchPage} hasNext={(branches.data?.items.length ?? 0) === (branches.data?.perPage ?? pageSize)} loading={branches.isFetching} statusId="github-branch-status" /></>}
          </div>}
          {source && !composePath && <button type="button" className="button" disabled={inspectSource.isPending} onClick={() => runInspection({ githubSource: source }, "")}>{inspectSource.isPending ? "Finding Compose files…" : "Find Compose files"}</button>}
          {inspectionError && <div className="callout danger" role="alert">{inspectionError}</div>}
          {inspection && source && !composePath && inspection.composeCandidates.length > 0 && <div className="field"><label htmlFor="github-compose-path">Compose file</label><select id="github-compose-path" value={composePath} onChange={(event) => { setComposePath(event.target.value); invalidateInspection(); }}><option value="">Choose a Compose file</option>{inspection.composeCandidates.map((candidate) => <option key={candidate} value={candidate}>{candidate}</option>)}</select></div>}
          {source?.composePath && <button type="button" className="button" disabled={inspectSource.isPending} onClick={() => runInspection({ githubSource: source }, JSON.stringify(source))}>{inspectSource.isPending ? "Inspecting…" : "Inspect selected Compose file"}</button>}
          <InspectionSummary inspection={inspection} discovery={Boolean(source && !composePath)} />
        </>}
      </section>}
      {kind === "github" && <p id="github-save-help" className="save-help">{githubSaveHelp}</p>}
      <footer><button className="button" type="button" onClick={onCancel}>Back</button><button className="button primary" aria-describedby={kind === "github" ? "github-save-help" : undefined} disabled={create.isPending || (kind === "github" && (!githubEnabled || !exactInspection))}>{create.isPending ? "Saving…" : "Save application"}</button></footer>
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
