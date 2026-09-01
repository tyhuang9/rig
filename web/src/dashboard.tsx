import { Component, lazy, Suspense, useEffect, useMemo, useRef, useState, type ComponentType, type ReactNode } from "react";
import { NavLink, Route, Routes, useLocation, useNavigate, useParams } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod";
import {
  api,
  clearCSRF,
  setCSRF,
  type Application,
  type Job,
  type User,
} from "./api";
import { SourceWizard } from "./source-wizard";
import { ApplicationConfigurationPanel } from "./application-configuration";
import { AutoDeployPanel } from "./auto-deploy";
import { DeploymentHistoryPanel } from "./deployment-history";
import { UnsavedChangesGuard, useConfirmDiscard } from "./unsaved-changes";

type RelayPanelProps = { role: string };
type RelayPanelLoader = () => Promise<{ default: ComponentType<RelayPanelProps> }>;

const defaultRelayPanelLoader: RelayPanelLoader = async () => {
  const relayManagement = await import("./relay-management");
  return { default: relayManagement.RelayManagementPanel };
};

class RelayPanelErrorBoundary extends Component<{ children: ReactNode; fallback: ReactNode }, { failed: boolean }> {
  state = { failed: false };

  static getDerivedStateFromError() {
    return { failed: true };
  }

  render() {
    return this.state.failed ? this.props.fallback : this.props.children;
  }
}

function RelayPanelLoading() {
  return <section className="relay-panel" aria-label="GitHub deployment relay"><div className="relay-loading" role="status" aria-live="polite" aria-busy="true"><span className="sr-only">Loading relay management</span><i/><i/></div></section>;
}

function RelayPanelSlot({ role, loader }: RelayPanelProps & { loader: RelayPanelLoader }) {
  const [attempt, setAttempt] = useState(0);
  const retryInFlight = useRef(false);
  const LazyRelayManagementPanel = useMemo(() => lazy(loader), [attempt, loader]);
  useEffect(() => { retryInFlight.current = false; }, [attempt]);
  const retry = () => {
    if (retryInFlight.current) return;
    retryInFlight.current = true;
    setAttempt((current) => current + 1);
  };
  const errorFallback = <section className="relay-panel" aria-label="GitHub deployment relay"><div className="callout danger relay-chunk-error" role="alert"><strong>Relay management unavailable</strong><span>Relay management controls could not be loaded. Machine information remains available.</span><button className="button small" type="button" onClick={retry}>Retry relay management</button></div></section>;
  return <RelayPanelErrorBoundary key={attempt} fallback={errorFallback}><Suspense fallback={<RelayPanelLoading/>}><LazyRelayManagementPanel role={role}/></Suspense></RelayPanelErrorBoundary>;
}

type IconName = "apps" | "machines" | "activity" | "logout";

function Icon({ name }: { name: IconName }) {
  const paths: Record<IconName, React.ReactNode> = {
    apps: <><rect x="3" y="3" width="7" height="7"/><rect x="14" y="3" width="7" height="7"/><rect x="3" y="14" width="7" height="7"/><rect x="14" y="14" width="7" height="7"/></>,
    machines: <><rect x="3" y="4" width="18" height="6" rx="1"/><rect x="3" y="14" width="18" height="6" rx="1"/><path d="M7 7h.01M7 17h.01"/></>,
    activity: <><path d="M21 12a9 9 0 1 1-2.64-6.36"/><path d="M21 3v6h-6"/></>,
    logout: <><path d="M10 17l5-5-5-5M15 12H3"/><path d="M14 3h5a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2h-5"/></>,
  };
  return <svg className="nav-icon" aria-hidden="true" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round">{paths[name]}</svg>;
}

function StatusText({ value }: { value: string }) {
  return <span className={`status ${value.toLowerCase().replaceAll(" ", "-")}`}><i aria-hidden="true"/><span>{value}</span></span>;
}

