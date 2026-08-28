import { useEffect, useId, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { z } from "zod";
import {
  APIError,
  api,
  type RelayBindingStatus,
  type RelayEnrollmentStart,
  type RelayEnrollmentStatus,
  type RelayKeyRotationStatus,
  type RelayStatus,
  type StartRelayEnrollmentRequest,
} from "./api";
import { Dialog } from "./dialog";

const PAGE_SIZE = 30;
export const RELAY_POLL_INTERVAL_MS = 2_000;
export const RELAY_POLL_MAX_DURATION_MS = 5 * 60_000;
export const RELAY_POLL_MAX_ATTEMPTS = Math.ceil(RELAY_POLL_MAX_DURATION_MS / RELAY_POLL_INTERVAL_MS);

const dateTime = z.string().refine((value) => Number.isFinite(Date.parse(value)), "Invalid date-time");
const uuid = z.string().uuid();
const positiveId = z.number().int().positive();
const connectionId = z.string().regex(/^[a-f0-9]{32}$/);
const nonNegative = z.number().int().nonnegative();
const bindingSchema = z.object({
  bindingId: uuid,
  connectionId,
  installationId: positiveId,
  repositoryId: positiveId,
  state: z.enum(["authorized", "access_lost", "removal_pending"]),
  updatedAt: dateTime,
}).strict();
const rotationSummarySchema = z.object({
  inProgress: z.boolean(),
  state: z.enum(["prepare", "propose", "confirm", "new_key_auth", "finalize"]).optional(),
  expiresAt: dateTime.optional(),
  updatedAt: dateTime.optional(),
}).strict();
const relayStatusSchema = z.object({
  availability: z.enum(["initializing", "available", "unavailable"]),
  state: z.string().max(64),
  paused: z.boolean(),
  outcome: z.string().max(64),
  diagnosticsUnavailable: z.boolean(),
  pendingCommands: nonNegative,
  activeLeases: nonNegative,
  expiredLeases: nonNegative,
  oldestPendingAgeSeconds: nonNegative,
  observerDropped: nonNegative,
  readModelAvailable: z.boolean(),
  removableBindings: z.array(bindingSchema).max(1000),
  keyRotation: rotationSummarySchema,
}).strict();
const enrollmentStartSchema = z.object({
  enrollmentId: uuid,
  authorizationUrl: z.string(),
  status: z.literal("pending"),
  expiresAt: dateTime,
}).strict();
const enrollmentStatusSchema = z.object({
  enrollmentId: uuid,
  bindingId: uuid.optional(),
  status: z.enum(["pending", "authorized", "denied", "expired", "failed"]),
  createdAt: dateTime,
  expiresAt: dateTime,
  updatedAt: dateTime,
  completedAt: dateTime.optional(),
}).strict();
const bindingStatusSchema = z.object({
  bindingId: uuid,
  state: z.enum(["pending", "authorized", "denied", "expired", "removal_pending", "removed", "access_lost", "failed"]),
  updatedAt: dateTime,
}).strict();
const rotationStatusSchema = z.object({
  rotationId: uuid,
  state: z.enum(["prepare", "propose", "confirm", "new_key_auth", "finalize", "completed", "failed"]),
  expiresAt: dateTime,
}).strict();
const sourceConnectionSchema = z.object({
  id: connectionId,
  provider: z.literal("github"),
  status: z.enum(["pending", "connected", "denied", "expired", "disconnected", "access_lost"]),
  providerUserId: z.string().min(1).max(128).optional(),
  providerLogin: z.string().min(1).max(255).optional(),
  credentialGeneration: nonNegative,
  pendingExpiresAt: dateTime.optional(),
  nextPollAt: dateTime.optional(),
  accessExpiresAt: dateTime.optional(),
  refreshExpiresAt: dateTime.optional(),
  lastErrorCode: z.string().min(1).max(64).optional(),
  connectedAt: dateTime.optional(),
  disconnectedAt: dateTime.optional(),
  createdAt: dateTime,
  updatedAt: dateTime,
}).strict().transform(({ id, provider, status, providerLogin }) => ({ id, provider, status, providerLogin }));
const connectionsSchema = z.object({ items: z.array(sourceConnectionSchema) }).strict();
const installationPageSchema = z.object({
  page: positiveId,
  perPage: positiveId,
  totalCount: nonNegative,
  items: z.array(z.object({
    id: positiveId,
    accountLogin: z.string().min(1).max(255),
    accountType: z.enum(["User", "Organization", "Enterprise", "Bot"]),
    targetType: z.enum(["User", "Organization"]),
    repositorySelection: z.enum(["all", "selected"]),
    suspendedAt: dateTime.optional(),
    cachedAt: dateTime,
  }).strict().transform(({ id, accountLogin, repositorySelection }) => ({ id, accountLogin, repositorySelection }))).max(100),
}).strict();
const repositoryPageSchema = z.object({
  page: positiveId,
  perPage: positiveId,
  totalCount: nonNegative,
  items: z.array(z.object({ id: positiveId, owner: z.string(), name: z.string(), defaultBranch: z.string(), archived: z.boolean(), disabled: z.boolean(), private: z.boolean() }).strict().transform(({ id, owner, name, archived, disabled, private: privateRepository }) => ({ id, owner, name, archived, disabled, private: privateRepository }))).max(100),
}).strict();

type SafeRelayStatus = z.infer<typeof relayStatusSchema>;
type SafeBinding = z.infer<typeof bindingSchema>;
type EnrollmentTarget = { connectionId: string; installationId: number; repositoryId: number; repositoryLabel: string };
type EnrollmentSession = z.infer<typeof enrollmentStartSchema> & { attempts: number; startedAt: number; paused: boolean; delayMs: number; target: EnrollmentTarget };
type EnrollmentOutcome = { status: "authorized" | "denied" | "expired" | "failed" };
type OperationStatus = { message: string; tone: "info" | "success" };
type PollingOptions = { intervalMs?: number; maxAttempts?: number; maxDurationMs?: number };
type DurableStatusObservation = { seeded: boolean; rotationInProgress: boolean; pendingRemovalBindingIds: Set<string> };
type RelayErrorOperation = "status" | "connections" | "installations" | "repositories" | "enrollmentStart" | "enrollmentPoll" | "bindingRemoval" | "keyRotation";
type StatusPollReason = "initializing" | "binding_removal" | "key_rotation" | "relay_operation";

type RelayAPI = {
  relayStatus: () => Promise<RelayStatus>;
  sourceConnections: typeof api.sourceConnections;
  githubInstallations: typeof api.githubInstallations;
  githubRepositories: typeof api.githubRepositories;
  startRelayEnrollment: (request: StartRelayEnrollmentRequest) => Promise<RelayEnrollmentStart>;
  pollRelayEnrollment: (enrollmentId: string) => Promise<RelayEnrollmentStatus>;
  removeRelayBinding: (bindingId: string) => Promise<RelayBindingStatus>;
  startRelayKeyRotation: () => Promise<RelayKeyRotationStatus>;
};

class UnsupportedRelayResponse extends Error {
  constructor() {
    super("The controller returned an unsupported relay response.");
  }
}

function parse<T>(schema: z.ZodType<T>, value: unknown): T {
  const result = schema.safeParse(value);
  if (!result.success) throw new UnsupportedRelayResponse();
  return result.data;
}

function canonicalBase64URL32(value: string) {
  if (!/^[A-Za-z0-9_-]{43}$/.test(value)) return false;
  try {
    const decoded = atob(`${value.replaceAll("-", "+").replaceAll("_", "/")}=`);
    if (decoded.length !== 32) return false;
    return btoa(decoded).replaceAll("+", "-").replaceAll("/", "_").replace(/=+$/, "") === value;
  } catch {
    return false;
  }
}

function canonicalRelayCallback(value: string) {
  try {
    const parsed = new URL(value);
    return parsed.protocol === "https:" && Boolean(parsed.hostname) && !parsed.hostname.endsWith(".") && !parsed.username && !parsed.password && parsed.pathname === "/v1/github/callback" && !parsed.search && !parsed.hash && parsed.href === value;
  } catch {
    return false;
  }
}

export function githubAuthorizationURL(value: string, repositoryId: number): string | null {
  const endpoint = "https://github.com/login/oauth/authorize";
  if (!value || value.length > 4096 || value.includes("\0") || !Number.isSafeInteger(repositoryId) || repositoryId <= 0) return null;
  try {
    const parsed = new URL(value);
    if (parsed.protocol !== "https:" || parsed.host !== "github.com" || parsed.origin !== "https://github.com" || parsed.username || parsed.password || parsed.pathname !== "/login/oauth/authorize" || parsed.hash || parsed.href !== value) return null;
    if (!value.startsWith(`${endpoint}?`)) return null;
    const rawQuery = value.slice(endpoint.length + 1);
    if (!rawQuery) return null;
    const canonicalQuery = new URLSearchParams(rawQuery);
    canonicalQuery.sort();
    if (canonicalQuery.toString() !== rawQuery) return null;
    const expectedKeys = ["client_id", "code_challenge", "code_challenge_method", "redirect_uri", "repository_id", "state"];
    const keys = [...canonicalQuery.keys()];
    if (keys.length !== expectedKeys.length || keys.some((key, index) => key !== expectedKeys[index])) return null;
    if (expectedKeys.some((key) => canonicalQuery.getAll(key).length !== 1)) return null;
    const clientID = canonicalQuery.get("client_id") ?? "";
    const challenge = canonicalQuery.get("code_challenge") ?? "";
    const redirectURI = canonicalQuery.get("redirect_uri") ?? "";
    const state = canonicalQuery.get("state") ?? "";
    if (!/^[A-Za-z0-9._-]{1,255}$/.test(clientID) || canonicalQuery.get("code_challenge_method") !== "S256" || canonicalQuery.get("repository_id") !== String(repositoryId) || !canonicalBase64URL32(state) || !canonicalBase64URL32(challenge) || !canonicalRelayCallback(redirectURI)) return null;
    return parsed.href;
  } catch {
    return null;
  }
}

function displayTime(value?: string) {
  if (!value) return "Unavailable";
  const timestamp = Date.parse(value);
  return Number.isFinite(timestamp)
    ? new Date(timestamp).toLocaleString([], { dateStyle: "medium", timeStyle: "short" })
    : "Unavailable";
}

function humanizeState(value?: string) {
  return value ? value.replaceAll("_", " ") : "Unavailable";
}

const relayErrorFallbacks: Record<RelayErrorOperation, string> = {
  status: "Relay status could not be loaded.",
  connections: "GitHub connections could not be loaded.",
  installations: "GitHub App installations could not be loaded.",
  repositories: "Repositories could not be loaded.",
  enrollmentStart: "Could not start relay authorization.",
  enrollmentPoll: "Could not check relay authorization.",
  bindingRemoval: "Could not remove the relay binding.",
  keyRotation: "Could not start controller key rotation.",
};

const relayErrorMessages = new Map<string, string>([
  ["relay_unavailable", "The relay is unavailable. Check relay status and try again."],
  ["provider_unavailable", "GitHub is temporarily unavailable. Try again."],
  ["rate_limited", "GitHub temporarily limited this operation. Wait and try again."],
  ["poll_too_soon", "The request was checked too soon. Wait and try again."],
  ["source_access_lost", "GitHub access was lost. Reconnect or reauthorize the source, refresh relay status, and try again."],
  ["authentication_required", "GitHub authorization is required. Reconnect or reauthorize the source and try again."],
  ["connection_not_found", "The selected GitHub connection is no longer available. Refresh connected sources and choose again."],
  ["invalid_connection_state", "The selected GitHub connection is not ready. Refresh connected sources and choose again."],
  ["invalid_source", "The selected GitHub source is no longer available. Refresh the source list and choose again."],
  ["relay_enrollment_not_found", "This relay authorization request is no longer available. Start again."],
  ["relay_binding_not_found", "This relay binding is no longer available. Refresh relay status."],
  ["relay_state_conflict", "Relay state changed before the operation completed. Refresh relay status and try again."],
  ["relay_prerequisite_missing", "Relay prerequisites are unavailable. Refresh relay status and try again."],
  ["invalid_relay_request", "The controller rejected this relay request. Refresh relay status and try again."],
  ["unauthenticated", "Your Rig session is no longer authenticated. Sign in again."],
  ["csrf_failed", "The request could not be verified. Refresh this page and try again."],
]);

function relayErrorMessage(error: unknown, operation: RelayErrorOperation) {
  if (error instanceof UnsupportedRelayResponse) return error.message;
  if (error instanceof APIError) return relayErrorMessages.get(error.code) ?? relayErrorFallbacks[operation];
  return relayErrorFallbacks[operation];
}

export function relayRetryDelay(error: unknown, minimumMs = RELAY_POLL_INTERVAL_MS) {
  if (!(error instanceof APIError) || !error.retryAfterSeconds) return minimumMs;
  return Math.min(Math.max(error.retryAfterSeconds * 1000, minimumMs), 30_000);
}

export function relayPollLimitReached(attempts: number, startedAt: number, now = Date.now(), maxAttempts = RELAY_POLL_MAX_ATTEMPTS, maxDurationMs = RELAY_POLL_MAX_DURATION_MS) {
  return attempts >= maxAttempts || now - startedAt >= maxDurationMs;
}

function Pagination({ label, page, hasNext, loading, disabled = false, change }: { label: string; page: number; hasNext: boolean; loading: boolean; disabled?: boolean; change: (page: number) => void }) {
  if (page === 1 && !hasNext && !loading) return null;
  return <nav className="relay-pagination" aria-label={`${label} pagination`} aria-busy={loading}>
    <button className="button small" type="button" disabled={disabled || loading || page === 1} onClick={() => change(page - 1)}>Previous</button>
    <span>Page {page}</span>
    <button className="button small" type="button" disabled={disabled || loading || !hasNext} onClick={() => change(page + 1)}>Next</button>
  </nav>;
}

function RelayBindingCard({ binding, select }: { binding: SafeBinding; select: (binding: SafeBinding) => void }) {
  const headingId = useId();
  return <article className="relay-binding" aria-labelledby={headingId}>
    <div className="relay-binding-heading"><h4 id={headingId}>Repository {binding.repositoryId}</h4><span className={`relay-state ${binding.state}`}>{humanizeState(binding.state)}</span></div>
    <dl><div><dt>Connection</dt><dd className="mono">{binding.connectionId}</dd></div><div><dt>Installation</dt><dd>{binding.installationId}</dd></div><div><dt>Repository</dt><dd>{binding.repositoryId}</dd></div><div><dt>Updated</dt><dd>{displayTime(binding.updatedAt)}</dd></div></dl>
    {binding.state === "access_lost" && <p className="relay-muted">Reconnect or reauthorize the GitHub source, then refresh relay status before retrying.</p>}
    <button className="button small" type="button" disabled={binding.state === "removal_pending"} onClick={() => select(binding)}>Remove binding</button>
  </article>;
}

export function RelayManagementPanel({ role, client = api, polling = {} }: { role: string; client?: RelayAPI; polling?: PollingOptions }) {
  const rotationDescriptionId = useId();
  const mounted = useRef(true);
  const statusEpoch = useRef(0);
  const enrollmentEpoch = useRef(0);
  const removalEpoch = useRef(0);
  const rotationEpoch = useRef(0);
  const enrollmentStartInFlight = useRef<number | null>(null);
  const enrollmentPollInFlight = useRef<number | null>(null);
  const removalInFlight = useRef(false);
  const rotationInFlight = useRef(false);
  const statusPoll = useRef({ attempts: 0, startedAt: 0 });
  const durableStatusObservation = useRef<DurableStatusObservation>({ seeded: false, rotationInProgress: false, pendingRemovalBindingIds: new Set() });
  const pollIntervalMs = polling.intervalMs ?? RELAY_POLL_INTERVAL_MS;
  const pollMaxAttempts = polling.maxAttempts ?? RELAY_POLL_MAX_ATTEMPTS;
  const pollMaxDurationMs = polling.maxDurationMs ?? RELAY_POLL_MAX_DURATION_MS;
  const [connection, setConnection] = useState("");
  const [installation, setInstallation] = useState<number | null>(null);
  const [repository, setRepository] = useState<number | null>(null);
  const [installationPage, setInstallationPage] = useState(1);
  const [repositoryPage, setRepositoryPage] = useState(1);
  const [enrollment, setEnrollment] = useState<EnrollmentSession | null>(null);
  const [enrollmentStarting, setEnrollmentStarting] = useState(false);
  const [enrollmentOutcome, setEnrollmentOutcome] = useState<EnrollmentOutcome | null>(null);
  const [enrollmentError, setEnrollmentError] = useState("");
  const [enrollmentRecovery, setEnrollmentRecovery] = useState<"resume" | "restart" | null>(null);
  const [selectedBinding, setSelectedBinding] = useState<SafeBinding | null>(null);
  const [removing, setRemoving] = useState(false);
  const [removalError, setRemovalError] = useState("");
  const [rotationPending, setRotationPending] = useState(false);
  const [rotationError, setRotationError] = useState("");
  const [operationStatus, setOperationStatus] = useState<OperationStatus | null>(null);
  const [statusPolling, setStatusPolling] = useState(false);
  const [statusPollStoppedReason, setStatusPollStoppedReason] = useState<StatusPollReason | null>(null);
  const removalErrorRef = useRef<HTMLDivElement>(null);
  const rotationErrorRef = useRef<HTMLDivElement>(null);
  const enrollmentOutcomeErrorRef = useRef<HTMLDivElement>(null);
  const operationStatusRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
      statusEpoch.current += 1;
      enrollmentEpoch.current += 1;
      removalEpoch.current += 1;
      rotationEpoch.current += 1;
    };
  }, []);

  const statusQuery = useQuery({
    queryKey: ["relay-management-status"],
    queryFn: async () => parse(relayStatusSchema, await client.relayStatus()),
    retry: false,
  });
  const status = statusQuery.data;

  useEffect(() => {
    if (!status || status.availability !== "available" || !status.readModelAvailable) return;
    const current: DurableStatusObservation = {
      seeded: true,
      rotationInProgress: status.keyRotation.inProgress,
      pendingRemovalBindingIds: new Set(status.removableBindings.filter((binding) => binding.state === "removal_pending").map((binding) => binding.bindingId)),
    };
    const previous = durableStatusObservation.current;
    durableStatusObservation.current = current;
    if (!previous.seeded) return;
    const messages: string[] = [];
    let success = true;
    if (previous.rotationInProgress && !current.rotationInProgress) {
      messages.push("Controller key rotation is no longer in progress.");
      success = false;
    }
    let removalCompleted = false;
    const authoritativeStates = new Set<SafeBinding["state"]>();
    for (const bindingID of previous.pendingRemovalBindingIds) {
      if (current.pendingRemovalBindingIds.has(bindingID)) continue;
      const binding = status.removableBindings.find((candidate) => candidate.bindingId === bindingID);
      if (binding) authoritativeStates.add(binding.state);
      else removalCompleted = true;
    }
    if (removalCompleted) messages.push("Relay binding removal completed.");
    for (const bindingState of authoritativeStates) {
      messages.push(bindingState === "access_lost"
        ? "Relay binding removal is no longer pending because GitHub access was lost. Reconnect or reauthorize the source, then refresh relay status before retrying."
        : `Relay binding removal is no longer pending; its authoritative state is ${humanizeState(bindingState)}. Refresh relay status before retrying the removal.`);
      success = false;
    }
    if (messages.length > 0) setOperationStatus({ message: messages.join(" "), tone: success ? "success" : "info" });
  }, [status]);

  const lifecyclePolling = status?.availability === "initializing";
  const pendingRemovalPolling = status?.removableBindings.some((item) => item.state === "removal_pending") === true;
  const rotationPolling = status?.keyRotation.inProgress === true;
  const statusPollReason: StatusPollReason | null = lifecyclePolling ? "initializing" : pendingRemovalPolling ? "binding_removal" : rotationPolling ? "key_rotation" : statusPolling ? "relay_operation" : null;
  const automaticStatusPolling = statusPollReason !== null;
  const transientPolling = automaticStatusPolling && statusPollStoppedReason === null;

  useEffect(() => {
    if (!transientPolling || statusQuery.isError) return;
    if (!statusPoll.current.startedAt) statusPoll.current.startedAt = Date.now();
    if (relayPollLimitReached(statusPoll.current.attempts, statusPoll.current.startedAt, Date.now(), pollMaxAttempts, pollMaxDurationMs)) {
      setStatusPollStoppedReason(statusPollReason ?? "relay_operation");
      return;
    }
    const epoch = statusEpoch.current;
    const timer = window.setTimeout(async () => {
      statusPoll.current.attempts += 1;
      const result = await statusQuery.refetch();
      if (!mounted.current || epoch !== statusEpoch.current) return;
      if (result.isError) setStatusPolling(false);
      if (result.data && result.data.availability !== "initializing" && !result.data.keyRotation.inProgress && !result.data.removableBindings.some((item) => item.state === "removal_pending")) {
        setStatusPolling(false);
        setStatusPollStoppedReason(null);
        statusPoll.current = { attempts: 0, startedAt: 0 };
      }
    }, pollIntervalMs);
    return () => window.clearTimeout(timer);
  }, [pollIntervalMs, pollMaxAttempts, pollMaxDurationMs, statusPollReason, statusQuery.dataUpdatedAt, statusQuery.isError, statusPolling, transientPolling]);

  const connectionsQuery = useQuery({
    queryKey: ["relay-source-connections"],
    queryFn: async () => parse(connectionsSchema, await client.sourceConnections()),
    enabled: status?.availability === "available" && status.readModelAvailable,
    retry: false,
  });
  const connected = connectionsQuery.data?.items.filter((item) => item.provider === "github" && item.status === "connected") ?? [];
  const installationsQuery = useQuery({
    queryKey: ["relay-installations", connection, installationPage],
    queryFn: async () => parse(installationPageSchema, await client.githubInstallations(connection, installationPage, PAGE_SIZE)),
    enabled: Boolean(connection),
    retry: false,
  });
  const repositoriesQuery = useQuery({
    queryKey: ["relay-repositories", connection, installation, repositoryPage],
    queryFn: async () => parse(repositoryPageSchema, await client.githubRepositories(connection, installation!, repositoryPage, PAGE_SIZE)),
    enabled: Boolean(connection) && installation !== null,
    retry: false,
  });
  const repositories = repositoriesQuery.data?.items.filter((item) => !item.archived && !item.disabled) ?? [];
  const selectedRepository = repositories.find((item) => item.id === repository);
  const sourceStatus = (() => {
    if (connectionsQuery.isFetching) return "Loading connected GitHub sources…";
    if (connectionsQuery.isError) return "";
    if (connected.length === 0) return "No connected GitHub sources are available. Connect GitHub while adding an application, then return here.";
    if (!connection) return `${connected.length} connected GitHub ${connected.length === 1 ? "source is" : "sources are"} available. Choose a source to continue.`;
    if (installationsQuery.isFetching) return "Loading GitHub App installations for the selected source…";
    if (installationsQuery.isError) return "";
    const installationCount = installationsQuery.data?.items.length ?? 0;
    if (installationCount === 0) return `No eligible GitHub App installations are available on page ${installationPage}.`;
    if (installation === null) return `${installationCount} GitHub App ${installationCount === 1 ? "installation is" : "installations are"} available on page ${installationPage}. Choose an installation to continue.`;
    if (repositoriesQuery.isFetching) return "Loading repositories for the selected installation…";
    if (repositoriesQuery.isError) return "";
    if (repositories.length === 0) return `No eligible repositories are available on page ${repositoryPage}.`;
    return `${repositories.length} eligible ${repositories.length === 1 ? "repository is" : "repositories are"} available on page ${repositoryPage}.`;
  })();

  const resetEnrollmentForScopeChange = () => {
    enrollmentEpoch.current += 1;
    enrollmentStartInFlight.current = null;
    enrollmentPollInFlight.current = null;
    setEnrollmentStarting(false);
    setEnrollment(null);
    setEnrollmentError("");
    setEnrollmentRecovery(null);
    setEnrollmentOutcome(null);
  };

  const stopEnrollmentForRestart = (message: string) => {
    enrollmentEpoch.current += 1;
    enrollmentStartInFlight.current = null;
    enrollmentPollInFlight.current = null;
    setEnrollmentStarting(false);
    setEnrollment(null);
    setEnrollmentError(message);
    setEnrollmentRecovery("restart");
  };

  const beginEnrollment = async () => {
    if (enrollmentStartInFlight.current !== null || enrollment || !connection || installation === null || repository === null) return;
    const epoch = ++enrollmentEpoch.current;
    enrollmentStartInFlight.current = epoch;
    const target: EnrollmentTarget = {
      connectionId: connection,
      installationId: installation,
      repositoryId: repository,
      repositoryLabel: selectedRepository ? `${selectedRepository.owner}/${selectedRepository.name}` : `Repository ${repository}`,
    };
    setEnrollmentStarting(true);
    setEnrollmentError("");
    setEnrollmentRecovery(null);
    setEnrollmentOutcome(null);
    try {
      const result = parse(enrollmentStartSchema, await client.startRelayEnrollment({ connectionId: target.connectionId, installationId: target.installationId, repositoryId: target.repositoryId }));
      if (!githubAuthorizationURL(result.authorizationUrl, target.repositoryId)) throw new UnsupportedRelayResponse();
      if (!mounted.current || epoch !== enrollmentEpoch.current || enrollmentStartInFlight.current !== epoch) return;
      if (Date.parse(result.expiresAt) <= Date.now()) {
        stopEnrollmentForRestart("Relay authorization expired before it could be opened. Start again.");
        return;
      }
      setEnrollment({ ...result, attempts: 0, startedAt: Date.now(), paused: false, delayMs: pollIntervalMs, target });
    } catch (error) {
      if (mounted.current && epoch === enrollmentEpoch.current && enrollmentStartInFlight.current === epoch) {
        setEnrollmentError(relayErrorMessage(error, "enrollmentStart"));
        setEnrollmentRecovery("restart");
      }
    } finally {
      if (enrollmentStartInFlight.current === epoch) {
        enrollmentStartInFlight.current = null;
        if (mounted.current) setEnrollmentStarting(false);
      }
    }
  };

  const pollEnrollment = async (session: EnrollmentSession) => {
    if (enrollmentPollInFlight.current !== null) return;
    if (Date.parse(session.expiresAt) <= Date.now()) {
      stopEnrollmentForRestart("Relay authorization expired. Start again to create a new authorization request.");
      return;
    }
    if (relayPollLimitReached(session.attempts, session.startedAt, Date.now(), pollMaxAttempts, pollMaxDurationMs)) {
      stopEnrollmentForRestart("Automatic authorization checks reached their polling limit. Start again to create a new bounded request.");
      return;
    }
    const epoch = enrollmentEpoch.current;
    enrollmentPollInFlight.current = epoch;
    try {
      const result = parse(enrollmentStatusSchema, await client.pollRelayEnrollment(session.enrollmentId));
      if (result.enrollmentId !== session.enrollmentId) throw new UnsupportedRelayResponse();
      if (!mounted.current || epoch !== enrollmentEpoch.current || enrollmentPollInFlight.current !== epoch) return;
      if (result.status === "pending") {
        const nextAttempts = session.attempts + 1;
        const now = Date.now();
        if (Date.parse(result.expiresAt) <= now) {
          stopEnrollmentForRestart("Relay authorization expired. Start again to create a new authorization request.");
          return;
        }
        if (relayPollLimitReached(nextAttempts, session.startedAt, now, pollMaxAttempts, pollMaxDurationMs)) {
          stopEnrollmentForRestart("Automatic authorization checks reached their polling limit. Start again to create a new bounded request.");
          return;
        }
        setEnrollment((current) => current && current.enrollmentId === session.enrollmentId ? { ...current, attempts: nextAttempts, expiresAt: result.expiresAt, paused: false, delayMs: pollIntervalMs } : current);
      } else {
        setEnrollment(null);
        setEnrollmentRecovery(null);
        setEnrollmentError("");
        setEnrollmentOutcome({ status: result.status });
        if (result.status === "failed") window.setTimeout(() => enrollmentOutcomeErrorRef.current?.focus(), 0);
        if (result.status === "authorized") {
          await statusQuery.refetch();
        }
      }
    } catch (error) {
      if (mounted.current && epoch === enrollmentEpoch.current && enrollmentPollInFlight.current === epoch) {
        setEnrollment((current) => current && current.enrollmentId === session.enrollmentId ? { ...current, attempts: current.attempts + 1, paused: true, delayMs: relayRetryDelay(error, pollIntervalMs) } : current);
        setEnrollmentError(relayErrorMessage(error, "enrollmentPoll"));
        setEnrollmentRecovery("resume");
      }
    } finally {
      if (enrollmentPollInFlight.current === epoch) enrollmentPollInFlight.current = null;
    }
  };

  useEffect(() => {
    if (!enrollment || enrollment.paused) return;
    const timer = window.setTimeout(() => void pollEnrollment(enrollment), enrollment.delayMs);
    return () => window.clearTimeout(timer);
  }, [enrollment]);

  const resumeEnrollment = () => {
    if (!enrollment) return;
    if (Date.parse(enrollment.expiresAt) <= Date.now()) {
      stopEnrollmentForRestart("Relay authorization expired. Start again to create a new authorization request.");
      return;
    }
    enrollmentEpoch.current += 1;
    enrollmentPollInFlight.current = null;
    setEnrollmentError("");
    setEnrollmentRecovery(null);
    setEnrollment((current) => current ? { ...current, attempts: 0, startedAt: Date.now(), paused: false } : null);
  };

  const restartEnrollment = () => {
    enrollmentEpoch.current += 1;
    enrollmentStartInFlight.current = null;
    enrollmentPollInFlight.current = null;
    setEnrollment(null);
    setEnrollmentError("");
    setEnrollmentRecovery(null);
    setEnrollmentOutcome(null);
    window.setTimeout(() => void beginEnrollment(), 0);
  };

  const removeBinding = async () => {
    if (!selectedBinding || removalInFlight.current) return;
    removalInFlight.current = true;
    const epoch = ++removalEpoch.current;
    setRemoving(true);
    setRemovalError("");
    setOperationStatus(null);
    const target = selectedBinding;
    try {
      const result = parse(bindingStatusSchema, await client.removeRelayBinding(target.bindingId));
      if (result.bindingId !== target.bindingId) throw new UnsupportedRelayResponse();
      if (!mounted.current || epoch !== removalEpoch.current) return;
      setSelectedBinding(null);
      setOperationStatus(result.state === "removed"
        ? { message: "Relay binding removed.", tone: "success" }
        : { message: "Relay binding removal requested.", tone: "info" });
      window.setTimeout(() => operationStatusRef.current?.focus(), 0);
      statusEpoch.current += 1;
      statusPoll.current = { attempts: 0, startedAt: Date.now() };
      setStatusPollStoppedReason(null);
      setStatusPolling(result.state === "removal_pending");
      await statusQuery.refetch();
    } catch (error) {
      if (mounted.current && epoch === removalEpoch.current) {
        setRemovalError(relayErrorMessage(error, "bindingRemoval"));
        window.setTimeout(() => removalErrorRef.current?.focus(), 0);
      }
    } finally {
      if (epoch === removalEpoch.current) {
        removalInFlight.current = false;
        if (mounted.current) setRemoving(false);
      }
    }
  };

  const rotateKey = async () => {
    if (role !== "administrator" || rotationInFlight.current || status?.keyRotation.inProgress || !status || status.availability !== "available" || !status.readModelAvailable) return;
    rotationInFlight.current = true;
    const epoch = ++rotationEpoch.current;
    setRotationPending(true);
    setRotationError("");
    setOperationStatus(null);
    try {
      const result = parse(rotationStatusSchema, await client.startRelayKeyRotation());
      if (!mounted.current || epoch !== rotationEpoch.current) return;
      const rotationLive = ["prepare", "propose", "confirm", "new_key_auth", "finalize"].includes(result.state);
      if (rotationLive) {
        setOperationStatus({ message: `Controller key rotation is in progress (${humanizeState(result.state)}).`, tone: "info" });
      } else if (result.state === "completed") {
        setOperationStatus({ message: "Controller key rotation completed.", tone: "success" });
      } else {
        setRotationError("Controller key rotation failed. Review relay status and try again.");
        window.setTimeout(() => rotationErrorRef.current?.focus(), 0);
      }
      statusEpoch.current += 1;
      statusPoll.current = { attempts: 0, startedAt: Date.now() };
      setStatusPollStoppedReason(null);
      setStatusPolling(rotationLive);
      await statusQuery.refetch();
    } catch (error) {
      if (mounted.current && epoch === rotationEpoch.current) {
        setRotationError(relayErrorMessage(error, "keyRotation"));
        window.setTimeout(() => rotationErrorRef.current?.focus(), 0);
      }
    } finally {
      if (epoch === rotationEpoch.current) {
        rotationInFlight.current = false;
        if (mounted.current) setRotationPending(false);
      }
    }
  };

  const authorizationURL = enrollment ? githubAuthorizationURL(enrollment.authorizationUrl, enrollment.target.repositoryId) : null;
  const enrollmentReady = Boolean(connection && installation && repository);
  const scopeLocked = Boolean(enrollment);
  const rotationReady = status?.availability === "available" && status.readModelAvailable;
  const rotationInProgress = status?.keyRotation.inProgress ?? false;
  const rotationState = !rotationReady ? "Unavailable" : rotationInProgress ? humanizeState(status?.keyRotation.state ?? "in_progress") : "Idle";
  const statusPollStoppedMessage = statusPollStoppedReason === "initializing"
    ? "The bounded status-check window ended while the relay was still initializing."
    : statusPollStoppedReason === "binding_removal"
      ? "The bounded status-check window ended while a relay binding removal remained pending."
      : statusPollStoppedReason === "key_rotation"
        ? "The bounded status-check window ended while controller key rotation remained in progress."
        : "The bounded status-check window ended while an active relay operation still needed status updates.";

  const resumeStatusChecks = () => {
    statusEpoch.current += 1;
    statusPoll.current = { attempts: 0, startedAt: Date.now() };
    setStatusPollStoppedReason(null);
  };

  return <section className="relay-panel" aria-labelledby="relay-title">
    <div className="relay-heading">
      <div><h2 id="relay-title">GitHub deployment relay</h2><p>Authorize repository event delivery to this controller and manage its durable relay access.</p></div>
      <button className="button small" type="button" disabled={statusQuery.isFetching} onClick={() => { statusEpoch.current += 1; statusPoll.current = { attempts: 0, startedAt: 0 }; setStatusPollStoppedReason(null); void statusQuery.refetch(); }}>{statusQuery.isFetching ? "Refreshing…" : "Refresh relay status"}</button>
    </div>
    {statusQuery.isLoading ? <div className="relay-loading" role="status" aria-busy="true"><span className="sr-only">Loading relay status</span><i/><i/></div>
      : statusQuery.isError ? <div className="callout danger relay-status-error" role="alert"><strong>Relay status unavailable</strong><span>{relayErrorMessage(statusQuery.error, "status")}</span><button className="button small" type="button" onClick={() => void statusQuery.refetch()}>Retry</button></div>
        : status ? <RelayOverview status={status}/> : null}
    {statusPollStoppedReason && automaticStatusPolling && <div className="callout warning relay-status-error" role="status"><strong>Automatic status checks paused</strong><span>{statusPollStoppedMessage} Resume to start a new bounded status-check window.</span><button className="button small" type="button" onClick={resumeStatusChecks}>Resume status checks</button></div>}
    {operationStatus && <div ref={operationStatusRef} className={`callout ${operationStatus.tone} relay-operation-status`} role="status" tabIndex={-1}>{operationStatus.message}</div>}

    {status?.availability === "available" && status.readModelAvailable && <>
      <div className="relay-section">
        <div className="relay-section-heading"><div><h3>Repository enrollment</h3><p>Pair one connected GitHub repository with the relay. Authorization progress is held only in this browser tab.</p></div></div>
        <p className="relay-muted relay-source-status" role="status" aria-live="polite" aria-atomic="true" aria-busy={connectionsQuery.isFetching || installationsQuery.isFetching || repositoriesQuery.isFetching}>{sourceStatus}</p>
        {connectionsQuery.isError ? <div className="callout danger" role="alert"><strong>Connected sources unavailable</strong><span>{relayErrorMessage(connectionsQuery.error, "connections")}</span><button className="button small" type="button" onClick={() => void connectionsQuery.refetch()}>Retry connections</button></div>
          : connected.length > 0 && <div className="relay-source-grid">
                <div className="field"><label htmlFor="relay-connection">GitHub connection</label><select id="relay-connection" value={connection} disabled={scopeLocked} onChange={(event) => { resetEnrollmentForScopeChange(); setConnection(event.target.value); setInstallation(null); setRepository(null); setInstallationPage(1); setRepositoryPage(1); }}><option value="">Choose a connected source</option>{connected.map((item) => <option key={item.id} value={item.id}>{item.providerLogin ? `@${item.providerLogin}` : "Connected GitHub source"}</option>)}</select></div>
                {connection && <><div className="field"><label htmlFor="relay-installation">GitHub App installation</label><select id="relay-installation" value={installation ?? ""} disabled={scopeLocked || installationsQuery.isFetching || installationsQuery.isError} onChange={(event) => { resetEnrollmentForScopeChange(); setInstallation(event.target.value ? Number(event.target.value) : null); setRepository(null); setRepositoryPage(1); }}><option value="">{installationsQuery.isFetching ? "Loading installations…" : installationsQuery.isError ? "Installations unavailable" : "Choose an installation"}</option>{installationsQuery.data?.items.map((item) => <option key={item.id} value={item.id}>{item.accountLogin} ({item.repositorySelection})</option>)}</select></div><Pagination label="installations" page={installationPage} loading={installationsQuery.isFetching} disabled={scopeLocked} hasNext={(installationsQuery.data?.page ?? 0) * (installationsQuery.data?.perPage ?? PAGE_SIZE) < (installationsQuery.data?.totalCount ?? 0)} change={(page) => { resetEnrollmentForScopeChange(); setInstallationPage(page); setInstallation(null); setRepository(null); setRepositoryPage(1); }}/></>}
                {connection && installationsQuery.isError && <div className="callout danger" role="alert"><strong>Installations unavailable</strong><span>{relayErrorMessage(installationsQuery.error, "installations")}</span><button className="button small" type="button" onClick={() => void installationsQuery.refetch()}>Retry installations</button></div>}
                {installation !== null && <><div className="field"><label htmlFor="relay-repository">Repository</label><select id="relay-repository" value={repository ?? ""} disabled={scopeLocked || repositoriesQuery.isFetching || repositoriesQuery.isError} onChange={(event) => { resetEnrollmentForScopeChange(); setRepository(event.target.value ? Number(event.target.value) : null); }}><option value="">{repositoriesQuery.isFetching ? "Loading repositories…" : repositoriesQuery.isError ? "Repositories unavailable" : "Choose a repository"}</option>{repositories.map((item) => <option key={item.id} value={item.id}>{item.owner}/{item.name}{item.private ? " (private)" : ""}</option>)}</select></div><Pagination label="repositories" page={repositoryPage} loading={repositoriesQuery.isFetching} disabled={scopeLocked} hasNext={(repositoriesQuery.data?.page ?? 0) * (repositoriesQuery.data?.perPage ?? PAGE_SIZE) < (repositoriesQuery.data?.totalCount ?? 0)} change={(page) => { resetEnrollmentForScopeChange(); setRepositoryPage(page); setRepository(null); }}/></>}
                {installation !== null && repositoriesQuery.isError && <div className="callout danger" role="alert"><strong>Repositories unavailable</strong><span>{relayErrorMessage(repositoriesQuery.error, "repositories")}</span><button className="button small" type="button" onClick={() => void repositoriesQuery.refetch()}>Retry repositories</button></div>}
                <div className="relay-enrollment-actions"><button className="button primary" type="button" disabled={!enrollmentReady || enrollmentStarting || Boolean(enrollment) || enrollmentRecovery === "restart"} onClick={() => void beginEnrollment()}>{enrollmentStarting ? "Starting authorization…" : "Start relay authorization"}</button>{selectedRepository && <span>Selected {selectedRepository.owner}/{selectedRepository.name}</span>}</div>
              </div>}
        {enrollment && authorizationURL && <div className="callout info relay-enrollment-status" role="status" aria-live="polite"><strong>Complete GitHub authorization</strong><span><a href={authorizationURL} target="_blank" rel="noopener noreferrer">Open GitHub authorization (opens in a new tab)</a></span><dl className="relay-enrollment-target"><div><dt>Connection</dt><dd className="mono">{enrollment.target.connectionId}</dd></div><div><dt>Installation</dt><dd>{enrollment.target.installationId}</dd></div><div><dt>Repository</dt><dd>{enrollment.target.repositoryLabel} ({enrollment.target.repositoryId})</dd></div></dl><span>Expires {displayTime(enrollment.expiresAt)}. Keep this tab open while Rig checks the result.</span><span>Reloading this page cannot rediscover a pending authorization. An authorized binding will appear here after status refresh.</span>{enrollment.paused && !enrollmentError && <button className="button small" type="button" onClick={resumeEnrollment}>Resume authorization check</button>}</div>}
        {enrollmentError && <div className="callout danger" role="alert"><strong>Relay authorization needs attention</strong><span>{enrollmentError}</span>{enrollmentRecovery === "resume" && enrollment && <button className="button small" type="button" onClick={resumeEnrollment}>Resume authorization check</button>}{enrollmentRecovery === "restart" && <button className="button small" type="button" disabled={!enrollmentReady || enrollmentStarting} onClick={restartEnrollment}>{enrollmentStarting ? "Starting again…" : "Start again"}</button>}</div>}
        {enrollmentOutcome?.status === "authorized" && <div className="callout success" role="status"><strong>Relay binding authorized</strong><span>Repository event delivery is authorized.</span></div>}
        {enrollmentOutcome?.status === "denied" && <div className="callout warning" role="status"><strong>GitHub authorization denied</strong><span>Confirm the account and repository access, then start again.</span><button className="button small" type="button" disabled={!enrollmentReady || enrollmentStarting} onClick={restartEnrollment}>Start again</button></div>}
        {enrollmentOutcome?.status === "expired" && <div className="callout warning" role="status"><strong>GitHub authorization expired</strong><span>Start again to create a new authorization request.</span><button className="button small" type="button" disabled={!enrollmentReady || enrollmentStarting} onClick={restartEnrollment}>Start again</button></div>}
        {enrollmentOutcome?.status === "failed" && <div ref={enrollmentOutcomeErrorRef} className="callout danger" role="alert" tabIndex={-1}><strong>Relay authorization failed</strong><span>Rig could not complete relay authorization. Start again; if the problem continues, check relay availability.</span><button className="button small" type="button" disabled={!enrollmentReady || enrollmentStarting} onClick={restartEnrollment}>Start again</button></div>}
      </div>

      <div className="relay-section">
        <div className="relay-section-heading"><div><h3>Repository bindings</h3><p>Only bindings owned by this signed-in user and eligible for removal are listed.</p></div><span>{status.removableBindings.length} total</span></div>
        {status.removableBindings.length === 0 ? <p className="relay-empty">No removable relay bindings.</p> : <div className="relay-bindings">{status.removableBindings.map((binding) => <RelayBindingCard key={binding.bindingId} binding={binding} select={(target) => { setRemovalError(""); setSelectedBinding(target); }}/>)}</div>}
      </div>

    </>}

    <div className="relay-section">
      <div className="relay-section-heading"><div><h3>Controller key rotation</h3><p id={rotationDescriptionId}>Rotation changes the relay authentication key without exposing key material.</p></div><span className="relay-state">{rotationState}</span></div>
      {rotationReady && rotationInProgress && <dl className="relay-rotation"><div><dt>State</dt><dd>{rotationState}</dd></div><div><dt>Expires</dt><dd>{displayTime(status?.keyRotation.expiresAt)}</dd></div><div><dt>Updated</dt><dd>{displayTime(status?.keyRotation.updatedAt)}</dd></div></dl>}
      {!rotationReady && <p className="relay-muted">Rotation is unavailable until durable relay status is available.</p>}
      {role === "administrator" && <button className="button" type="button" aria-describedby={rotationDescriptionId} disabled={rotationPending || !rotationReady || rotationInProgress} onClick={() => void rotateKey()}>{rotationPending ? "Starting rotation…" : rotationReady && rotationInProgress ? "Rotation in progress" : "Rotate controller key"}</button>}
      {rotationError && <div ref={rotationErrorRef} tabIndex={-1} className="callout danger" role="alert"><strong>Key rotation needs attention</strong><span>{rotationError}</span></div>}
    </div>

    {selectedBinding && <Dialog title="Remove relay binding" description="This stops relay delivery for this repository. Existing applications and releases are unchanged." pending={removing} close={() => { setSelectedBinding(null); setRemovalError(""); }}>
      <dl className="relay-confirmation"><div><dt>Connection</dt><dd className="mono">{selectedBinding.connectionId}</dd></div><div><dt>Installation</dt><dd>{selectedBinding.installationId}</dd></div><div><dt>Repository</dt><dd>{selectedBinding.repositoryId}</dd></div></dl>
      {removalError && <div ref={removalErrorRef} tabIndex={-1} className="callout danger" role="alert">{removalError}</div>}
      <div className="deployment-dialog-actions"><button className="button" type="button" disabled={removing} onClick={() => { setSelectedBinding(null); setRemovalError(""); }}>Cancel</button><button className="button danger-button" type="button" disabled={removing} onClick={() => void removeBinding()}>{removing ? "Removing…" : "Remove binding"}</button></div>
    </Dialog>}
  </section>;
}

