import { useEffect, useRef, useState } from "react";
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
  type User,
} from "./api";

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
      <button className="rail-link" aria-label="Sign out" onClick={onLogout}><Icon name="logout"/><span>Sign out</span></button>
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
  return <main className="auth">
    <section aria-labelledby="auth-title">
      <div className="auth-brand"><b aria-hidden="true">h&gt;</b><span>hostd</span></div>
      <h1 id="auth-title">{bootstrapMode ? "Set up hostd" : "Welcome back"}</h1>
      <p>{bootstrapMode ? "Create the first administrator using the one-time token from hostd's protected local console." : "Sign in to your local deployment manager."}</p>
      {(serverError || Object.keys(errors).length > 0) && <div className="error-summary" ref={errorSummary} tabIndex={-1} role="alert">{serverError || "Check the highlighted fields."}</div>}
      <form onSubmit={handleSubmit(submit, invalid)} noValidate>
        {bootstrapMode && <FormField label="Bootstrap token" id="token" error={serverError.startsWith("Enter the one-time") ? serverError : undefined} required><input id="token" required aria-invalid={Boolean(serverError.startsWith("Enter the one-time"))} aria-describedby={serverError ? "token-error" : undefined} autoComplete="off" {...register("token")}/></FormField>}
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
    <PageHeader title="Applications" subtitle="Deploy and manage apps on your machine." action={<NavLink className="button primary" to="/apps/new">Add application</NavLink>}/>
    {query.isLoading ? <LoadingState/> : query.isError ? <QueryError message={query.error.message}/> : items.length > 0 ? <div className="app-list">{items.map((app) => <ApplicationRow key={app.id} app={app}/>)}</div> : <section className="empty"><h2>No applications yet</h2><p>Save a local project draft. Runtime execution remains capability-gated.</p><NavLink className="button primary" to="/apps/new">Add application</NavLink></section>}
  </>;
}

function ApplicationRow({ app }: { app: Application }) {
  return <article className="app-row">
    <div className="app-primary"><div className="title-line"><strong title={app.name}>{app.name}</strong><StatusText value={app.status}/></div><span className="muted" title={app.description}>{app.description || "No description"}</span></div>
    <Meta label="Machine" value={app.machineName || "Local machine"}/>
    <Meta label="Release" value="Not deployed" mono/>
    <Meta label="Created" value={new Date(app.createdAt).toLocaleDateString()}/>
    <NavLink className="button small app-open" aria-label={`Open ${app.name}`} to={`/apps/${app.id}`}>Open</NavLink>
  </article>;
}

function Meta({ label, value, mono = false }: { label: string; value: string; mono?: boolean }) {
  return <div className="meta"><small>{label}</small><span className={mono ? "mono" : undefined} title={value}>{value}</span></div>;
}

const addSchema = z.object({
  name: z.string().trim().min(1, "Enter an application name").max(100, "Use 100 characters or fewer"),
  description: z.string().max(300, "Use 300 characters or fewer"),
  sourcePath: z.string().trim().min(1, "Enter a local source path"),
});
type AddFields = z.infer<typeof addSchema>;

function AddApplicationPage() {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [inspection, setInspection] = useState("");
  const errorSummary = useRef<HTMLDivElement>(null);
  const { register, handleSubmit, getValues, formState: { errors, isSubmitting } } = useForm<AddFields>({ resolver: zodResolver(addSchema), defaultValues: { name: "", description: "", sourcePath: "" }, shouldFocusError: false });
  const create = useMutation({ mutationFn: api.createApp, onSuccess: async (app) => { await queryClient.invalidateQueries({ queryKey: ["apps"] }); navigate(`/apps/${app.id}`); } });
  const invalid = () => window.setTimeout(() => errorSummary.current?.focus(), 0);
  return <>
    <PageHeader title="Add application" subtitle="Save a source reference and durable application draft." action={<button className="button" onClick={() => navigate("/apps")}>Cancel</button>}/>
    <div className="wizard">
      <ol aria-label="Setup progress"><li aria-current="step">Source and review</li><li>Development deployment</li></ol>
      <form onSubmit={handleSubmit((values) => create.mutate(values), invalid)} noValidate>
        <h2>Application source</h2><p>Compose parsing starts in the real runtime milestone. Source validation here is truthful about that boundary.</p>
        {(create.error || Object.keys(errors).length > 0) && <div ref={errorSummary} tabIndex={-1} className="error-summary" role="alert">{create.error?.message || "Check the highlighted fields."}</div>}
        <FormField label="Application name" id="app-name" error={errors.name?.message} required><input id="app-name" required aria-invalid={Boolean(errors.name)} aria-describedby={errors.name ? "app-name-error" : undefined} {...register("name")}/></FormField>
        <FormField label="Description" id="description" error={errors.description?.message}><input id="description" aria-invalid={Boolean(errors.description)} aria-describedby={errors.description ? "description-error" : undefined} {...register("description")}/></FormField>
        <FormField label="Local source path" id="source-path" error={errors.sourcePath?.message} required><input id="source-path" required placeholder="C:\projects\my-app" aria-invalid={Boolean(errors.sourcePath)} aria-describedby={errors.sourcePath ? "source-path-error" : undefined} {...register("sourcePath")}/></FormField>
        <button type="button" className="button" onClick={() => api.inspect(getValues("sourcePath")).then((result) => setInspection(result.message)).catch((error) => setInspection(error.message))}>Check source</button>
        {inspection && <p className="callout info" role="status">{inspection}</p>}
        <footer><button className="button" type="button" onClick={() => navigate("/apps")}>Back</button><button className="button primary" disabled={isSubmitting || create.isPending}>{create.isPending ? "Saving…" : "Save application"}</button></footer>
      </form>
    </div>
  </>;
}

