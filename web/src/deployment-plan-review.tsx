import { useEffect, useMemo, useRef, useState } from "react";
import type {
  AcceptDeploymentPlanRequest,
  DeploymentPlanCandidate,
  DeploymentPlanRevision,
  DeploymentSetupComponentInput,
  DeploymentSetupInput,
  InspectResponse,
} from "./api";

type Technology = "node" | "nextjs" | "static";
type FieldErrors = Record<string, string>;

const supportedNodeVersions = ["20", "22", "24"] as const;

function technologyFor(component: DeploymentPlanCandidate["components"][number]): Technology {
  if (component.kind === "static") return "static";
  if (component.framework.toLowerCase() === "nextjs" || component.framework.toLowerCase() === "next.js") return "nextjs";
  return "node";
}

function supportedNodeVersion(value: string | undefined) {
  const match = value?.match(/(?:^|v)(20|22|24)(?:$|\D)/);
  return match?.[1] ?? "24";
}

function defaultComponent(id = "component-1", technology: Technology = "node"): DeploymentSetupComponentInput {
  const staticSite = technology === "static";
  return {
    id,
    technology,
    rootDirectory: ".",
    packageManager: "npm",
    nodeVersion: "24",
    installCommand: staticSite ? "npm install" : "",
    buildCommand: staticSite ? "npm run build" : "",
    startCommand: "",
    outputDirectory: staticSite ? "dist" : "",
    internalPort: staticSite ? 8080 : 3000,
    healthProbe: "/",
  };
}

export function defaultDeploymentSetup(): DeploymentSetupInput {
  return { components: [defaultComponent()] };
}

export function deploymentSetupFromCandidate(candidate: DeploymentPlanCandidate): DeploymentSetupInput {
  const migrationCommand = candidate.components.find((component) => component.migration?.present)?.migration?.command;
  return {
    components: candidate.components.map((component) => {
      const technology = technologyFor(component);
      const staticSite = technology === "static";
      return {
        id: component.id,
        technology,
        rootDirectory: component.rootDirectory || ".",
        packageManager: ["npm", "pnpm", "yarn"].includes(candidate.packageManager.name ?? "") ? candidate.packageManager.name! : "npm",
        nodeVersion: supportedNodeVersion(candidate.nodeVersion.value),
        installCommand: candidate.install?.command ?? "",
        buildCommand: component.build?.command ?? "",
        startCommand: staticSite ? "" : component.run?.command ?? "",
        outputDirectory: staticSite ? component.staticOutputDirectory || "dist" : "",
        internalPort: Number(component.internalPort?.value) || (staticSite ? 8080 : 3000),
        healthProbe: component.healthProbe?.path || "/",
      };
    }),
    ...(migrationCommand ? { migrationCommand } : {}),
  };
}

export function deploymentSetupFromRevision(revision: DeploymentPlanRevision): DeploymentSetupInput {
  if (revision.setup) return copySetup(revision.setup);
  return {
    components: revision.components.map((component, index) => {
      const staticSite = component.role.toLowerCase().includes("static");
      return {
        id: component.name || `legacy-component-${index + 1}`,
        technology: staticSite ? "static" : "node",
        rootDirectory: component.rootDirectory || ".",
        packageManager: ["npm", "pnpm", "yarn"].includes(component.packageManager) ? component.packageManager : "npm",
        nodeVersion: supportedNodeVersion(component.nodeVersion),
        installCommand: component.installBehavior ?? "",
        buildCommand: component.buildCommand ?? "",
        startCommand: staticSite ? "" : component.runCommand ?? "",
        outputDirectory: staticSite ? legacyStaticOutputDirectory(component.runCommand) : "",
        internalPort: component.internalPort || (staticSite ? 8080 : 3000),
        healthProbe: component.healthProbe || "/",
      };
    }),
    ...(revision.migration.command ? { migrationCommand: revision.migration.command } : {}),
  };
}

function legacyStaticOutputDirectory(command: string) {
  const quoted = command.match(/^rig-static\s+--root\s+'((?:[^']|'"'"')*)'\s+--port\s+\d+\s*$/);
  if (quoted) return quoted[1].replaceAll("'\"'\"'", "'");
  const plain = command.match(/^rig-static\s+--root\s+([A-Za-z0-9._/-]+)\s+--port\s+\d+\s*$/);
  return plain?.[1] ?? "";
}

