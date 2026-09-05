import { useEffect, useMemo, useRef, useState } from "react";
import type {
  AcceptDeploymentPlanRequest,
  AnalysisComponent,
  AnalysisEvidence,
  DeploymentPlanCandidate,
  InspectResponse,
} from "./api";

type ComponentDraft = {
  buildCommand: string;
  runCommand: string;
  nodeVersion: string;
  internalPort: string;
  healthProbe: string;
};

type Draft = {
  packageManager: string;
  installBehavior: string;
  migrationCommand: string;
  components: Record<string, ComponentDraft>;
};

const overrideableMissingField = (field: string) =>
  field === "package_manager" || field === "package_manager.version" || field === "install_behavior" || field === "node_version" ||
  field.endsWith(".build") || field.endsWith(".run") || field.endsWith(".internal_port") || field.endsWith(".health_probe");

function inferredDraft(candidate: DeploymentPlanCandidate): Draft {
  return {
    packageManager: candidate.packageManager.name ?? "",
    installBehavior: candidate.install?.command ?? "",
    migrationCommand: candidate.components.find((component) => component.migration?.present)?.migration?.command ?? "",
    components: Object.fromEntries(candidate.components.map((component) => [component.id, {
      buildCommand: component.build?.command ?? "",
      runCommand: component.run?.command ?? "",
      nodeVersion: candidate.nodeVersion.value ?? "",
      internalPort: component.internalPort?.value ?? "",
      healthProbe: component.healthProbe?.path ?? "",
    }])),
  };
}

function evidenceLabel(evidence: AnalysisEvidence[]) {
  if (evidence.length === 0) return "Rig did not record supporting evidence for this value.";
  return evidence.map((item) => [item.path, item.field || item.code].filter(Boolean).join(" · ")).join(", ");
}

function fieldMissing(candidate: DeploymentPlanCandidate, componentId: string, suffix: string) {
  return candidate.missingFields.includes(`components.${componentId}.${suffix}`);
}

function validate(candidate: DeploymentPlanCandidate, draft: Draft) {
  const errors: Record<string, string> = {};
  if (!draft.packageManager) errors.packageManager = "Choose npm, pnpm, or Yarn.";
  if (!draft.installBehavior.trim()) errors.installBehavior = "Enter the dependency installation command.";
  for (const component of candidate.components) {
    const value = draft.components[component.id];
    if (!value) continue;
    if (fieldMissing(candidate, component.id, "build") && !value.buildCommand.trim()) errors[`${component.id}.buildCommand`] = "Enter a build command.";
    if (!value.runCommand.trim()) errors[`${component.id}.runCommand`] = "Enter a run command.";
    if (!value.nodeVersion.trim()) errors[`${component.id}.nodeVersion`] = "Enter a supported Node.js version.";
    const port = Number(value.internalPort);
    if (!Number.isInteger(port) || port < 1 || port > 65535) errors[`${component.id}.internalPort`] = "Enter a port from 1 to 65535.";
    if (!value.healthProbe.startsWith("/")) errors[`${component.id}.healthProbe`] = "Enter a health-check path beginning with /.";
  }
  return errors;
}

function errorTarget(key: string) {
  if (key === "packageManager") return { id: "plan-package-manager", label: "Package manager" };
  if (key === "installBehavior") return { id: "plan-install-behavior", label: "Dependency installation" };
  const separator = key.lastIndexOf(".");
  const componentId = key.slice(0, separator);
  const field = key.slice(separator + 1);
  const labels: Record<string, string> = { buildCommand: "Build command", runCommand: "Run command", nodeVersion: "Node.js version", internalPort: "Internal port", healthProbe: "Health-check path" };
  return { id: `plan-${componentId}-${field}`, label: labels[field] ?? field };
}

export function deploymentPlanRequest(inspection: InspectResponse, candidate: DeploymentPlanCandidate, draft: Draft, expectedRevisionNumber: number): AcceptDeploymentPlanRequest {
  return {
    candidateId: candidate.id,
    expectedCandidateDigest: candidate.digest,
    expectedRevisionNumber,
    expectedSourceStructuralFingerprint: inspection.analysis.structuralFingerprint,
    packageManager: draft.packageManager,
    installBehavior: draft.installBehavior,
    migrationCommand: draft.migrationCommand,
    components: candidate.components.map((component) => ({
      componentId: component.id,
      buildCommand: draft.components[component.id]?.buildCommand ?? "",
      runCommand: draft.components[component.id]?.runCommand ?? "",
      nodeVersion: draft.components[component.id]?.nodeVersion ?? "",
      internalPort: Number(draft.components[component.id]?.internalPort ?? 0),
      healthProbe: draft.components[component.id]?.healthProbe ?? "",
    })),
  };
}