function Layout({ user, onLogout, children }: { user: User; onLogout: () => void; children: React.ReactNode }) {
  const location = useLocation();
  const confirmDiscard = useConfirmDiscard();
  const routeName = location.pathname.startsWith("/machines") ? "Machines" : location.pathname.startsWith("/activity") ? "Activity" : location.pathname.startsWith("/apps/new") ? "Add application" : "Applications";
  return <div className="shell">
    <a className="skip" href="#main">Skip to content</a>
    <aside className="rail">
      <div className="wordmark"><b aria-hidden="true">h&gt;</b><strong>hostd</strong></div>
      <div className="system"><StatusText value="System ready"/></div>
      <nav aria-label="Primary navigation">
        <NavLink to="/apps" aria-label="Applications"><Icon name="apps"/><span>Applications</span></NavLink>
        <NavLink to="/machines" aria-label="Machines"><Icon name="machines"/><span>Machines</span></NavLink>
        <NavLink to="/activity" aria-label="Activity"><Icon name="activity"/><span>Activity</span></NavLink>
      </nav>
      <div className="rail-fill"/>
      <button className="rail-link" aria-label="Sign out" onClick={() => { if (confirmDiscard()) onLogout(); }}><Icon name="logout"/><span>Sign out</span></button>
      <div className="account"><b aria-hidden="true">{user.username.slice(0, 1).toUpperCase()}</b><span>{user.username}<small>Administrator</small></span></div>
    </aside>
    <span className="sr-only" role="status" aria-live="polite">{routeName} page</span>
    <main id="main" className="content">{children}</main>
  </div>;
}

function PageHeader({ title, subtitle, action }: { title: string; subtitle: string; action?: React.ReactNode }) {
  const heading = useRef<HTMLHeadingElement>(null);
  useEffect(() => {
    document.title = `${title} · hostd`;
    heading.current?.focus();
  }, [title]);
  return <header className="page-head">
    <div><h1 ref={heading} tabIndex={-1}>{title}</h1><p>{subtitle}</p></div>
    {action && <div className="actions">{action}</div>}
  </header>;
}

const loginSchema = z.object({
  token: z.string(),
  username: z.string().trim().min(1, "Enter your username"),
  passphrase: z.string().min(12, "Use at least 12 characters"),
});
type LoginFields = z.infer<typeof loginSchema>;

function Login({ setup, onAuthenticated }: { setup: boolean; onAuthenticated: (user: User) => void }) {
  const [bootstrapMode, setBootstrapMode] = useState(setup);
  const [serverError, setServerError] = useState("");
  const errorSummary = useRef<HTMLDivElement>(null);
  const { register, handleSubmit, formState: { errors, isSubmitting }, reset } = useForm<LoginFields>({
    resolver: zodResolver(loginSchema),
    defaultValues: { token: "", username: "", passphrase: "" },
    shouldFocusError: false,
  });
  const submit = async (values: LoginFields) => {
    setServerError("");
    if (bootstrapMode && !values.token.trim()) {
      setServerError("Enter the one-time bootstrap token.");
      window.setTimeout(() => errorSummary.current?.focus(), 0);
      return;
    }
    try {
      const response = bootstrapMode
        ? await api.bootstrap(values)
        : await api.login({ username: values.username, passphrase: values.passphrase });
      setCSRF(response.csrfToken);
      onAuthenticated(response.user);
    } catch (error) {
      setServerError(error instanceof Error ? error.message : "Unable to sign in");
      window.setTimeout(() => errorSummary.current?.focus(), 0);
    }
  };
  const invalid = () => window.setTimeout(() => errorSummary.current?.focus(), 0);
  const tokenError = serverError.startsWith("Enter the one-time") ? serverError : undefined;
  return <main className="auth">
    <section aria-labelledby="auth-title">
      <div className="auth-brand"><b aria-hidden="true">h&gt;</b><span>hostd</span></div>
      <h1 id="auth-title">{bootstrapMode ? "Set up hostd" : "Welcome back"}</h1>
      <p>{bootstrapMode ? "Create the first administrator using the one-time token from hostd's protected local console." : "Sign in to your local deployment manager."}</p>
      {(serverError || Object.keys(errors).length > 0) && <div className="error-summary" ref={errorSummary} tabIndex={-1} role="alert">{serverError || "Check the highlighted fields."}</div>}
      <form onSubmit={handleSubmit(submit, invalid)} noValidate>
        {bootstrapMode && <FormField label="Bootstrap token" id="token" error={tokenError} required><input id="token" required aria-invalid={Boolean(tokenError)} aria-describedby={tokenError ? "token-error" : undefined} autoComplete="off" {...register("token")}/></FormField>}
        <FormField label="Username" id="username" error={errors.username?.message} required><input id="username" required aria-invalid={Boolean(errors.username)} aria-describedby={errors.username ? "username-error" : undefined} autoComplete="username" {...register("username")}/></FormField>
        <FormField label="Passphrase" id="passphrase" error={errors.passphrase?.message} required><input id="passphrase" required type="password" aria-invalid={Boolean(errors.passphrase)} aria-describedby={errors.passphrase ? "passphrase-error" : undefined} autoComplete={bootstrapMode ? "new-password" : "current-password"} {...register("passphrase")}/></FormField>
        <button className="button primary" disabled={isSubmitting}>{isSubmitting ? "Working…" : bootstrapMode ? "Create administrator" : "Sign in"}</button>
      </form>
      {setup && <button className="text-button" onClick={() => { setBootstrapMode(!bootstrapMode); setServerError(""); reset(); }}>{bootstrapMode ? "I already have an account" : "Set up hostd"}</button>}
    </section>
  </main>;
}