function copySetup(setup: DeploymentSetupInput): DeploymentSetupInput {
  return {
    components: setup.components.map((component) => ({ ...component })),
    ...(setup.migrationCommand ? { migrationCommand: setup.migrationCommand } : {}),
  };
}

function setupKey(setup: DeploymentSetupInput) {
  return JSON.stringify({
    components: setup.components.map((component) => ({
      ...component,
      rootDirectory: component.rootDirectory.trim(),
      healthProbe: component.healthProbe.trim(),
      outputDirectory: component.outputDirectory.trim(),
    })),
    migrationCommand: setup.migrationCommand ?? "",
  });
}

function fieldKey(componentId: string, field: string) {
  return `components.${componentId}.${field}`;
}

function fieldId(index: number, field: string) {
  return `deployment-setup-${index}-${field}`;
}

function userCandidate(candidate: DeploymentPlanCandidate | undefined) {
  return candidate?.id === "user:deployment-setup" || candidate?.origin.toLowerCase() === "user";
}

function viableCandidates(inspection: InspectResponse | undefined) {
  return inspection?.analysis.candidates.filter((candidate) => candidate.kind === "javascript" && candidate.status !== "unsupported") ?? [];
}

function validate(setup: DeploymentSetupInput): FieldErrors {
  const errors: FieldErrors = {};
  if (setup.components.length === 0) errors.components = "Add a server or static-site component.";
  const staticCount = setup.components.filter((component) => component.technology === "static").length;
  const serverCount = setup.components.length - staticCount;
  if (staticCount > 1 || serverCount > 1 || setup.components.length > 2) {
    errors.components = "Choose one server, one static site, or a static site and server.";
  }
  const ids = new Set<string>();
  for (const component of setup.components) {
    const prefix = (field: string) => fieldKey(component.id, field);
    if (!component.id || ids.has(component.id)) errors[prefix("id")] = "Components need unique identities.";
    ids.add(component.id);
    if (!(["node", "nextjs", "static"] as string[]).includes(component.technology)) errors[prefix("technology")] = "Choose Node.js, Next.js, or Static site.";
    if (!component.rootDirectory.trim()) errors[prefix("rootDirectory")] = "Enter the component root directory.";
    if (!(["npm", "pnpm", "yarn"] as string[]).includes(component.packageManager)) errors[prefix("packageManager")] = "Choose npm, pnpm, or Yarn.";
    if (!supportedNodeVersions.includes(component.nodeVersion as (typeof supportedNodeVersions)[number])) errors[prefix("nodeVersion")] = "Choose Node.js 20, 22, or 24.";
    const port = Number(component.internalPort);
    if (!Number.isInteger(port) || port < 1 || port > 65535) errors[prefix("internalPort")] = "Enter a port from 1 to 65535.";
    if (!component.healthProbe.trim().startsWith("/")) errors[prefix("healthProbe")] = "Enter a health-check path beginning with /.";
    if (component.technology === "static") {
      if (!component.outputDirectory.trim()) errors[prefix("outputDirectory")] = "Enter the generated static directory.";
    } else if (!component.startCommand.trim()) errors[prefix("startCommand")] = "Enter a server start command.";
  }
  return errors;
}

function errorTarget(key: string, components: DeploymentSetupComponentInput[]) {
  if (key === "components") return { id: "deployment-setup-components", label: "Components" };
  const component = components.find((item) => key.startsWith(`components.${item.id}.`));
  const field = component ? key.slice(`components.${component.id}.`.length) : "";
  const index = component ? components.indexOf(component) : -1;
  const labels: Record<string, string> = {
    rootDirectory: "Root directory", packageManager: "Package manager", nodeVersion: "Node.js version",
    installCommand: "Install command", buildCommand: "Build command", startCommand: "Start command",
    outputDirectory: "Output directory", internalPort: "Internal port", healthProbe: "Health-check path",
  };
  return { id: index >= 0 ? fieldId(index, field) : "deployment-setup-components", label: labels[field] ?? "Component" };
}