function ApplicationDetailPage() {
  const { id = "" } = useParams();
  const appQuery = useQuery({ queryKey: ["app", id], queryFn: () => api.app(id) });
  const statusQuery = useQuery({ queryKey: ["system-status"], queryFn: api.status });
  const deploy = useMutation({ mutationFn: () => api.action(id, "deploy") });
  if (appQuery.isLoading || statusQuery.isLoading) return <LoadingState/>;
  if (appQuery.isError) return <QueryError message={appQuery.error.message}/>;
  if (statusQuery.isError) return <QueryError message={statusQuery.error.message}/>;
  if (!appQuery.data || !statusQuery.data) return <QueryError message="The API returned an incomplete response."/>;
  const app = appQuery.data;
  const fakeRuntime = statusQuery.data.capabilities.fakeRuntime;
  return <>
    <PageHeader title={app.name} subtitle={`${app.machineName || "Local machine"} · ${app.status}`} action={fakeRuntime ? <button className="button primary" onClick={() => deploy.mutate()} disabled={deploy.isPending}>{deploy.isPending ? "Queuing…" : "Deploy with fake runtime"}</button> : undefined}/>
    <p className="section-kicker">Overview</p>
    <div className="summary"><article><small>Current state</small><strong><StatusText value={app.status}/></strong><span>Durable control record</span></article><article><small>Source</small><strong className="mono">{app.slug}</strong><span>Runtime is not inferred</span></article><article><small>Health</small><strong>Not verified</strong><span>No real workload has run</span></article></div>
    {fakeRuntime ? <div className="callout warning"><strong>Development capability</strong><span>The fake runtime persists job progress but executes no workload.</span></div> : <div className="callout info"><strong>Runtime actions unavailable</strong><span>Start hostd with the explicit development fake-runtime flag to exercise job orchestration.</span></div>}
    {deploy.data && <div className="callout success" role="status">Deployment job queued: <span className="mono">{deploy.data.job.id}</span></div>}
    {deploy.error && <div className="callout danger" role="alert">{deploy.error.message}</div>}
  </>;
}

function MachinesPage() {
  const query = useQuery({ queryKey: ["machines"], queryFn: api.machines });
  const items = query.data?.items ?? [];
  return <><PageHeader title="Machines" subtitle="Hosts registered with this local controller."/>{query.isLoading ? <LoadingState/> : query.isError ? <QueryError message={query.error.message}/> : <div className="machine-list">{items.map((machine) => <article className="machine" key={machine.id}><div><h2>{machine.name}</h2><p>{machine.os} · {machine.architecture} · {machine.hostname}</p></div><StatusText value={machine.status}/><div className="machine-meta">Local controller · independent runtime diagnostics</div></article>)}</div>}</>;
}

function ActivityPage() {
  const query = useQuery({ queryKey: ["jobs"], queryFn: api.jobs, refetchInterval: 1000 });
  const items = query.data?.items ?? [];
  return <><PageHeader title="Activity" subtitle="Durable runtime jobs ordered by creation time."/>{query.isLoading ? <LoadingState/> : query.isError ? <QueryError message={query.error.message}/> : items.length === 0 ? <section className="empty"><h2>No activity yet</h2><p>Development deployments will appear here as durable jobs.</p></section> : <div className="activity-list">{items.map((job) => <article className="activity-row" key={job.id}><time dateTime={job.createdAt}>{new Date(job.createdAt).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })}</time><div><strong>{job.type} application</strong><small className="mono">{job.id}</small></div><StatusText value={job.status}/></article>)}</div>}</>;
}

export function App() {
  const [bootstrapRequired, setBootstrapRequired] = useState<boolean | null>(null);
  const [user, setUser] = useState<User | null>(null);
  const navigate = useNavigate();
  useEffect(() => {
    api.bootstrapStatus().then(async ({ bootstrapRequired }) => {
      setBootstrapRequired(bootstrapRequired);
      if (!bootstrapRequired) {
        try { setUser((await api.me()).user); } catch { setUser(null); }
      }
    }).catch(() => setBootstrapRequired(false));
  }, []);
  if (bootstrapRequired === null) return <main className="auth"><LoadingState/></main>;
  if (!user) return <Login setup={bootstrapRequired} onAuthenticated={(nextUser) => { setUser(nextUser); setBootstrapRequired(false); navigate("/apps"); }}/>;
  const logout = async () => { try { await api.logout(); } finally { clearCSRF(); setUser(null); navigate("/login"); } };
  return <Layout user={user} onLogout={logout}><Routes><Route path="/" element={<ApplicationsPage/>}/><Route path="/apps" element={<ApplicationsPage/>}/><Route path="/apps/new" element={<AddApplicationPage/>}/><Route path="/apps/:id" element={<ApplicationDetailPage/>}/><Route path="/machines" element={<MachinesPage/>}/><Route path="/activity" element={<ActivityPage/>}/><Route path="*" element={<ApplicationsPage/>}/></Routes></Layout>;
}
