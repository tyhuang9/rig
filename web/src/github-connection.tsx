import { useEffect, useId, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { APIError, api, type ConnectedGitHubRepository, type GitHubConnectionAuthorization } from "./api";
import { Dialog } from "./dialog";

export const githubConnectionKey = ["default-source-connection"];
export const githubRepositoriesKey = ["default-github-repositories"];
const authorizationStorageKey = "rig-github-authorization";

function message(error: unknown, fallback: string) {
  if (!(error instanceof APIError)) return fallback;
  switch (error.code) {
    case "source_access_lost": return "GitHub access needs to be renewed. Reconnect your GitHub account.";
    case "authorization_denied": return "GitHub authorization was denied. Connect again when you are ready.";
    case "authorization_expired": return "GitHub authorization expired. Connect again to receive a new code.";
    case "authorization_identity_mismatch": return "Sign in with the GitHub account already connected to Rig.";
    case "authorization_superseded": return "Another GitHub authorization replaced this attempt. Reconnect to begin again.";
    case "authorization_failed": return "GitHub authorization could not be completed. Reconnect to try again.";
    case "rate_limited": return "GitHub is temporarily rate limiting requests. Try again shortly.";
    case "provider_unavailable": return "GitHub is temporarily unavailable. Your saved connection is retained.";
    default: return fallback;
  }
}

function githubURL(value: string | undefined, kind: "device" | "install") {
  if (!value) return undefined;
  try {
    const url = new URL(value);
    if (url.origin !== "https://github.com" || url.username || url.password || url.hash || url.search) return undefined;
    if (kind === "device" ? url.pathname !== "/login/device" : !/^\/apps\/[a-zA-Z0-9-]+\/installations\/new$/.test(url.pathname)) return undefined;
    return url.href;
  } catch { return undefined; }
}

export function GitHubConnectionCard() {
  const queryClient = useQueryClient();
  const capability = useQuery({ queryKey: ["system-status"], queryFn: api.status, retry: false });
  const enabled = capability.data?.capabilities.githubConnections === true;
  const connection = useQuery({ queryKey: githubConnectionKey, queryFn: api.defaultSourceConnection, retry: false });
  const [authorization, setAuthorization] = useState<GitHubConnectionAuthorization | null>(null);
  const [busy, setBusy] = useState(false);
  const [paused, setPaused] = useState(false);
  const [error, setError] = useState("");
  const [disconnectOpen, setDisconnectOpen] = useState(false);
  const [connectedNow, setConnectedNow] = useState(false);
  const [nextPollAt, setNextPollAt] = useState(0);
  const epoch = useRef(0);
  const mounted = useRef(true);
  const inFlight = useRef(false);
  const startButton = useRef<HTMLButtonElement>(null);
  const retryConnectionButton = useRef<HTMLButtonElement>(null);
  const verificationLink = useRef<HTMLAnchorElement>(null);
  const manageLink = useRef<HTMLAnchorElement>(null);
  const statusRef = useRef<HTMLDivElement>(null);
  const focusNext = useRef<"verify" | "manage" | "start" | null>(null);
  const restored = useRef(false);
  const id = useId();
  const current = connection.data?.connection;
  const connected = current?.status === "connected";
  const installURL = githubURL(current?.installUrl ?? authorization?.installUrl, "install");
  const verificationURL = githubURL(authorization?.verificationUri, "device");

  useEffect(() => { mounted.current = true; return () => { mounted.current = false; epoch.current += 1; }; }, []);
  useEffect(() => {
    if (restored.current || !connection.data) return;
    restored.current = true;
    try {
      const stored = JSON.parse(window.sessionStorage.getItem(authorizationStorageKey) ?? "null") as GitHubConnectionAuthorization | null;
      if (!stored) return;
      if (stored.connectionId !== connection.data.connection?.id || typeof stored.authorizationId !== "string" || typeof stored.userCode !== "string" || !githubURL(stored.verificationUri, "device") || !githubURL(stored.installUrl, "install") || !Number.isFinite(Date.parse(stored.expiresAt)) || !Number.isFinite(stored.pollIntervalSeconds) || stored.pollIntervalSeconds < 1) {
        window.sessionStorage.removeItem(authorizationStorageKey); return;
      }
      setAuthorization(stored);
      setNextPollAt(Date.now() + stored.pollIntervalSeconds * 1000);
    } catch { window.sessionStorage.removeItem(authorizationStorageKey); }
  }, [connection.data]);
  useEffect(() => {
    if (!focusNext.current) return;
    const target = focusNext.current === "verify" ? verificationLink.current
      : focusNext.current === "manage" || connected ? manageLink.current ?? statusRef.current
      : connection.isError ? retryConnectionButton.current : startButton.current;
    if (!target || (target instanceof HTMLButtonElement && target.disabled)) return;
    target.focus();
    if (document.activeElement === target) focusNext.current = null;
  });

  const invalidate = async () => {
    await queryClient.invalidateQueries({ queryKey: githubConnectionKey });
    await queryClient.invalidateQueries({ queryKey: githubRepositoriesKey });
  };
  const begin = async () => {
    if (inFlight.current) return;
    inFlight.current = true;
    const generation = ++epoch.current;
    setBusy(true); setError(""); setPaused(false); setConnectedNow(false); setAuthorization(null);
    window.sessionStorage.removeItem(authorizationStorageKey);
    try {
      const started = await api.startDefaultGitHubConnection();
      if (!mounted.current || generation !== epoch.current) return;
      if (!githubURL(started.verificationUri, "device") || !githubURL(started.installUrl, "install") || !Number.isFinite(Date.parse(started.expiresAt)) || !Number.isFinite(started.pollIntervalSeconds) || started.pollIntervalSeconds < 1) throw new Error("Invalid authorization response");
      setAuthorization(started);
      window.sessionStorage.setItem(authorizationStorageKey, JSON.stringify(started));
      setNextPollAt(Date.now() + started.pollIntervalSeconds * 1000);
      focusNext.current = "verify";
      await queryClient.invalidateQueries({ queryKey: githubConnectionKey });
    } catch (failure) {
      if (mounted.current && generation === epoch.current) { setError(message(failure, "Could not start GitHub authorization. Try again.")); setAuthorization(null); }
    } finally {
      if (mounted.current && generation === epoch.current) { inFlight.current = false; setBusy(false); }
    }
  };

  useEffect(() => {
    if (!authorization || paused || !enabled) return;
    let cancelled = false;
    const generation = epoch.current;
    const expiresAt = Date.parse(authorization.expiresAt);
    const delay = Math.max(0, Math.min(nextPollAt, expiresAt) - Date.now());
    const timer = window.setTimeout(async () => {
      try {
        const result = await api.pollDefaultGitHubConnection(authorization.connectionId, authorization.authorizationId);
        if (cancelled || generation !== epoch.current) return;
        if (result.authorizationId !== authorization.authorizationId || result.connection.id !== authorization.connectionId) throw new Error("Authorization response does not match the active attempt");
        if (result.status === "connected") {
          const ownsFocus = document.activeElement === verificationLink.current || statusRef.current?.contains(document.activeElement);
          queryClient.setQueryData(githubConnectionKey, { configured: true, connection: result.connection });
          window.sessionStorage.removeItem(authorizationStorageKey);
          setConnectedNow(true); setAuthorization(null); setError("");
          if (ownsFocus) focusNext.current = "manage";
          await invalidate();
        } else if (result.status === "pending" && Date.now() < expiresAt) {
          const serverNext = result.nextPollAt ? Date.parse(result.nextPollAt) : 0;
          setNextPollAt(Math.max(Date.now() + authorization.pollIntervalSeconds * 1000, Number.isFinite(serverNext) ? serverNext : 0));
        } else {
          if (statusRef.current?.contains(document.activeElement)) focusNext.current = "start";
          window.sessionStorage.removeItem(authorizationStorageKey);
          setAuthorization(null); setError(result.status === "denied" ? "GitHub authorization was denied. Connect again when you are ready." : result.status === "superseded" ? "Another GitHub authorization replaced this attempt. Reconnect to begin again." : result.status === "failed" ? "GitHub authorization could not be completed. Reconnect to try again." : "GitHub authorization expired. Connect again to receive a new code.");
          await invalidate();
        }
      } catch (failure) {
        if (cancelled || generation !== epoch.current) return;
        if (failure instanceof APIError && failure.code === "poll_too_soon" && Date.now() < expiresAt) {
          setNextPollAt(Date.now() + Math.max(1, failure.retryAfterSeconds ?? authorization.pollIntervalSeconds) * 1000);
        } else if (failure instanceof APIError && ["authorization_expired", "authorization_denied", "authorization_identity_mismatch", "authorization_superseded", "authorization_failed"].includes(failure.code)) {
          if (statusRef.current?.contains(document.activeElement)) focusNext.current = "start";
          window.sessionStorage.removeItem(authorizationStorageKey);
          setAuthorization(null); setError(message(failure, "GitHub authorization could not be completed."));
          await invalidate();
        } else {
          setPaused(true); setError(message(failure, "Could not check GitHub authorization. Retry to continue this authorization."));
        }
      }
    }, delay);
    return () => { cancelled = true; window.clearTimeout(timer); };
  }, [authorization, paused, nextPollAt, queryClient, enabled]);

  const disconnect = async () => {
    if (inFlight.current || !current) return;
    inFlight.current = true;
    const generation = ++epoch.current;
    setBusy(true); setError(""); setAuthorization(null);
    window.sessionStorage.removeItem(authorizationStorageKey);
    try {
      await api.disconnectSourceConnection(current.id);
      if (!mounted.current || generation !== epoch.current) return;
      await invalidate();
      if (!mounted.current || generation !== epoch.current) return;
      setDisconnectOpen(false); setConnectedNow(false); focusNext.current = "start";
    } catch (failure) {
      if (mounted.current && generation === epoch.current) setError(message(failure, "Could not disconnect GitHub. Try again."));
    } finally {
      if (mounted.current && generation === epoch.current) { inFlight.current = false; setBusy(false); }
    }
  };

  return <section className="github-connection-card" aria-labelledby={`${id}-title`}>
    <div className="connector-heading"><div><h3 id={`${id}-title`}>GitHub</h3><p>Connect once to reuse your repositories across applications on this controller.</p></div>{current?.providerLogin && <strong>@{current.providerLogin}</strong>}</div>
    <div ref={statusRef} tabIndex={-1} className="wizard-status connection-status" role="status" aria-live="polite" aria-atomic="true">
      {connection.isLoading ? "Loading GitHub connection…" : connection.isError ? "GitHub connection could not be loaded." : authorization ? <><strong>Step 1 of 2: Authorize GitHub</strong><span>Enter <code>{authorization.userCode}</code> at GitHub. Rig will check when authorization is complete.</span>{verificationURL && <a ref={verificationLink} className="button primary" href={verificationURL} target="_blank" rel="noopener noreferrer">Authorize GitHub (opens in a new tab)</a>}</> : connected ? <><strong>Connected{current?.providerLogin ? ` as @${current.providerLogin}` : " to GitHub"}</strong>{connectedNow && <span>Step 2 of 2: Choose repository access. Grant Rig access to the personal or organization repositories you want to deploy.</span>}</> : <><strong>{current?.status === "access_lost" ? "Reconnect GitHub" : "GitHub is not connected"}</strong><span>{current?.status === "access_lost" ? "Renew permission for this account to restore its existing applications." : "Authorize your GitHub account, then choose which repositories Rig can access."}</span></>}
    </div>
    {error && !disconnectOpen && <div className="callout danger" role="alert">{error}</div>}
    {capability.isError && <div className="callout danger" role="alert"><span>Could not check whether GitHub connections are enabled.</span><button className="button small" type="button" onClick={() => void capability.refetch()}>Retry capability check</button></div>}
    {capability.data && !enabled && <p className="callout warning" role="status">GitHub connections are disabled on this controller.</p>}
    {connection.isError ? <button ref={retryConnectionButton} className="button" type="button" onClick={() => void connection.refetch()}>Retry connection</button> : <div className="connection-actions">
      {!authorization && !connected && <button ref={startButton} className="button primary" type="button" disabled={busy || connection.isLoading || !enabled} onClick={() => void begin()}>{busy ? "Working…" : current?.status === "access_lost" ? "Reconnect" : "Connect GitHub"}</button>}
      {authorization && paused && <><button className="button" type="button" disabled={!enabled} onClick={() => { statusRef.current?.focus(); setError(""); setPaused(false); setNextPollAt(Date.now()); }}>Retry authorization check</button><button className="button" type="button" disabled={!enabled || busy} onClick={() => void begin()}>Start new authorization</button></>}
      {installURL && connected && <a ref={manageLink} className="button" href={installURL} target="_blank" rel="noopener noreferrer">Manage repository access (opens in a new tab)</a>}
      {connected && <button className="button" type="button" disabled={busy || authorization !== null} onClick={() => void invalidate()}>Refresh repositories</button>}
      {current && current.status !== "disconnected" && <button className="button" type="button" disabled={busy || authorization !== null} onClick={() => setDisconnectOpen(true)}>Disconnect</button>}
    </div>}
    {disconnectOpen && <Dialog title="Disconnect GitHub?" pending={busy} close={() => { if (!busy) setDisconnectOpen(false); }}><p>Applications remain saved. Future GitHub deployments and automatic deployment checks will pause until you reconnect.</p>{error && <p role="alert">{error}</p>}<div className="dialog-actions"><button className="button" type="button" disabled={busy} onClick={() => setDisconnectOpen(false)}>Keep connected</button><button className="button danger" type="button" disabled={busy} onClick={() => void disconnect()}>{busy ? "Disconnecting…" : "Disconnect GitHub"}</button></div></Dialog>}
  </section>;
}

export function GitHubRepositoryPicker({ id, value, onChange, disabled = false, client = api }: { id: string; value: ConnectedGitHubRepository | null; onChange: (value: ConnectedGitHubRepository | null) => void; disabled?: boolean; client?: Pick<typeof api, "defaultGitHubRepositories"> }) {
  const [search, setSearch] = useState("");
  const [query, setQuery] = useState("");
  const [page, setPage] = useState(1);
  const repositories = useQuery({ queryKey: [...githubRepositoriesKey, query, page], queryFn: () => client.defaultGitHubRepositories(query, page, 30), retry: false });
  const items = repositories.data?.items.filter((item) => !item.archived && !item.disabled) ?? [];
  const busy = repositories.isFetching;
  const failed = repositories.isError;
  const hasNext = (repositories.data?.page ?? 1) * (repositories.data?.perPage ?? 30) < (repositories.data?.totalCount ?? 0);
  const key = (item: ConnectedGitHubRepository) => `${item.connectionId}:${item.installationId}:${item.id}`;
  const changePage = (next: number) => { onChange(null); setPage(next); };
  const applySearch = () => {
    if (disabled || busy) return;
    onChange(null); setPage(1); setQuery(search.trim());
  };
  return <div className="github-repository-picker" aria-busy={busy}>
    <div className="repository-search field"><label htmlFor={`${id}-search`}>Search repositories</label><div><input id={`${id}-search`} type="search" value={search} disabled={disabled} onChange={(event) => setSearch(event.target.value)} onKeyDown={(event) => { if (event.key === "Enter") { event.preventDefault(); applySearch(); } }}/><button className="button" type="button" disabled={disabled || busy} onClick={applySearch}>Search</button></div></div>
    <div className="field"><label htmlFor={id}>Repository</label><span id={`${id}-status`} className="sr-only" role="status" aria-live="polite" aria-atomic="true">{busy ? `Loading repositories page ${page}.` : failed ? "Repositories could not be loaded." : `Repositories page ${page} loaded. ${items.length} result${items.length === 1 ? "" : "s"}.`}</span><select id={id} aria-describedby={`${id}-status`} value={value ? key(value) : ""} disabled={disabled || busy || failed || items.length === 0} onChange={(event) => onChange(items.find((item) => key(item) === event.target.value) ?? null)}><option value="">{busy ? "Loading repositories…" : failed ? "Repositories unavailable" : "Choose a repository"}</option>{items.map((item) => <option key={key(item)} value={key(item)}>{item.owner}/{item.name}{item.private ? " (private)" : ""}</option>)}</select></div>
    {failed && <div className="callout danger" role="alert"><strong>Repositories unavailable</strong><span>{message(repositories.error, "Could not load repositories. Your saved connection is retained.")}</span><button className="button small" type="button" disabled={disabled} onClick={() => void repositories.refetch()}>Retry repositories</button></div>}
    {!busy && !failed && items.length === 0 && <div className="callout info" role="status"><strong>No repositories found</strong><span>{query ? "Try another search or update repository access using Manage repository access." : "Use Manage repository access to grant Rig access to your repositories, then retry."}</span><a className="button small" href="/connections" target="_blank" rel="noopener noreferrer">Manage repository access (opens in a new tab)</a><button className="button small" type="button" disabled={disabled} onClick={() => void repositories.refetch()}>Retry repositories</button></div>}
    {repositories.data?.truncated && !failed && <p className="callout warning" role="status">Some repositories could not be included. Narrow repository access or try a more specific search.</p>}
    {(page > 1 || hasNext || busy) && <nav className="wizard-pagination" aria-label="repositories pagination" aria-busy={busy}><button className="button small" type="button" aria-label="Previous repositories page" aria-disabled={disabled || busy || page === 1} onClick={() => { if (!disabled && !busy && page > 1) changePage(page - 1); }}>Previous</button><span>Page {page}</span><button className="button small" type="button" aria-label="Next repositories page" aria-disabled={disabled || busy || !hasNext} onClick={() => { if (!disabled && !busy && hasNext) changePage(page + 1); }}>Next</button></nav>}
  </div>;
}