function normalizedFieldErrors(errors: FieldErrors, components: DeploymentSetupComponentInput[]) {
  return Object.fromEntries(Object.entries(errors).map(([key, message]) => {
    const match = key.match(/^components\.(\d+)\.(.+)$/);
    if (!match) return [key, message];
    const component = components[Number(match[1])];
    return [component ? fieldKey(component.id, match[2]) : key, message];
  }));
}

export function deploymentPlanRequest(inspection: InspectResponse, candidate: DeploymentPlanCandidate, setup: DeploymentSetupInput, expectedRevisionNumber: number): AcceptDeploymentPlanRequest {
  return {
    candidateId: candidate.id,
    expectedCandidateDigest: candidate.digest,
    expectedRevisionNumber,
    expectedSourceStructuralFingerprint: inspection.analysis.structuralFingerprint,
    setup: copySetup(setup),
  };
}

export function DeploymentPlanReview({
  inspection,
  initialSetup,
  expectedRevisionNumber,
  pending,
  error,
  apiErrors = {},
  reviewedSetup = null,
  reviewRequired = false,
  onBack,
  onAnalyze,
  onAccept,
  onUseCompose,
  draftSaved = false,
  onOpenSavedDraft,
}: {
  inspection?: InspectResponse;
  initialSetup?: DeploymentSetupInput;
  expectedRevisionNumber: number;
  pending: boolean;
  error: string;
  apiErrors?: FieldErrors;
  reviewedSetup?: DeploymentSetupInput | null;
  reviewRequired?: boolean;
  onBack?: () => void;
  onAnalyze: (setup: DeploymentSetupInput) => void;
  onAccept: (request: AcceptDeploymentPlanRequest) => void;
  onUseCompose?: () => void;
  draftSaved?: boolean;
  onOpenSavedDraft?: () => void;
}) {
  const candidates = useMemo(() => viableCandidates(inspection), [inspection]);
  const initialCandidate = candidates.length === 1 ? candidates[0] : undefined;
  const [candidateId, setCandidateId] = useState(initialCandidate?.id ?? "");
  const initialDraft = useRef<DeploymentSetupInput | null>(null);
  if (!initialDraft.current) initialDraft.current = copySetup(initialSetup ?? (initialCandidate ? deploymentSetupFromCandidate(initialCandidate) : defaultDeploymentSetup()));
  const [draft, setDraft] = useState<DeploymentSetupInput>(initialDraft.current);
  const [detectedSetup, setDetectedSetup] = useState<DeploymentSetupInput | null>(() => initialCandidate && !userCandidate(initialCandidate) ? deploymentSetupFromCandidate(initialCandidate) : null);
  const [errors, setErrors] = useState<FieldErrors>(() => validate(initialDraft.current!));
  const [dirty, setDirty] = useState(false);
  const [dismissedAPIErrorKeys, setDismissedAPIErrorKeys] = useState<Set<string>>(new Set());
  const [errorDismissed, setErrorDismissed] = useState(false);
  const [advancedOpen, setAdvancedOpen] = useState(false);
  const [componentSequence, setComponentSequence] = useState(() => draft.components.length + 1);
  const errorSummary = useRef<HTMLDivElement>(null);
  const heading = useRef<HTMLHeadingElement>(null);
  const previousInspection = useRef<InspectResponse | undefined>(inspection);

  const candidate = candidates.find((item) => item.id === candidateId) ?? (candidates.length === 1 ? candidates[0] : undefined);
  const reviewed = !reviewRequired && userCandidate(candidate) && Boolean(reviewedSetup) && setupKey(reviewedSetup!) === setupKey(draft);
  const hasDetectedSettings = detectedSetup !== null;
  const serverErrors = normalizedFieldErrors(apiErrors, draft.components);
  const visibleErrors = { ...Object.fromEntries(Object.entries(serverErrors).filter(([key]) => !dismissedAPIErrorKeys.has(key))), ...errors };
  const apiErrorSignature = JSON.stringify(apiErrors);

  useEffect(() => { heading.current?.focus(); }, []);
  useEffect(() => {
    if (previousInspection.current === inspection) return;
    previousInspection.current = inspection;
    if (!candidates.some((item) => item.id === candidateId)) {
      const next = candidates.find(userCandidate) ?? (candidates.length === 1 ? candidates[0] : undefined);
      setCandidateId(next?.id ?? "");
    }
    const detected = candidates.find((item) => !userCandidate(item));
    if (detected) {
      const setup = deploymentSetupFromCandidate(detected);
      setDetectedSetup(setup);
      if (!dirty) {
        setDraft(setup);
        setErrors(validate(setup));
      }
    }
  }, [candidateId, candidates, dirty, inspection]);
  useEffect(() => {
    setDismissedAPIErrorKeys(new Set());
    setErrorDismissed(false);
    if (error) window.setTimeout(() => errorSummary.current?.focus(), 0);
  }, [apiErrorSignature, error]);

  const updateSetup = (updater: (current: DeploymentSetupInput) => DeploymentSetupInput, errorKey?: string) => {
    setDraft((current) => updater(current));
    setDirty(true);
    setErrorDismissed(true);
    if (errorKey) setDismissedAPIErrorKeys((current) => new Set(current).add(errorKey));
    if (errorKey) setErrors((current) => ({ ...current, [errorKey]: "" }));
  };
  const updateComponent = (index: number, field: keyof DeploymentSetupComponentInput, value: string | number) => {
    const component = draft.components[index];
    const key = component ? fieldKey(component.id, String(field)) : undefined;
    updateSetup((current) => ({ ...current, components: current.components.map((item, itemIndex) => {
      if (itemIndex !== index) return item;
      if (field === "technology") {
        const technology = value as Technology;
        const staticSite = technology === "static";
        return { ...item, technology, startCommand: staticSite ? "" : item.startCommand || "npm start", outputDirectory: staticSite ? item.outputDirectory || "dist" : "", internalPort: staticSite ? (item.technology === "static" ? item.internalPort || 8080 : 8080) : (item.technology === "static" ? 3000 : item.internalPort || 3000) };
      }
      return { ...item, [field]: field === "internalPort" ? Number(value) : value };
    }) }), key);
  };
  const chooseCandidate = (next: DeploymentPlanCandidate) => {
    const setup = deploymentSetupFromCandidate(next);
    setCandidateId(next.id);
    setDetectedSetup(setup);
    if (!dirty) {
      setDraft(setup);
      setErrors(validate(setup));
    }
  };
  const addComponent = (technology: Technology) => {
    const id = `component-${componentSequence}`;
    setComponentSequence((current) => current + 1);
    updateSetup((current) => ({ ...current, components: [...current.components, defaultComponent(id, technology)] }));
  };
  const removeComponent = (index: number) => updateSetup((current) => ({ ...current, components: current.components.filter((_, itemIndex) => itemIndex !== index) }));
  const resetDetected = () => {
    if (!detectedSetup) return;
    setDraft(copySetup(detectedSetup));
    setDirty(false);
    setErrorDismissed(true);
    setDismissedAPIErrorKeys(new Set(Object.keys(serverErrors)));
    setErrors(validate(detectedSetup));
    window.setTimeout(() => document.getElementById(fieldId(0, "rootDirectory"))?.focus(), 0);
  };
  const submit = () => {
    const nextErrors = validate(draft);
    setErrors(nextErrors);
    if (Object.keys(nextErrors).length > 0) {
      setAdvancedOpen(true);
      window.setTimeout(() => errorSummary.current?.focus(), 0);
      return;
    }
    if (reviewed && inspection && candidate) {
      onAccept(deploymentPlanRequest(inspection, candidate, draft, expectedRevisionNumber));
      return;
    }
    onAnalyze(copySetup(draft));
  };

  const detectedCandidates = candidates.filter((item) => !userCandidate(item));
  return <section className="plan-review" aria-labelledby="plan-review-title" aria-busy={pending}>
    <h2 id="plan-review-title" ref={heading} tabIndex={-1}>How Rig will run this app</h2>
    <p>Build and run settings start with Rig’s detection, but remain fully editable even when detection is incomplete.</p>
    <span className="sr-only" aria-live="polite" aria-atomic="true">{pending ? (reviewed ? "Accepting deployment setup." : "Reviewing deployment setup.") : ""}</span>
    {draftSaved && onOpenSavedDraft && <SavedDraftNotice onOpen={onOpenSavedDraft} />}
    {((!errorDismissed && error) || Object.values(visibleErrors).some(Boolean)) && <div ref={errorSummary} className="error-summary" role="alert" tabIndex={-1}>
      <span>{(!errorDismissed && error) || "Check the highlighted deployment settings."}</span>
      {Object.values(visibleErrors).some(Boolean) && <ul>{Object.entries(visibleErrors).filter(([, message]) => Boolean(message)).map(([key, message]) => {
        const target = errorTarget(key, draft.components);
        return <li key={key}><a href={`#${target.id}`}>{target.label}: {message}</a></li>;
      })}</ul>}
      {!errorDismissed && error && <button className="button small" type="button" disabled={pending} onClick={submit}>Retry review</button>}
    </div>}
    {detectedCandidates.length > 1 && <fieldset className="candidate-picker">
      <legend>Detected project layouts</legend>
      {detectedCandidates.map((item) => <label key={item.id} className={candidateId === item.id ? "candidate-option selected" : "candidate-option"}>
        <input type="radio" name="deployment-candidate" disabled={pending} checked={candidateId === item.id} onChange={() => chooseCandidate(item)} />
        <span><strong>{item.rootDirectory === "." ? "Repository root" : item.rootDirectory}</strong><small>{item.components.map((component) => component.framework || component.kind).join(" + ")}</small></span>
      </label>)}
    </fieldset>}
    {inspection && candidates.length === 0 && <div className="callout info" role="status"><strong>No supported setup was detected</strong><span>Enter the commands and paths Rig should review. Empty install and build commands explicitly skip those steps.</span></div>}
    {inspection?.analysis.findings.length ? <div className="callout warning" role="status"><strong>Detection findings</strong>{inspection.analysis.findings.map((finding, index) => <span key={`${finding.code}:${index}`}>{finding.message}</span>)}</div> : null}
    <div className="deployment-setup-actions" id="deployment-setup-components">
      <div><strong>Application components</strong><span>Use one server, one static site, or a static site and server together.</span></div>
      <div><button className="button small" type="button" disabled={pending || draft.components.some((component) => component.technology !== "static")} onClick={() => addComponent("node")}>Add server</button><button className="button small" type="button" disabled={pending || draft.components.some((component) => component.technology === "static")} onClick={() => addComponent("static")}>Add static site</button></div>
    </div>
    <div className="component-plans">
      {draft.components.map((component, index) => <ComponentSetupCard key={component.id} component={component} index={index} errors={visibleErrors} pending={pending} removable={draft.components.length > 1} allowStatic={component.technology === "static" || !draft.components.some((item) => item.technology === "static")} allowServer={component.technology !== "static" || !draft.components.some((item) => item.technology !== "static")} onChange={updateComponent} onRemove={() => removeComponent(index)} />)}
    </div>
    {hasDetectedSettings && <button className="text-button reset-detected" type="button" disabled={pending} onClick={resetDetected}>Reset to detected settings</button>}
    <details className="advanced-settings" open={advancedOpen} onToggle={(event) => setAdvancedOpen(event.currentTarget.open)}>
      <summary>Advanced settings</summary>
      <div className="advanced-settings-content">
        <div className="field">
          <label htmlFor="deployment-setup-migration-command">Migration command <span className="field-optional">(optional)</span></label>
          <input className="command-input" id="deployment-setup-migration-command" value={draft.migrationCommand ?? ""} disabled={pending} onChange={(event) => updateSetup((current) => ({ ...current, ...(event.target.value ? { migrationCommand: event.target.value } : { migrationCommand: undefined }) }))} />
          <small>Rig runs an accepted migration before new containers start. Approval remains a separate action.</small>
        </div>
        {draft.components.map((component, index) => <ComponentAdvanced key={component.id} component={component} index={index} errors={visibleErrors} pending={pending} onChange={updateComponent} />)}
      </div>
    </details>
    <p className="command-security-note">Commands run inside the project container, never directly on Windows. Leave install or build empty only when that phase should be skipped. Keep passwords and API keys in application configuration.</p>
    {!reviewed && <p className="plan-review-hint">Review setup validates these exact settings against the current source before Rig can accept them.</p>}
    <footer>
      {onBack && <button className="button" type="button" disabled={pending || draftSaved} onClick={onBack}>Back to source</button>}
      <button className="button primary" type="button" disabled={pending} onClick={submit}>{pending ? (reviewed ? "Accepting…" : "Reviewing…") : reviewed ? "Accept setup" : "Review setup"}</button>
    </footer>
    {onUseCompose && !draftSaved && <details className="other-strategies"><summary>Other setup options</summary><button className="text-button" type="button" disabled={pending} onClick={onUseCompose}>Use existing Compose setup</button></details>}
  </section>;
}