export function DeploymentPlanReview({
  inspection,
  expectedRevisionNumber,
  pending,
  error,
  onBack,
  onRefresh,
  onAccept,
  onUseCompose,
  draftSaved = false,
  onOpenSavedDraft,
}: {
  inspection: InspectResponse;
  expectedRevisionNumber: number;
  pending: boolean;
  error: string;
  onBack: () => void;
  onRefresh: () => void;
  onAccept: (request: AcceptDeploymentPlanRequest) => void;
  onUseCompose?: () => void;
  draftSaved?: boolean;
  onOpenSavedDraft?: () => void;
}) {
  const candidates = useMemo(() => inspection.analysis.candidates.filter((candidate) => candidate.kind === "javascript" && candidate.status !== "unsupported" && candidate.components.length > 0), [inspection]);
  const [candidateId, setCandidateId] = useState(candidates.length === 1 ? candidates[0].id : "");
  const candidate = candidates.find((item) => item.id === candidateId);
  const [draft, setDraft] = useState<Draft | null>(() => candidate ? inferredDraft(candidate) : null);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [changed, setChanged] = useState(new Set<string>());
  const [advancedOpen, setAdvancedOpen] = useState(false);
  const errorSummary = useRef<HTMLDivElement>(null);
  const heading = useRef<HTMLHeadingElement>(null);

  useEffect(() => { heading.current?.focus(); }, []);
  useEffect(() => {
    const selectedStillExists = candidates.some((item) => item.id === candidateId);
    const nextCandidateId = selectedStillExists ? candidateId : candidates.length === 1 ? candidates[0].id : "";
    if (nextCandidateId !== candidateId) {
      setCandidateId(nextCandidateId);
      return;
    }
    const next = candidates.find((item) => item.id === nextCandidateId);
    setDraft(next ? inferredDraft(next) : null);
    setErrors({});
    setChanged(new Set());
    setAdvancedOpen((next?.missingFields.filter(overrideableMissingField).length ?? 0) > 0);
  }, [candidateId, candidates]);
  useEffect(() => { if (error) window.setTimeout(() => errorSummary.current?.focus(), 0); }, [error]);

  const chooseCandidate = (nextId: string) => setCandidateId(nextId);
  const editTop = (field: "packageManager" | "installBehavior" | "migrationCommand", value: string) => {
    setDraft((current) => current ? { ...current, [field]: value } : current);
    setChanged((current) => new Set(current).add(field));
    setErrors((current) => ({ ...current, [field]: "" }));
  };
  const editComponent = (componentId: string, field: keyof ComponentDraft, value: string) => {
    setDraft((current) => current ? { ...current, components: { ...current.components, [componentId]: { ...current.components[componentId], [field]: value } } } : current);
    const key = `${componentId}.${field}`;
    setChanged((current) => new Set(current).add(key));
    setErrors((current) => ({ ...current, [key]: "" }));
  };
  const resetField = (key: string, fieldId: string, action: () => void) => {
    action();
    setChanged((current) => { const next = new Set(current); next.delete(key); return next; });
    window.setTimeout(() => document.getElementById(fieldId)?.focus(), 0);
  };
  const submit = () => {
    if (!candidate || !draft) return;
    const nextErrors = validate(candidate, draft);
    setErrors(nextErrors);
    if (Object.keys(nextErrors).length > 0) {
      if (Object.keys(nextErrors).some((key) => !key.endsWith(".buildCommand") && !key.endsWith(".runCommand"))) setAdvancedOpen(true);
      window.setTimeout(() => errorSummary.current?.focus(), 0);
      return;
    }
    onAccept(deploymentPlanRequest(inspection, candidate, draft, expectedRevisionNumber));
  };

  if (candidates.length === 0) return <section className="plan-review" aria-labelledby="plan-review-title" aria-busy={pending}>
    <h2 id="plan-review-title" ref={heading} tabIndex={-1}>Rig can’t safely identify the app yet</h2>
    <p>The repository does not contain a supported project layout that Rig can build automatically.</p>
    <AnalysisProblems inspection={inspection} />
    {draftSaved && onOpenSavedDraft && <SavedDraftNotice onOpen={onOpenSavedDraft} />}
    <footer><button className="button" type="button" disabled={pending || draftSaved} onClick={onBack}>Back to source</button><button className="button" type="button" disabled={pending} onClick={onRefresh}>Analyze again</button></footer>
  </section>;

  const blocking = candidate?.missingFields.filter((field) => !overrideableMissingField(field)) ?? [];
  const inferred = candidate ? inferredDraft(candidate) : null;
  const advancedRequired = candidate?.missingFields.filter(overrideableMissingField).length ?? 0;

  return <section className="plan-review" aria-labelledby="plan-review-title" aria-busy={pending}>
    <h2 id="plan-review-title" ref={heading} tabIndex={-1}>How Rig will run this app</h2>
    <p>Review the suggestions. Change only what your project needs.</p>
    <span className="sr-only" role="status" aria-live="polite" aria-atomic="true">{pending ? "Accepting deployment setup." : ""}</span>
    {draftSaved && onOpenSavedDraft && <SavedDraftNotice onOpen={onOpenSavedDraft} />}
    {(error || Object.values(errors).some(Boolean)) && <div ref={errorSummary} className="error-summary" role="alert" tabIndex={-1}>
      {error || "Check the highlighted deployment settings."}
      {!error && <ul>{Object.entries(errors).filter(([, message]) => Boolean(message)).map(([key, message]) => { const target = errorTarget(key); return <li key={key}><a href={`#${target.id}`}>{target.label}: {message}</a></li>; })}</ul>}
      {error && <button className="button small" type="button" disabled={pending} onClick={onRefresh}>Review updated setup</button>}
    </div>}
    {candidates.length > 1 && <fieldset className="candidate-picker">
      <legend>Which app do you want to deploy?</legend>
      {candidates.map((item) => <label key={item.id} className={candidateId === item.id ? "candidate-option selected" : "candidate-option"}>
        <input type="radio" name="deployment-candidate" disabled={pending} checked={candidateId === item.id} onChange={() => chooseCandidate(item.id)} />
        <span><strong>{item.rootDirectory === "." ? "Repository root" : item.rootDirectory}</strong><small>{item.components.map((component) => component.framework || component.kind).join(" + ")}</small></span>
      </label>)}
    </fieldset>}
    {!candidate && <div className="callout warning" role="alert"><strong>Choose a project</strong><span>Rig found more than one independent app. Select the one to configure.</span></div>}
    {candidate && draft && <>
      {blocking.length > 0 && <div className="callout warning" role="alert"><strong>Rig needs a safer project choice</strong><span>{candidate.findings.map((finding) => finding.message).join(" ") || `Unresolved fields: ${blocking.join(", ")}.`}</span></div>}
      <div className="component-plans">
        {candidate.components.map((component) => <ComponentPlanCard key={component.id} component={component} candidate={candidate} draft={draft.components[component.id]} errors={errors} changed={changed} inferred={inferred!.components[component.id]} onEdit={editComponent} onReset={resetField} />)}
      </div>
      <p className="command-security-note">Commands run inside the project container, never directly on Windows. Don’t put passwords or API keys in commands; add them to application configuration.</p>
      <details className="advanced-settings" open={advancedOpen || advancedRequired > 0} onToggle={(event) => setAdvancedOpen(event.currentTarget.open)}>
        <summary>Advanced settings {advancedRequired > 0 && <span className="badge warning">{advancedRequired} required</span>}</summary>
        <div className="advanced-settings-content">
          <div className="field">
            <label htmlFor="plan-package-manager">Package manager (required)</label>
            <select id="plan-package-manager" required value={draft.packageManager} aria-invalid={Boolean(errors.packageManager)} aria-describedby={errors.packageManager ? "plan-package-manager-error" : undefined} onChange={(event) => editTop("packageManager", event.target.value)}><option value="">Choose one</option><option value="npm">npm</option><option value="pnpm">pnpm</option><option value="yarn">Yarn</option></select>
            <FieldMeta label="package manager" fieldId="plan-package-manager" changed={changed.has("packageManager")} evidence={candidate.packageManager.evidence} onReset={() => resetField("packageManager", "plan-package-manager", () => editTop("packageManager", inferred!.packageManager))} />
            {errors.packageManager && <p id="plan-package-manager-error" className="form-error">{errors.packageManager}</p>}
          </div>
          <div className="field">
            <label htmlFor="plan-install-behavior">Dependency installation (required)</label>
            <input className="command-input" id="plan-install-behavior" required value={draft.installBehavior} aria-invalid={Boolean(errors.installBehavior)} aria-describedby={errors.installBehavior ? "plan-install-behavior-error" : undefined} onChange={(event) => editTop("installBehavior", event.target.value)} />
            <FieldMeta label="dependency installation" fieldId="plan-install-behavior" changed={changed.has("installBehavior")} evidence={candidate.install?.evidence ?? []} onReset={() => resetField("installBehavior", "plan-install-behavior", () => editTop("installBehavior", inferred!.installBehavior))} />
            {candidate.missingFields.includes("package_manager.version") && <small>Yarn does not declare its version in this repository. Enter the exact Corepack/Yarn install command your project requires; Rig will record it as a reviewed override.</small>}
            {errors.installBehavior && <p id="plan-install-behavior-error" className="form-error">{errors.installBehavior}</p>}
          </div>
          {candidate.components.map((component) => <ComponentAdvanced key={component.id} component={component} draft={draft.components[component.id]} errors={errors} changed={changed} inferred={inferred!.components[component.id]} onEdit={editComponent} onReset={resetField} />)}
          {candidate.components.some((component) => component.migration?.present) && <div className="migration-review">
            <strong>Database migration detected</strong>
            <p>Rig will run this before the new version starts. The old and new versions briefly share the migrated database, so the migration should remain backward-compatible. Rig will not automatically undo database changes.</p>
            <div className="field"><label htmlFor="plan-migration-command">Migration command</label><input className="command-input" id="plan-migration-command" value={draft.migrationCommand} onChange={(event) => editTop("migrationCommand", event.target.value)} /></div>
            <small>Accepting this setup does not approve the migration. Approval is a separate action.</small>
          </div>}
        </div>
      </details>
    </>}
    <footer><button className="button" type="button" disabled={pending || draftSaved} onClick={onBack}>Back to source</button><button className="button primary" type="button" disabled={!candidate || !draft || blocking.length > 0 || pending} onClick={submit}>{pending ? "Accepting…" : "Accept setup"}</button></footer>
    {onUseCompose && !draftSaved && <details className="other-strategies"><summary>Other setup options</summary><button className="text-button" type="button" disabled={pending} onClick={onUseCompose}>Use existing Compose setup</button></details>}
  </section>;
}