function FormField({ label, id, error, required, children }: { label: string; id: string; error?: string; required?: boolean; children: React.ReactNode }) {
  return <div className="field"><label htmlFor={id}>{label}{required && <span aria-hidden="true"> *</span>}</label>{children}{error && <span id={`${id}-error`} className="form-error" role="alert">{error}</span>}</div>;
}

function LoadingState() {
  return <div className="skeletons" role="status" aria-live="polite" aria-busy="true"><span className="sr-only">Loading content</span><i/><i/><i/></div>;
}

function QueryError({ message }: { message: string }) {
  return <div className="callout danger" role="alert"><strong>Unable to load this page.</strong><span>{message}</span></div>;
}

function ApplicationsPage() {
  const query = useQuery({ queryKey: ["apps"], queryFn: api.apps });
  const items = query.data?.items ?? [];
  return <>
    <PageHeader title="Applications" subtitle="Deploy and manage apps on your machines." action={<NavLink className="button primary" to="/apps/new">Add application</NavLink>}/>
    {query.isLoading ? <LoadingState/> : query.isError ? <QueryError message={query.error.message}/> : items.length > 0 ? <div className="app-list">{items.map((app) => <ApplicationRow key={app.id} app={app}/>)}</div> : <section className="empty"><h2>No applications yet</h2><p>Save a local project draft. Runtime execution remains capability-gated.</p><NavLink className="button primary" to="/apps/new">Add application</NavLink></section>}
  </>;
}

function ApplicationRow({ app }: { app: Application }) {
  return <article className="app-row">
    <div className="app-primary"><div className="title-line"><strong title={app.name}>{app.name}</strong><StatusText value={app.status}/></div><span className="muted" title={app.description}>{app.description || "No description"}</span></div>
    <Meta label="Machine" value={app.machineName || "Local machine"}/>
    <Meta label="Release" value="View details" mono/>
    <Meta label="Created" value={new Date(app.createdAt).toLocaleDateString()}/>
    <NavLink className="button small app-open" aria-label={`Open ${app.name}`} to={`/apps/${app.id}`}>Open</NavLink>
  </article>;
}

function Meta({ label, value, mono = false }: { label: string; value: string; mono?: boolean }) {
  return <div className="meta"><small>{label}</small><span className={mono ? "mono" : undefined} title={value}>{value}</span></div>;
}

