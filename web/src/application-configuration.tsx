import { useEffect, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { APIError, api, type ApplicationConfiguration } from "./api";
import { useUnsavedChanges } from "./unsaved-changes";

type VariableRow = { id: string; key: string; value: string; stored: boolean };
type SecretRow = { id: string; key: string; value: string; stored: boolean };
type RowError = { key?: string; value?: string };

const portableEnvironmentName = /^[A-Za-z_][A-Za-z0-9_]*$/;
const maxKeyLength = 128;
const maxValueLength = 8192;

export function ApplicationConfigurationPanel({ appId }: { appId: string }) {
  return <ApplicationConfigurationEditor key={appId} appId={appId}/>;
}

function ApplicationConfigurationEditor({ appId }: { appId: string }) {
  const queryClient = useQueryClient();
  const query = useQuery({ queryKey: ["app-configuration", appId], queryFn: () => api.applicationConfiguration(appId), retry: false });
  const [revision, setRevision] = useState(0);
  const [variables, setVariables] = useState<VariableRow[]>([]);
  const [secrets, setSecrets] = useState<SecretRow[]>([]);
  const [removed, setRemoved] = useState<Set<string>>(new Set());
  const [revealed, setRevealed] = useState<Set<string>>(new Set());
  const [rowErrors, setRowErrors] = useState<Record<string, RowError>>({});
  const [dirty, setDirty] = useState(false);
  const [message, setMessage] = useState("");
  const [announcement, setAnnouncement] = useState("");
  const [clientError, setClientError] = useState("");
  const [pendingFocus, setPendingFocus] = useState("");
  const nextID = useRef(0);
  const hydratedIdentity = useRef("");
  const saving = useRef(false);
  const errorSummary = useRef<HTMLDivElement>(null);

  useUnsavedChanges(dirty);

  const createID = (kind: string) => `configuration-${kind}-${++nextID.current}`;
  const markDirty = (nextAnnouncement = "") => {
    if (saving.current) return;
    setDirty(true);
    setMessage("");
    setAnnouncement(nextAnnouncement);
  };
  const hydrate = (configuration: ApplicationConfiguration) => {
    const nextVariables = configuration.entries
      .filter((entry) => !entry.sensitive)
      .map((entry) => ({ id: createID("variable"), key: entry.key, value: entry.value ?? "", stored: true }));
    const nextSecrets = configuration.entries
      .filter((entry) => entry.sensitive)
      .map((entry) => ({ id: createID("secret"), key: entry.key, value: "", stored: true }));
    setRevision(configuration.revisionNumber);
    setVariables(nextVariables);
    setSecrets(nextSecrets);
    setRemoved(new Set());
    setRevealed(new Set());
    setRowErrors({});
    setDirty(false);
    setClientError("");
    setAnnouncement("");
    hydratedIdentity.current = `${configuration.revisionId ?? ""}:${configuration.revisionNumber}`;
    return nextVariables[0]?.id ? `${nextVariables[0].id}-key` : nextSecrets[0]?.id ? `${nextSecrets[0].id}-value` : "configuration-add-variable";
  };

  useEffect(() => {
    const identity = query.data ? `${query.data.revisionId ?? ""}:${query.data.revisionNumber}` : "";
    if (query.data && !dirty && hydratedIdentity.current !== identity) hydrate(query.data);
    // Dirty edits intentionally survive background query refreshes.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [query.data]);

  useEffect(() => {
    if (!pendingFocus) return;
    document.getElementById(pendingFocus)?.focus();
    setPendingFocus("");
  }, [pendingFocus, removed, secrets, variables]);

  const mutation = useMutation({
    mutationFn: () => api.replaceApplicationConfiguration(appId, {
      expectedRevisionNumber: revision,
      variables: variables.filter((variable) => !removed.has(variable.key)).map(({ key, value }) => ({ key, value })),
      secrets: secrets.filter((secret) => !removed.has(secret.key) && secret.value !== "").map(({ key, value }) => ({ key, value })),
      remove: [...removed],
    }),
    onSuccess: async (configuration) => {
      hydrate(configuration);
      setMessage(`Configuration revision ${configuration.revisionNumber} saved.`);
      await queryClient.setQueryData(["app-configuration", appId], configuration);
    },
    onError: () => window.setTimeout(() => errorSummary.current?.focus(), 0),
    onSettled: () => { saving.current = false; },
  });

  const clearRowError = (id: string, field: keyof RowError) => {
    setRowErrors((current) => {
      if (!current[id]?.[field]) return current;
      return { ...current, [id]: { ...current[id], [field]: undefined } };
    });
  };

  const validate = () => {
    const nextErrors: Record<string, RowError> = {};
    const owners = new Map<string, string>();
    const activeRows = [
      ...variables.filter((row) => !removed.has(row.key)).map((row) => ({ ...row, kind: "variable" as const })),
      ...secrets.filter((row) => !removed.has(row.key)).map((row) => ({ ...row, kind: "secret" as const })),
    ];
    for (const key of removed) owners.set(key, "removed");
    for (const row of activeRows) {
      const error: RowError = {};
      if (!row.key) error.key = "Enter a name.";
      else if (row.key.length > maxKeyLength || !portableEnvironmentName.test(row.key)) error.key = "Use letters, numbers, and underscores; start with a letter or underscore.";
      else if (owners.has(row.key)) {
        error.key = owners.get(row.key) === "removed" ? "This name is already scheduled for removal." : "Each variable and secret name must be unique.";
        const previous = owners.get(row.key);
        if (previous && previous !== "removed") nextErrors[previous] = { ...nextErrors[previous], key: "Each variable and secret name must be unique." };
      } else owners.set(row.key, row.id);
      if (row.value.length > maxValueLength) error.value = "Use 8,192 characters or fewer.";
      else if (row.kind === "secret" && !row.stored && !row.value) error.value = "Enter a secret value.";
      if (error.key || error.value) nextErrors[row.id] = { ...nextErrors[row.id], ...error };
    }
    setRowErrors(nextErrors);
    if (Object.keys(nextErrors).length === 0) return true;
    setClientError("Check the highlighted configuration fields.");
    window.setTimeout(() => errorSummary.current?.focus(), 0);
    return false;
  };

  const save = (event: React.FormEvent) => {
    event.preventDefault();
    if (saving.current || mutation.isPending) return;
    setClientError("");
    setMessage("");
    setAnnouncement("");
    mutation.reset();
    if (!validate()) return;
    saving.current = true;
    mutation.mutate();
  };

  const reload = async () => {
    if (!window.confirm("Discard your unsaved edits and load the latest configuration from this controller?")) return;
    const result = await query.refetch();
    if (result.isError || !result.data) {
      setClientError("Could not load the latest configuration. Your edits are still here.");
      window.setTimeout(() => errorSummary.current?.focus(), 0);
      return;
    }
    const focusTarget = hydrate(result.data);
    mutation.reset();
    setMessage("Latest configuration loaded. Your previous edits were discarded.");
    setPendingFocus(focusTarget);
  };

  const updateVariable = (id: string, field: "key" | "value", value: string) => {
    if (saving.current) return;
    mutation.reset();
    setVariables((rows) => rows.map((row) => row.id === id ? { ...row, [field]: value } : row));
    clearRowError(id, field);
    markDirty();
  };
  const updateSecret = (id: string, field: "key" | "value", value: string) => {
    if (saving.current) return;
    mutation.reset();
    setSecrets((rows) => rows.map((row) => row.id === id ? { ...row, [field]: value } : row));
    if (field === "value" && value === "") setRevealed((ids) => { const next = new Set(ids); next.delete(id); return next; });
    clearRowError(id, field);
    markDirty();
  };
  const focusAfterDelete = (rows: Array<VariableRow | SecretRow>, index: number, fallback: string, storedKeysDisabled = false) => {
    const adjacent: Array<VariableRow | SecretRow> = [];
    for (let offset = 1; offset < rows.length; offset++) {
      if (rows[index + offset]) adjacent.push(rows[index + offset]);
      if (rows[index - offset]) adjacent.push(rows[index - offset]);
    }
    const active = adjacent.find((row) => !removed.has(row.key) && !(storedKeysDisabled && row.stored));
    if (active) return `${active.id}-key`;
    const staged = adjacent.find((row) => row.stored && removed.has(row.key));
    return staged ? `${staged.id}-undo` : fallback;
  };
  const stageRemoval = (row: VariableRow | SecretRow, kind: "Variable" | "Secret") => {
    if (saving.current) return;
    mutation.reset();
    setRemoved((keys) => new Set(keys).add(row.key));
    setRowErrors((current) => { const next = { ...current }; delete next[row.id]; return next; });
    markDirty(`${kind} ${row.key} scheduled for removal.`);
    setPendingFocus(`${row.id}-undo`);
  };
  const undoRemoval = (row: VariableRow | SecretRow, kind: "variable" | "secret") => {
    if (saving.current) return;
    mutation.reset();
    setRemoved((keys) => { const next = new Set(keys); next.delete(row.key); return next; });
    markDirty(`${kind === "variable" ? "Variable" : "Secret"} ${row.key} will be kept.`);
    setPendingFocus(`${row.id}-remove`);
  };

  if (query.isLoading) return <section className="configuration-panel" aria-labelledby="configuration-title" aria-busy="true"><h2 id="configuration-title">Configuration</h2><p role="status">Loading configuration…</p><button className="button primary" disabled>Save configuration</button></section>;
  if (query.isError && !query.data) return <section className="configuration-panel" aria-labelledby="configuration-title"><h2 id="configuration-title">Configuration</h2><div className="callout danger" role="alert"><strong>Configuration could not be loaded.</strong><span>{query.error.message}</span></div><button className="button" onClick={() => query.refetch()}>Try again</button></section>;

  const error = clientError || mutation.error?.message || "";
  const apiErrors = mutation.error instanceof APIError ? mutation.error.errors : {};
  const conflict = mutation.error instanceof APIError && mutation.error.code === "configuration_conflict";
  const busy = mutation.isPending;
  const statusMessage = busy ? "Saving configuration. Editing is temporarily unavailable." : announcement || message;
  const describedBy = (...ids: Array<string | undefined>) => ids.filter(Boolean).join(" ") || undefined;
  return <section className="configuration-panel" aria-labelledby="configuration-title">
    <div className="configuration-heading"><div><h2 id="configuration-title">Configuration</h2><p>Variables and secrets are stored in protected files on this controller. Stored secret values are never loaded into this page.</p></div><span className="configuration-revision">Revision {revision}</span></div>
    <form onSubmit={save} noValidate aria-busy={busy}>
      {error && <div className="error-summary" ref={errorSummary} tabIndex={-1} role="alert"><span>{error}</span>{conflict && <button type="button" className="button small" disabled={busy} onClick={reload}>Discard edits and load latest</button>}</div>}
      {apiErrors.configuration && <p className="form-error" role="alert">{apiErrors.configuration}</p>}
      {apiErrors.remove && <p id="configuration-remove-error" className="form-error" role="alert">{apiErrors.remove}</p>}
      <div className="configuration-group">
        <div className="configuration-group-heading"><div><h3>Variables</h3><p>Replacing the configuration removes variables not listed here.</p></div><button id="configuration-add-variable" type="button" className="button small" disabled={busy} onClick={() => { if (saving.current) return; mutation.reset(); const id = createID("variable"); setVariables((rows) => [...rows, { id, key: "", value: "", stored: false }]); markDirty(); setPendingFocus(`${id}-key`); }}>Add variable</button></div>
        {apiErrors.variables && <p id="configuration-variables-error" className="form-error" role="alert">{apiErrors.variables}</p>}
        {variables.length === 0 ? <p className="configuration-empty">No variables configured.</p> : <div className="configuration-rows">{variables.map((row, index) => {
          const isRemoved = row.stored && removed.has(row.key);
          const keyError = rowErrors[row.id]?.key;
          const valueError = rowErrors[row.id]?.value;
          return <fieldset className={`configuration-row${isRemoved ? " removed" : ""}`} key={row.id} aria-describedby={describedBy(apiErrors.variables ? "configuration-variables-error" : undefined, isRemoved && apiErrors.remove ? "configuration-remove-error" : undefined)}>
            <legend className="configuration-row-title">Variable {row.key || index + 1}</legend>
            <div className="configuration-row-fields">
              <div className="field"><label htmlFor={`${row.id}-key`}>Variable name <span aria-hidden="true">*</span></label><input id={`${row.id}-key`} value={row.key} required maxLength={maxKeyLength} disabled={isRemoved || busy} aria-invalid={Boolean(keyError)} aria-describedby={describedBy(keyError ? `${row.id}-key-error` : undefined, apiErrors.variables ? "configuration-variables-error" : undefined)} onChange={(event) => updateVariable(row.id, "key", event.target.value)} autoComplete="off"/>{keyError && <span id={`${row.id}-key-error`} className="form-error" role="alert">{keyError}</span>}</div>
              <div className="field"><label htmlFor={`${row.id}-value`}>Value</label><input id={`${row.id}-value`} value={row.value} maxLength={maxValueLength} disabled={isRemoved || busy} aria-invalid={Boolean(valueError)} aria-describedby={valueError ? `${row.id}-value-error` : undefined} onChange={(event) => updateVariable(row.id, "value", event.target.value)} autoComplete="off"/>{valueError && <span id={`${row.id}-value-error`} className="form-error" role="alert">{valueError}</span>}</div>
              {isRemoved ? <button id={`${row.id}-undo`} type="button" className="button small configuration-remove" disabled={busy} aria-label={`Undo removal of variable ${row.key}`} onClick={() => undoRemoval(row, "variable")}>Undo removal</button> : <button id={`${row.id}-remove`} type="button" className="button small configuration-remove" disabled={busy} aria-label={`Remove variable ${row.key || index + 1}`} onClick={() => { if (row.stored) stageRemoval(row, "Variable"); else { const target = focusAfterDelete(variables, index, "configuration-add-variable"); setVariables((rows) => rows.filter((item) => item.id !== row.id)); setRowErrors((current) => { const next = { ...current }; delete next[row.id]; return next; }); markDirty(`Variable row ${index + 1} removed.`); setPendingFocus(target); } }}>Remove</button>}
            </div>
          </fieldset>;
        })}</div>}
      </div>

      <div className="configuration-group">
        <div className="configuration-group-heading"><div><h3>Secrets</h3><p>Leave a stored replacement blank to preserve it. Enter a value only to replace it.</p></div><button id="configuration-add-secret" type="button" className="button small" disabled={busy} onClick={() => { if (saving.current) return; mutation.reset(); const id = createID("secret"); setSecrets((rows) => [...rows, { id, key: "", value: "", stored: false }]); markDirty(); setPendingFocus(`${id}-key`); }}>Add secret</button></div>
        {apiErrors.secrets && <p id="configuration-secrets-error" className="form-error" role="alert">{apiErrors.secrets}</p>}
        {secrets.length === 0 ? <p className="configuration-empty">No secrets configured.</p> : <div className="configuration-rows">{secrets.map((row, index) => {
          const isRemoved = row.stored && removed.has(row.key);
          const keyError = rowErrors[row.id]?.key;
          const valueError = rowErrors[row.id]?.value;
          const storedDescription = row.stored && !isRemoved ? `${row.id}-stored` : undefined;
          return <fieldset className={`configuration-row${isRemoved ? " removed" : ""}`} key={row.id} aria-describedby={describedBy(apiErrors.secrets ? "configuration-secrets-error" : undefined, apiErrors.remove ? "configuration-remove-error" : undefined)}>
            <legend className="configuration-row-title">Secret {row.key || index + 1}</legend>
            <div className="configuration-row-fields">
              <div className="field"><label htmlFor={`${row.id}-key`}>Secret name <span aria-hidden="true">*</span></label><input id={`${row.id}-key`} value={row.key} required maxLength={maxKeyLength} disabled={row.stored || busy} aria-invalid={Boolean(keyError)} aria-describedby={describedBy(keyError ? `${row.id}-key-error` : undefined, apiErrors.secrets ? "configuration-secrets-error" : undefined)} onChange={(event) => updateSecret(row.id, "key", event.target.value)} autoComplete="off"/>{keyError && <span id={`${row.id}-key-error`} className="form-error" role="alert">{keyError}</span>}</div>
              <div className="field"><label htmlFor={`${row.id}-value`}>{row.stored ? "Replacement value" : "Secret value"}{!row.stored && <span aria-hidden="true"> *</span>}</label><input id={`${row.id}-value`} type={revealed.has(row.id) ? "text" : "password"} value={row.value} required={!row.stored} maxLength={maxValueLength} disabled={isRemoved || busy} placeholder={row.stored ? "Stored — leave blank to preserve" : ""} aria-invalid={Boolean(valueError)} aria-describedby={describedBy(storedDescription, valueError ? `${row.id}-value-error` : undefined, apiErrors.secrets ? "configuration-secrets-error" : undefined)} onChange={(event) => updateSecret(row.id, "value", event.target.value)} autoComplete="new-password"/>{row.stored && !isRemoved && <span id={`${row.id}-stored`} className="stored-secret">Stored on this controller</span>}{valueError && <span id={`${row.id}-value-error`} className="form-error" role="alert">{valueError}</span>}</div>
              <div className="configuration-row-actions">
                {row.value && !isRemoved && <button type="button" className="button small" disabled={busy} aria-label={`${revealed.has(row.id) ? "Hide value" : "Show value"} for secret ${row.key || index + 1}`} onClick={() => { if (saving.current) return; setRevealed((ids) => { const next = new Set(ids); if (next.has(row.id)) next.delete(row.id); else next.add(row.id); return next; }); }}>{revealed.has(row.id) ? "Hide value" : "Show value"}</button>}
                {isRemoved ? <button id={`${row.id}-undo`} type="button" className="button small configuration-remove" disabled={busy} aria-label={`Undo removal of secret ${row.key}`} onClick={() => undoRemoval(row, "secret")}>Undo removal</button> : <button id={`${row.id}-remove`} type="button" className="button small configuration-remove" disabled={busy} aria-label={`Remove secret ${row.key || index + 1}`} onClick={() => { if (row.stored) stageRemoval(row, "Secret"); else { const target = focusAfterDelete(secrets, index, "configuration-add-secret", true); setSecrets((rows) => rows.filter((item) => item.id !== row.id)); setRevealed((ids) => { const next = new Set(ids); next.delete(row.id); return next; }); setRowErrors((current) => { const next = { ...current }; delete next[row.id]; return next; }); markDirty(`Secret row ${index + 1} removed.`); setPendingFocus(target); } }}>Remove</button>}
              </div>
            </div>
          </fieldset>;
        })}</div>}
      </div>
      <div className="configuration-footer"><span aria-live="polite" aria-atomic="true" role="status">{statusMessage}</span><button className="button primary" disabled={busy || !dirty}>{busy ? "Saving…" : "Save configuration"}</button></div>
    </form>
  </section>;
}