function SavedDraftNotice({ onOpen }: { onOpen: () => void }) {
  return <div className="callout info"><strong>Application draft saved</strong><span>Rig created this application before accepting its setup. Its source and application details are now locked in this wizard.</span><button className="button small" type="button" onClick={onOpen}>Open saved draft</button></div>;
}

function ComponentPlanCard({ component, candidate, draft, errors, changed, inferred, onEdit, onReset }: { component: AnalysisComponent; candidate: DeploymentPlanCandidate; draft: ComponentDraft; errors: Record<string, string>; changed: Set<string>; inferred: ComponentDraft; onEdit: (id: string, field: keyof ComponentDraft, value: string) => void; onReset: (key: string, fieldId: string, action: () => void) => void }) {
  const staticRuntime = component.kind === "static";
  const buildId = `plan-${component.id}-buildCommand`;
  const runId = `plan-${component.id}-runCommand`;
  return <article className="component-plan">
    <header><div><h3>{component.name || "Application"}</h3><p>{component.kind.replaceAll("_", " ")} · {component.rootDirectory === "." ? "repository root" : component.rootDirectory}</p></div>{component.framework && <span className="badge">{component.framework}</span>}</header>
    <div className="field">
      <label htmlFor={buildId}>Build command{fieldMissing(candidate, component.id, "build") && " (required)"}</label>
      <input className="command-input" id={buildId} required={fieldMissing(candidate, component.id, "build")} value={draft.buildCommand} aria-invalid={Boolean(errors[`${component.id}.buildCommand`])} aria-describedby={errors[`${component.id}.buildCommand`] ? `${buildId}-error` : undefined} onChange={(event) => onEdit(component.id, "buildCommand", event.target.value)} />
      <FieldMeta label={`${component.name} build command`} fieldId={buildId} changed={changed.has(`${component.id}.buildCommand`)} evidence={component.build?.evidence ?? []} onReset={() => onReset(`${component.id}.buildCommand`, buildId, () => onEdit(component.id, "buildCommand", inferred.buildCommand))} />
      {errors[`${component.id}.buildCommand`] && <p id={`${buildId}-error`} className="form-error">{errors[`${component.id}.buildCommand`]}</p>}
    </div>
    <div className="field">
      {staticRuntime ? <><span id={`${runId}-label`} className="field-label">Run command</span><div id={runId} aria-labelledby={`${runId}-label`} className="managed-command">Serve generated static files <span className="badge">Managed by Rig</span></div></> : <><label htmlFor={runId}>Run command (required)</label><input className="command-input" id={runId} required value={draft.runCommand} aria-invalid={Boolean(errors[`${component.id}.runCommand`])} aria-describedby={errors[`${component.id}.runCommand`] ? `${runId}-error` : undefined} onChange={(event) => onEdit(component.id, "runCommand", event.target.value)} /></>}
      {!staticRuntime && <FieldMeta label={`${component.name} run command`} fieldId={runId} changed={changed.has(`${component.id}.runCommand`)} evidence={component.run?.evidence ?? []} onReset={() => onReset(`${component.id}.runCommand`, runId, () => onEdit(component.id, "runCommand", inferred.runCommand))} />}
      {errors[`${component.id}.runCommand`] && <p id={`${runId}-error`} className="form-error">{errors[`${component.id}.runCommand`]}</p>}
    </div>
  </article>;
}