function AddApplicationPage() {
  const navigate = useNavigate();
  return <>
    <PageHeader title="Add application" subtitle="Save a source reference and durable application draft." action={<button className="button" onClick={() => navigate("/apps")}>Cancel</button>}/>
    <SourceWizard onCancel={() => navigate("/apps")} onCreated={(id) => navigate(`/apps/${id}`)} />
  </>;
}

function ApplicationDetailPage() {
  const { id = "" } = useParams();
  const appQuery = useQuery({ queryKey: ["app", id], queryFn: () => api.app(id) });
  const statusQuery = useQuery({ queryKey: ["system-status"], queryFn: api.status });
  const deploymentQuery = useQuery({ queryKey: ["deployments", id], queryFn: () => api.deployments(id) });
  if (appQuery.isLoading || statusQuery.isLoading) return <LoadingState/>;
  if (appQuery.isError) return <QueryError message={appQuery.error.message}/>;
  if (statusQuery.isError) return <QueryError message={statusQuery.error.message}/>;
  if (!appQuery.data || !statusQuery.data) return <QueryError message="The API returned an incomplete response."/>;
  const app = appQuery.data;
  const fakeRuntime = statusQuery.data.capabilities.fakeRuntime;
  const generatedRuntime = statusQuery.data.capabilities.generatedRuntime;
  const composeRuntime = statusQuery.data.capabilities.composeRuntime;
  const currentDeployment = deploymentQuery.data?.items[0];
  return <>
    <PageHeader title={app.name} subtitle={`${app.machineName || "Local machine"} · ${app.status}`}/>
    <p className="section-kicker">Overview</p>
    <div className="summary"><article><small>Current deployment</small><strong>{currentDeployment ? <StatusText value={currentDeployment.status}/> : deploymentQuery.isLoading ? "Loading..." : "Not deployed"}</strong><span>{currentDeployment ? `Configuration ${currentDeployment.configurationMode}` : deploymentQuery.isError ? "History unavailable" : "No deployment record"}</span></article><article><small>Source</small><strong className="mono">{app.slug}</strong><span>Runtime is not inferred</span></article><article><small>Health</small><strong>Not verified</strong><span>Health reporting is not available</span></article></div>
    {fakeRuntime ? <div className="callout warning"><strong>Development capability</strong><span>The fake runtime persists job progress but executes no workload.</span></div> : !composeRuntime && !generatedRuntime && <div className="callout info"><strong>Runtime actions unavailable</strong><span>Configure a runtime to deploy this application.</span></div>}
    <AutoDeployPanel appId={id} composeRuntime={composeRuntime} generatedRuntime={generatedRuntime} githubConnections={statusQuery.data.capabilities.githubConnections}/>
    <DeploymentHistoryPanel appId={id} composeRuntime={composeRuntime} fakeRuntime={fakeRuntime} generatedRuntime={generatedRuntime}/>
    <ApplicationConfigurationPanel appId={id}/>
  </>;
}

export function MachinesPage({ role, relayPanelLoader = defaultRelayPanelLoader }: { role: string; relayPanelLoader?: RelayPanelLoader }) {
  const query = useQuery({ queryKey: ["machines"], queryFn: api.machines });
  const items = query.data?.items ?? [];
  return <><PageHeader title="Machines" subtitle="Hosts registered with this local controller."/>{query.isLoading ? <LoadingState/> : query.isError ? <QueryError message={query.error.message}/> : <div className="machine-list">{items.map((machine) => <article className="machine" key={machine.id}><div><h2>{machine.name}</h2><p>{machine.os} · {machine.architecture} · {machine.hostname}</p></div><StatusText value={machine.status}/><div className="machine-meta">Local controller · independent runtime diagnostics</div></article>)}</div>}<RelayPanelSlot role={role} loader={relayPanelLoader}/></>;
}