function ComponentSetupCard({ component, index, errors, pending, removable, allowStatic, allowServer, onChange, onRemove }: { component: DeploymentSetupComponentInput; index: number; errors: FieldErrors; pending: boolean; removable: boolean; allowStatic: boolean; allowServer: boolean; onChange: (index: number, field: keyof DeploymentSetupComponentInput, value: string | number) => void; onRemove: () => void }) {
  const error = (field: string) => errors[fieldKey(component.id, field)];
  const describe = (field: string) => error(field) ? `${fieldId(index, field)}-error` : undefined;
  const staticSite = component.technology === "static";
  return <article className="component-plan">
    <header><div><h3>{staticSite ? "Static site" : component.technology === "nextjs" ? "Next.js server" : "Node.js server"}</h3><p>{component.rootDirectory === "." ? "Repository root" : component.rootDirectory || "Choose a root directory"}</p></div>{removable && <button className="text-button inline" type="button" disabled={pending} onClick={onRemove}>Remove component</button>}</header>
    <div className="component-setup-grid">
      <div className="field"><label htmlFor={fieldId(index, "technology")}>Technology</label><select id={fieldId(index, "technology")} value={component.technology} disabled={pending} aria-invalid={Boolean(error("technology"))} aria-describedby={describe("technology")} onChange={(event) => onChange(index, "technology", event.target.value)}><option value="node" disabled={!allowServer}>Node.js</option><option value="nextjs" disabled={!allowServer}>Next.js</option><option value="static" disabled={!allowStatic}>Static site</option></select>{error("technology") && <p id={`${fieldId(index, "technology")}-error`} className="form-error">{error("technology")}</p>}</div>
      <div className="field"><label htmlFor={fieldId(index, "rootDirectory")}>Root directory</label><input id={fieldId(index, "rootDirectory")} value={component.rootDirectory} disabled={pending} aria-invalid={Boolean(error("rootDirectory"))} aria-describedby={describe("rootDirectory")} onChange={(event) => onChange(index, "rootDirectory", event.target.value)} />{error("rootDirectory") && <p id={`${fieldId(index, "rootDirectory")}-error`} className="form-error">{error("rootDirectory")}</p>}</div>
      <div className="field"><label htmlFor={fieldId(index, "packageManager")}>Package manager</label><select id={fieldId(index, "packageManager")} value={component.packageManager} disabled={pending} aria-invalid={Boolean(error("packageManager"))} aria-describedby={describe("packageManager")} onChange={(event) => onChange(index, "packageManager", event.target.value)}><option value="npm">npm</option><option value="pnpm">pnpm</option><option value="yarn">Yarn</option></select>{error("packageManager") && <p id={`${fieldId(index, "packageManager")}-error`} className="form-error">{error("packageManager")}</p>}</div>
      <div className="field"><label htmlFor={fieldId(index, "nodeVersion")}>Node.js version</label><select id={fieldId(index, "nodeVersion")} value={component.nodeVersion} disabled={pending} aria-invalid={Boolean(error("nodeVersion"))} aria-describedby={describe("nodeVersion")} onChange={(event) => onChange(index, "nodeVersion", event.target.value)}>{supportedNodeVersions.map((version) => <option value={version} key={version}>{version}</option>)}</select>{error("nodeVersion") && <p id={`${fieldId(index, "nodeVersion")}-error`} className="form-error">{error("nodeVersion")}</p>}</div>
    </div>
    <div className="field"><label htmlFor={fieldId(index, "installCommand")}>Install command <span className="field-optional">(optional)</span></label><input className="command-input" id={fieldId(index, "installCommand")} value={component.installCommand} disabled={pending} aria-invalid={Boolean(error("installCommand"))} aria-describedby={describe("installCommand")} onChange={(event) => onChange(index, "installCommand", event.target.value)} />{error("installCommand") && <p id={`${fieldId(index, "installCommand")}-error`} className="form-error">{error("installCommand")}</p>}<small>Leave empty to skip dependency installation.</small></div>
    <div className="field"><label htmlFor={fieldId(index, "buildCommand")}>Build command <span className="field-optional">(optional)</span></label><input className="command-input" id={fieldId(index, "buildCommand")} value={component.buildCommand} disabled={pending} aria-invalid={Boolean(error("buildCommand"))} aria-describedby={describe("buildCommand")} onChange={(event) => onChange(index, "buildCommand", event.target.value)} />{error("buildCommand") && <p id={`${fieldId(index, "buildCommand")}-error`} className="form-error">{error("buildCommand")}</p>}<small>Leave empty to skip the build step.</small></div>
    {staticSite ? <div className="field"><label htmlFor={fieldId(index, "outputDirectory")}>Output directory</label><input id={fieldId(index, "outputDirectory")} value={component.outputDirectory} disabled={pending} aria-invalid={Boolean(error("outputDirectory"))} aria-describedby={describe("outputDirectory")} onChange={(event) => onChange(index, "outputDirectory", event.target.value)} />{error("outputDirectory") && <p id={`${fieldId(index, "outputDirectory")}-error`} className="form-error">{error("outputDirectory")}</p>}<small>Rig’s managed static server serves this directory; there is no start command.</small></div> : <div className="field"><label htmlFor={fieldId(index, "startCommand")}>Start command</label><input className="command-input" id={fieldId(index, "startCommand")} value={component.startCommand} disabled={pending} aria-invalid={Boolean(error("startCommand"))} aria-describedby={describe("startCommand")} onChange={(event) => onChange(index, "startCommand", event.target.value)} />{error("startCommand") && <p id={`${fieldId(index, "startCommand")}-error`} className="form-error">{error("startCommand")}</p>}</div>}
  </article>;
}