function ComponentAdvanced({ component, draft, errors, changed, inferred, onEdit, onReset }: { component: AnalysisComponent; draft: ComponentDraft; errors: Record<string, string>; changed: Set<string>; inferred: ComponentDraft; onEdit: (id: string, field: keyof ComponentDraft, value: string) => void; onReset: (key: string, fieldId: string, action: () => void) => void }) {
  const fields: Array<{ key: keyof ComponentDraft; label: string; evidence: AnalysisEvidence[]; type?: string }> = [
    { key: "nodeVersion", label: "Node.js version", evidence: [] },
    { key: "internalPort", label: "Internal port", evidence: component.internalPort?.evidence ?? [], type: "number" },
    { key: "healthProbe", label: "Health-check path", evidence: component.healthProbe?.evidence ?? [] },
  ];
  return <fieldset className="component-advanced"><legend>{component.name || component.rootDirectory}</legend>{fields.map((field) => {
    const id = `plan-${component.id}-${field.key}`;
    const error = errors[`${component.id}.${field.key}`];
    return <div className="field" key={field.key}><label htmlFor={id}>{field.label} (required)</label><input id={id} required type={field.type} value={draft[field.key]} aria-invalid={Boolean(error)} aria-describedby={error ? `${id}-error` : undefined} onChange={(event) => onEdit(component.id, field.key, event.target.value)} /><FieldMeta label={`${component.name} ${field.label}`} fieldId={id} changed={changed.has(`${component.id}.${field.key}`)} evidence={field.evidence} onReset={() => onReset(`${component.id}.${field.key}`, id, () => onEdit(component.id, field.key, inferred[field.key]))} />{error && <p id={`${id}-error`} className="form-error">{error}</p>}</div>;
  })}</fieldset>;
}

function FieldMeta({ label, fieldId, changed, evidence, onReset }: { label: string; fieldId: string; changed: boolean; evidence: AnalysisEvidence[]; onReset: () => void }) {
  return <div className="field-meta" data-field-id={fieldId}>{changed && <span className="badge changed">Changed</span>}<button type="button" className="text-button inline" disabled={!changed} aria-label={`Reset ${label} to suggestion`} onClick={onReset}>Reset to suggestion</button><details><summary aria-label={`Why this ${label}?`}>Why this?</summary><span>{evidenceLabel(evidence)}</span></details><span className="sr-only" aria-live="polite" aria-atomic="true">{changed ? `${label} changed.` : `${label} uses the suggested value.`}</span></div>;
}

function AnalysisProblems({ inspection }: { inspection: InspectResponse }) {
  const findings = [...inspection.analysis.findings, ...inspection.analysis.candidates.flatMap((candidate) => candidate.findings)];
  return <div className="callout warning" role="alert"><strong>Analysis needs more information</strong>{findings.length ? findings.map((finding, index) => <span key={`${finding.code}:${finding.path ?? ""}:${index}`}>{finding.message}</span>) : <span>Choose another source or add a supported JavaScript/TypeScript application.</span>}</div>;
}