function RelayOverview({ status }: { status: SafeRelayStatus }) {
  if (status.availability === "initializing") return <div className="callout info" role="status"><strong>Relay is initializing</strong><span>Rig will check again for a limited time.</span></div>;
  if (status.availability === "unavailable") return <div className="callout warning" role="status"><strong>Relay unavailable</strong><span>Manual deployments remain available. Relay management will return when the service reconnects.</span></div>;
  return <>
    {!status.readModelAvailable && <div className="callout warning" role="status"><strong>Relay management data unavailable</strong><span>The relay is connected, but durable bindings and key-rotation state cannot be read safely.</span></div>}
    {status.paused && <div className="callout warning" role="status"><strong>Relay delivery is paused</strong><span>Automatic source-event delivery is paused. Manual deployments remain available.</span></div>}
    {status.diagnosticsUnavailable && <div className="callout warning" role="status"><strong>Relay diagnostics unavailable</strong><span>Management data may still be used, but live runtime diagnostics are incomplete.</span></div>}
    <dl className="relay-overview"><div><dt>Lifecycle</dt><dd>{humanizeState(status.state)}</dd></div><div><dt>Outcome</dt><dd>{humanizeState(status.outcome)}</dd></div><div><dt>Pending commands</dt><dd>{status.pendingCommands}</dd></div><div><dt>Active leases</dt><dd>{status.activeLeases}</dd></div><div><dt>Expired leases</dt><dd>{status.expiredLeases}</dd></div><div><dt>Dropped observations</dt><dd>{status.observerDropped}</dd></div><div><dt>Oldest pending</dt><dd>{status.oldestPendingAgeSeconds}s</dd></div></dl>
  </>;
}