function ComponentAdvanced({ component, index, errors, pending, onChange }: { component: DeploymentSetupComponentInput; index: number; errors: FieldErrors; pending: boolean; onChange: (index: number, field: keyof DeploymentSetupComponentInput, value: string | number) => void }) {
  const error = (field: string) => errors[fieldKey(component.id, field)];
  return <fieldset className="component-advanced"><legend>{component.rootDirectory || `Component ${index + 1}`}</legend>
    <div className="field"><label htmlFor={fieldId(index, "internalPort")}>Internal port</label><input id={fieldId(index, "internalPort")} type="number" min="1" max="65535" value={component.internalPort} disabled={pending} aria-invalid={Boolean(error("internalPort"))} aria-describedby={error("internalPort") ? `${fieldId(index, "internalPort")}-error` : undefined} onChange={(event) => onChange(index, "internalPort", event.target.value)} />{error("internalPort") && <p id={`${fieldId(index, "internalPort")}-error`} className="form-error">{error("internalPort")}</p>}</div>
    <div className="field"><label htmlFor={fieldId(index, "healthProbe")}>Health-check path</label><input id={fieldId(index, "healthProbe")} value={component.healthProbe} disabled={pending} aria-invalid={Boolean(error("healthProbe"))} aria-describedby={error("healthProbe") ? `${fieldId(index, "healthProbe")}-error` : undefined} onChange={(event) => onChange(index, "healthProbe", event.target.value)} />{error("healthProbe") && <p id={`${fieldId(index, "healthProbe")}-error`} className="form-error">{error("healthProbe")}</p>}</div>
  </fieldset>;
}

function SavedDraftNotice({ onOpen }: { onOpen: () => void }) {
  return <div className="callout info"><strong>Application draft saved</strong><span>Rig created this application before accepting its setup. Its source and application details are now locked in this wizard.</span><button className="button small" type="button" onClick={onOpen}>Open saved draft</button></div>;
}
