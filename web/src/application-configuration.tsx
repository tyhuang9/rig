import { useEffect, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { APIError, api, type ApplicationConfiguration } from "./api";

type VariableRow = { id: string; key: string; value: string };
type SecretRow = { id: string; key: string; value: string; stored: boolean };

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
  const [dirty, setDirty] = useState(false);
  const [message, setMessage] = useState("");
  const [clientError, setClientError] = useState("");
  const [pendingFocus, setPendingFocus] = useState("");
  const nextID = useRef(0);
  const saving = useRef(false);
  const errorSummary = useRef<HTMLDivElement>(null);

  const createID = (kind: string) => `configuration-${kind}-${++nextID.current}`;
  const markDirty = () => { if (saving.current) return; setDirty(true); setMessage(""); };
  const hydrate = (configuration: ApplicationConfiguration) => {
    setRevision(configuration.revisionNumber);
    setVariables(configuration.entries.filter((entry) => !entry.sensitive).map((entry) => ({ id: createID("variable"), key: entry.key, value: entry.value ?? "" })));
    setSecrets(configuration.entries.filter((entry) => entry.sensitive).map((entry) => ({ id: createID("secret"), key: entry.key, value: "", stored: true })));
    setRemoved(new Set());
    setDirty(false);
    setClientError("");
  };

  useEffect(() => {
    if (query.data && !dirty) hydrate(query.data);
    // Dirty edits intentionally survive background query refreshes.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [query.data]);

  useEffect(() => {
    if (!pendingFocus) return;
    document.getElementById(pendingFocus)?.focus();
    setPendingFocus("");
  }, [pendingFocus, variables, secrets]);

  const mutation = useMutation({
    mutationFn: () => api.replaceApplicationConfiguration(appId, {
      expectedRevisionNumber: revision,
      variables: variables.map(({ key, value }) => ({ key, value })),
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

  const save = (event: React.FormEvent) => {
    event.preventDefault();
    if (saving.current || mutation.isPending) return;
    setClientError("");
    setMessage("");
    const incompleteSecret = secrets.find((secret) => !secret.stored && !removed.has(secret.key) && secret.value === "");
    if (incompleteSecret) {
      setClientError("Enter a value for each new secret, or remove the incomplete row.");
      window.setTimeout(() => errorSummary.current?.focus(), 0);
      return;
    }
    saving.current = true;
    mutation.mutate();
  };

  const reload = async () => {
    const result = await query.refetch();
    if (result.isError || !result.data) {
      setClientError("Could not reload the latest configuration. Your edits are still here.");
      window.setTimeout(() => errorSummary.current?.focus(), 0);
      return;
    }
    hydrate(result.data);
    mutation.reset();
    setMessage("Latest configuration loaded. Your previous edits were discarded.");
  };

  const updateVariable = (id: string, field: "key" | "value", value: string) => {
    if (saving.current) return;
    setVariables((rows) => rows.map((row) => row.id === id ? { ...row, [field]: value } : row));
    markDirty();
  };
  const updateSecret = (id: string, field: "key" | "value", value: string) => {
    if (saving.current) return;
    setSecrets((rows) => rows.map((row) => row.id === id ? { ...row, [field]: value } : row));
    markDirty();
  };

  if (query.isLoading) return <section className="configuration-panel" aria-labelledby="configuration-title" aria-busy="true"><h2 id="configuration-title">Configuration</h2><p role="status">Loading configuration…</p><button className="button primary" disabled>Save configuration</button></section>;
  if (query.isError && !query.data) return <section className="configuration-panel" aria-labelledby="configuration-title"><h2 id="configuration-title">Configuration</h2><div className="callout danger" role="alert"><strong>Configuration could not be loaded.</strong><span>{query.error.message}</span></div><button className="button" onClick={() => query.refetch()}>Try again</button></section>;

  const error = clientError || mutation.error?.message || "";
  const conflict = mutation.error instanceof APIError && mutation.error.code === "configuration_conflict";
  const busy = mutation.isPending;
  return <section className="configuration-panel" aria-labelledby="configuration-title">
    <div className="configuration-heading"><div><h2 id="configuration-title">Configuration</h2><p>Variables are visible. Secret values are stored locally and never loaded into this page.</p></div><span className="configuration-revision">Revision {revision}</span></div>
    <form onSubmit={save} noValidate>
      {error && <div className="error-summary" ref={errorSummary} tabIndex={-1} role="alert"><span>{error}</span>{conflict && <button type="button" className="button small" disabled={busy} onClick={reload}>Reload latest configuration</button>}</div>}
      <div className="configuration-group">
        <div className="configuration-group-heading"><div><h3>Variables</h3><p>Replacing the configuration removes variables not listed here.</p></div><button type="button" className="button small" disabled={busy} onClick={() => { if (saving.current) return; const id = createID("variable"); setVariables((rows) => [...rows, { id, key: "", value: "" }]); markDirty(); setPendingFocus(`${id}-key`); }}>Add variable</button></div>
        {variables.length === 0 ? <p className="configuration-empty">No variables configured.</p> : <div className="configuration-rows">{variables.map((row) => <div className="configuration-row" key={row.id}>
          <div className="field"><label htmlFor={`${row.id}-key`}>Variable name</label><input id={`${row.id}-key`} value={row.key} disabled={busy} onChange={(event) => updateVariable(row.id, "key", event.target.value)} autoComplete="off" /></div>
          <div className="field"><label htmlFor={`${row.id}-value`}>Value</label><input id={`${row.id}-value`} value={row.value} disabled={busy} onChange={(event) => updateVariable(row.id, "value", event.target.value)} autoComplete="off" /></div>
          <button type="button" className="button small configuration-remove" disabled={busy} aria-label={`Remove variable ${row.key || "row"}`} onClick={() => { if (saving.current) return; setVariables((rows) => rows.filter((item) => item.id !== row.id)); markDirty(); }}>Remove</button>
        </div>)}</div>}
      </div>

      <div className="configuration-group">
        <div className="configuration-group-heading"><div><h3>Secrets</h3><p>Leave a stored secret blank to preserve it. Enter a value only to replace it.</p></div><button type="button" className="button small" disabled={busy} onClick={() => { if (saving.current) return; const id = createID("secret"); setSecrets((rows) => [...rows, { id, key: "", value: "", stored: false }]); markDirty(); setPendingFocus(`${id}-key`); }}>Add secret</button></div>
        {secrets.length === 0 ? <p className="configuration-empty">No secrets configured.</p> : <div className="configuration-rows">{secrets.map((row) => {
          const isRemoved = row.stored && removed.has(row.key);
          return <div className={`configuration-row${isRemoved ? " removed" : ""}`} key={row.id}>
            <div className="field"><label htmlFor={`${row.id}-key`}>Secret name</label><input id={`${row.id}-key`} value={row.key} disabled={row.stored || busy} onChange={(event) => updateSecret(row.id, "key", event.target.value)} autoComplete="off" /></div>
            <div className="field"><label htmlFor={`${row.id}-value`}>{row.stored ? "Replacement value" : "Secret value"}</label><input id={`${row.id}-value`} type="password" value={row.value} disabled={isRemoved || busy} placeholder={row.stored ? "Stored — leave blank to preserve" : ""} aria-describedby={row.stored && !isRemoved ? `${row.id}-stored` : undefined} onChange={(event) => updateSecret(row.id, "value", event.target.value)} autoComplete="new-password" />{row.stored && !isRemoved && <span id={`${row.id}-stored`} className="stored-secret">Stored</span>}</div>
            {isRemoved ? <button type="button" className="button small configuration-remove" disabled={busy} onClick={() => { if (saving.current) return; setRemoved((keys) => { const next = new Set(keys); next.delete(row.key); return next; }); markDirty(); }}>Undo remove</button> : <button type="button" className="button small configuration-remove" disabled={busy} aria-label={`Remove secret ${row.key || "row"}`} onClick={() => { if (saving.current) return; if (row.stored) setRemoved((keys) => new Set(keys).add(row.key)); else setSecrets((rows) => rows.filter((item) => item.id !== row.id)); markDirty(); }}>Remove</button>}
          </div>;
        })}</div>}
      </div>
      <div className="configuration-footer"><span aria-live="polite" role="status">{message}</span><button className="button primary" disabled={busy || !dirty}>{busy ? "Saving…" : "Save configuration"}</button></div>
    </form>
  </section>;
}