function ActivityPage() {
  const query = useQuery({ queryKey: ["jobs"], queryFn: api.jobs, refetchInterval: 1000 });
  const items = query.data?.items ?? [];
  return <><PageHeader title="Activity" subtitle="Durable runtime jobs ordered by creation time."/>{query.isLoading ? <LoadingState/> : query.isError ? <QueryError message={query.error.message}/> : items.length === 0 ? <section className="empty"><h2>No activity yet</h2><p>Development deployments will appear here as durable jobs.</p></section> : <div className="activity-list">{items.map((job) => <ActivityRow job={job} key={job.id}/>)}</div>}</>;
}

const cancellableStatuses = new Set(["queued", "assigned", "running", "waiting_external", "waiting_user"]);
const terminalJobStatuses = new Set(["succeeded", "failed", "cancelled", "interrupted", "needs_attention"]);
const terminalCancellationMessages = new Map([
  ["cancelled", "Cancellation recorded. Job cancelled."],
  ["succeeded", "Cancellation recorded. Job succeeded."],
  ["failed", "Cancellation recorded. Job failed."],
  ["interrupted", "Cancellation recorded. Job interrupted."],
  ["needs_attention", "Cancellation recorded. Job needs attention."],
]);

export function ActivityRow({ job }: { job: Job }) {
  const queryClient = useQueryClient();
  const cancellation = useMutation({
    mutationFn: () => api.cancelJob(job.id),
    onSuccess: () => { void queryClient.invalidateQueries({ queryKey: ["jobs"] }); },
  });
  const current = terminalJobStatuses.has(job.status) ? job : cancellation.data?.job ?? job;
  const cancellationFeedback = terminalCancellationMessages.get(current.status) ?? "Cancellation recorded.";
  return <article className="activity-row">
    <time dateTime={current.createdAt}>{new Date(current.createdAt).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })}</time>
    <div><strong>{current.type} application</strong><small className="mono">{current.id}</small></div>
    <StatusText value={current.status}/>
    <div className="job-actions">
      {cancellableStatuses.has(current.status) && <button className="button small" onClick={() => cancellation.mutate()} disabled={cancellation.isPending || cancellation.isSuccess}>{cancellation.isPending ? "Cancelling…" : cancellation.isSuccess ? "Cancellation requested" : "Cancel job"}</button>}
      {cancellation.isSuccess && <span className="activity-feedback" role="status" aria-live="polite" aria-atomic="true">{cancellationFeedback}</span>}
      {cancellation.isError && <span className="activity-feedback error" role="alert">{cancellation.error.message}</span>}
    </div>
  </article>;
}

export function App() {
  const [bootstrapRequired, setBootstrapRequired] = useState<boolean | null>(null);
  const [user, setUser] = useState<User | null>(null);
  const navigate = useNavigate();
  useEffect(() => {
    api.bootstrapStatus().then(async ({ bootstrapRequired }) => {
      setBootstrapRequired(bootstrapRequired);
      if (!bootstrapRequired) {
        try {
          const restored = await api.me();
          await api.csrf();
          setUser(restored.user);
        } catch {
          setUser(null);
        }
      }
    }).catch(() => setBootstrapRequired(false));
  }, []);
  if (bootstrapRequired === null) return <main className="auth"><LoadingState/></main>;
  if (!user) return <Login setup={bootstrapRequired} onAuthenticated={(nextUser) => { setUser(nextUser); setBootstrapRequired(false); navigate("/apps"); }}/>;
  const logout = async () => { try { await api.logout(); } finally { clearCSRF(); setUser(null); navigate("/login"); } };
  return <UnsavedChangesGuard><Layout user={user} onLogout={logout}><Routes><Route path="/" element={<ApplicationsPage/>}/><Route path="/apps" element={<ApplicationsPage/>}/><Route path="/apps/new" element={<AddApplicationPage/>}/><Route path="/apps/:id" element={<ApplicationDetailPage/>}/><Route path="/machines" element={<MachinesPage role={user.role}/>}/><Route path="/activity" element={<ActivityPage/>}/><Route path="*" element={<ApplicationsPage/>}/></Routes></Layout></UnsavedChangesGuard>;
}
