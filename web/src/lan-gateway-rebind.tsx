import { useEffect, useId, useMemo, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  APIError,
  api,
  type LANGatewayCandidate,
  type LANGatewayProfileRead,
  type LANGatewayProfileRevision,
  type LANGatewayProfileSpec,
  type LANGatewayRebindMutation,
  type LANGatewayRebindRecoveryReview,
  type LANGatewayRebindReview,
  type LANGatewayRebindStatus,
} from "./api";
import { Dialog } from "./dialog";

const uuidPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;
const digestPattern = /^[a-f0-9]{64}$/;

type RebindDraft = {
  operationId: string;
  successorProfileRevisionId: string;
  successorProfileRevisionNumber: number;
  successorProfileOperationId: string;
  successorProfile: LANGatewayProfileSpec;
};

type RebindReview = {
  draft: RebindDraft;
  response: LANGatewayRebindReview;
  predecessor: LANGatewayProfileRevision;
};

const rebindLocatorParameter = "gatewayRebindOperation";

function readRebindLocator() {
  if (typeof window === "undefined") return null;
  const operationId = new URLSearchParams(window.location.search).get(rebindLocatorParameter);
  return exactUUID(operationId) ? operationId : null;
}

function writeRebindLocator(operationId: string | null) {
  if (typeof window === "undefined") return;
  const url = new URL(window.location.href);
  if (operationId) url.searchParams.set(rebindLocatorParameter, operationId);
  else url.searchParams.delete(rebindLocatorParameter);
  window.history.replaceState(window.history.state, "", `${url.pathname}${url.search}${url.hash}`);
}

function candidateID(candidate: LANGatewayCandidate) {
  return `${candidate.interfaceId}\u0000${candidate.selectedIpv4}`;
}

function candidateLabel(candidate: LANGatewayCandidate) {
  return `${candidate.name} · ${candidate.selectedIpv4}${candidate.prefix ? `/${candidate.prefix}` : ""}`;
}

function profileMatches(left: LANGatewayProfileSpec, right: LANGatewayProfileSpec) {
  return left.interfaceId === right.interfaceId && left.selectedIpv4 === right.selectedIpv4 &&
    left.portStart === right.portStart && left.portEnd === right.portEnd;
}

function revisionMatches(left: LANGatewayProfileRevision | undefined, right: LANGatewayProfileRevision) {
  return Boolean(left && left.id === right.id && left.revisionNumber === right.revisionNumber &&
    left.specDigest === right.specDigest && profileMatches(left.spec, right.spec));
}

function poolText(spec: Pick<LANGatewayProfileSpec, "portStart" | "portEnd">) {
  return spec.portStart === spec.portEnd ? String(spec.portStart) : `${spec.portStart}–${spec.portEnd}`;
}

function readable(value: string | undefined) {
  return value ? value.replaceAll("_", " ") : "Unavailable";
}

function exactDigest(value: unknown) {
  return typeof value === "string" && digestPattern.test(value);
}

function exactUUID(value: unknown) {
  return typeof value === "string" && uuidPattern.test(value);
}

function record(value: unknown): Record<string, unknown> | null {
  return value !== null && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : null;
}

function validProfileSpec(value: unknown): value is LANGatewayProfileSpec {
  const spec = record(value);
  return Boolean(spec && typeof spec.interfaceId === "string" && spec.interfaceId.length > 0 &&
    typeof spec.selectedIpv4 === "string" && spec.selectedIpv4.length > 0 &&
    Number.isInteger(spec.portStart) && Number.isInteger(spec.portEnd) &&
    Number(spec.portStart) >= 8100 && Number(spec.portEnd) >= Number(spec.portStart) && Number(spec.portEnd) <= 8119);
}

function validRosterEntry(value: unknown, operationId: string) {
  const entry = record(value);
  if (!entry || entry.version !== 2 || entry.operationId !== operationId || !Number.isSafeInteger(entry.ordinal) || Number(entry.ordinal) < 1 ||
      !Number.isInteger(entry.port) || Number(entry.port) < 8100 || Number(entry.port) > 8119) return false;
  return [entry.appId, entry.allocationId, entry.allocationOwnerOperationId, entry.accessRevisionId, entry.grantAttemptId,
    entry.servingDeploymentId, entry.servingReleaseId, entry.sourceProfileRevisionId].every(exactUUID) &&
    [entry.accessSpecDigest, entry.grantProtectedStateDigest, entry.sourceProfileSpecDigest].every(exactDigest) &&
    [entry.accessRevisionNumber, entry.grantStateSequence, entry.sourceProfileRevisionNumber, entry.routeGeneration].every((part) => Number.isSafeInteger(part) && Number(part) >= 1) &&
    typeof entry.allocationState === "string" && typeof entry.servingSlot === "string";
}

function validRuntimeHead(value: unknown) {
  const head = record(value);
  return Boolean(head && exactUUID(head.appId) && exactUUID(head.deploymentId) && exactUUID(head.releaseId) &&
    typeof head.slot === "string" && typeof head.updatedAt === "string" && Number.isSafeInteger(head.generation) && Number(head.generation) >= 1);
}

function validReview(value: unknown, draft: RebindDraft, predecessor: LANGatewayProfileRevision): value is LANGatewayRebindReview {
  const review = record(value);
  const spec = record(review?.spec);
  const lineage = record(record(spec?.predecessor)?.lineage);
  if (!review || !spec || !lineage || review.version !== 1 || spec.version !== 2 ||
      spec.operationId !== draft.operationId || spec.successorProfileRevisionId !== draft.successorProfileRevisionId ||
      spec.successorProfileRevisionNumber !== draft.successorProfileRevisionNumber ||
      spec.successorProfileOperationId !== draft.successorProfileOperationId ||
      !validProfileSpec(spec.successorProfile) || !profileMatches(spec.successorProfile, draft.successorProfile) ||
      !exactDigest(review.proposalDigest) || !exactDigest(review.configureApprovalDigest) || !exactDigest(review.rebindApprovalDigest) ||
      !exactUUID(lineage.profileRevisionId) || lineage.profileRevisionId !== predecessor.id ||
      lineage.profileRevisionNumber !== predecessor.revisionNumber || lineage.profileSpecDigest !== predecessor.specDigest ||
      !Array.isArray(review.roster) || !Array.isArray(review.runtimeHeads) ||
      spec.rosterCount !== review.roster.length || spec.runtimeHeadsCount !== review.runtimeHeads.length ||
      !exactDigest(spec.rosterDigest) || !exactDigest(spec.runtimeHeadsDigest)) return false;

  const rosterIDs = new Set<string>();
  const ordinals = new Set<number>();
  for (const entry of review.roster) {
    if (!validRosterEntry(entry, draft.operationId) || rosterIDs.has(entry.appId) || ordinals.has(entry.ordinal)) return false;
    rosterIDs.add(entry.appId);
    ordinals.add(entry.ordinal);
  }
  for (let ordinal = 1; ordinal <= review.roster.length; ordinal += 1) if (!ordinals.has(ordinal)) return false;

  const runtimeIDs = new Set<string>();
  for (const head of review.runtimeHeads) {
    if (!validRuntimeHead(head) || runtimeIDs.has(head.appId)) return false;
    runtimeIDs.add(head.appId);
  }
  return [...rosterIDs].every((appId) => runtimeIDs.has(appId));
}

function validStatus(value: unknown, operationId: string): value is LANGatewayRebindStatus {
  const status = record(value);
  const operation = record(status?.operation);
  if (!status || status.version !== 1 || status.operationId !== operationId ||
      typeof status.availability !== "string" || typeof status.active !== "boolean" ||
      typeof status.historicalTerminal !== "boolean" || typeof status.terminalProof !== "string" ||
      typeof status.currentProof !== "string" || typeof status.operationFence !== "string" || !Array.isArray(status.allowedActions)) return false;
  if (operation && (!exactUUID(operation.operationId) || operation.operationId !== operationId || typeof operation.storedState !== "string" ||
      !Number.isSafeInteger(operation.stateSequence) || typeof operation.databaseCommitObserved !== "boolean")) return false;
  return status.allowedActions.every((action) => action === "reconcile");
}

function verifiedTerminalStatus(status: LANGatewayRebindStatus) {
  return status.historicalTerminal && Boolean(status.operation) && !status.active &&
    (status.operation?.storedState === "committed" || status.operation?.storedState === "rolled_back") &&
    status.terminalProof === "verified" && status.currentProof === "verified" && status.operationFence === "released";
}

function recordedTerminalStatus(status: LANGatewayRebindStatus) {
  return status.historicalTerminal && Boolean(status.operation) && !status.active &&
    (status.operation?.storedState === "committed" || status.operation?.storedState === "rolled_back") &&
    status.operationFence === "released";
}

function normalControllerRecoveryCleared(value: unknown) {
  const response = record(value);
  return Boolean(response && response.version === 1 && response.mode === "normal" && !("recovery" in response));
}

function verifiedTerminalMutation(value: unknown, operationId: string): value is LANGatewayRebindMutation {
  const mutation = record(value);
  const operation = record(mutation?.operation);
  return Boolean(mutation && operation && mutation.recoveryRequired === false && operation.operationId === operationId &&
    (operation.storedState === "committed" || operation.storedState === "rolled_back") &&
    mutation.terminalProof === "verified" && mutation.currentProof === "verified" && mutation.fenceReleased === true);
}

function validRecoveryReview(value: unknown, operationId: string): value is LANGatewayRebindRecoveryReview {
  const review = record(value);
  return Boolean(review && review.version === 1 && review.operationId === operationId && validStatus(review.status, operationId) &&
    exactDigest(review.recoveryDigest) && exactDigest(review.recoveryApprovalDigest));
}

function newDraft(successorProfile: LANGatewayProfileSpec, predecessor: LANGatewayProfileRevision): RebindDraft {
  return {
    operationId: crypto.randomUUID(),
    successorProfileRevisionId: crypto.randomUUID(),
    successorProfileRevisionNumber: predecessor.revisionNumber + 1,
    successorProfileOperationId: crypto.randomUUID(),
    successorProfile,
  };
}

function ReviewDetails({ review, applicationNames }: { review: RebindReview; applicationNames: Map<string, string> }) {
  const byApp = new Map(review.response.runtimeHeads.map((head) => [head.appId, head]));
  return <>
    <dl className="lan-review-details">
      <div><dt>Current gateway profile</dt><dd>Revision {review.predecessor.revisionNumber}</dd></div>
      <div><dt>New profile revision</dt><dd>Revision {review.draft.successorProfileRevisionNumber}</dd></div>
      <div><dt>Current interface and address</dt><dd>{review.predecessor.spec.interfaceId} · {review.predecessor.spec.selectedIpv4}</dd></div>
      <div><dt>New interface and address</dt><dd>{review.draft.successorProfile.interfaceId} · {review.draft.successorProfile.selectedIpv4}</dd></div>
      <div><dt>Current port pool</dt><dd>{poolText(review.predecessor.spec)}</dd></div>
      <div><dt>New port pool</dt><dd>{poolText(review.draft.successorProfile)}</dd></div>
      <div><dt>Applications included</dt><dd>{review.response.roster.length}</dd></div>
    </dl>
    <div className="lan-rebind-roster" aria-label={`Applications included (${review.response.roster.length})`}>
      <h4>Applications included ({review.response.roster.length})</h4>
      <ol>{[...review.response.roster].sort((left, right) => left.ordinal - right.ordinal).map((entry) => {
        const runtime = byApp.get(entry.appId);
        return <li key={entry.appId}>
          <div><strong>{applicationNames.get(entry.appId) ?? entry.appId}</strong><span>Port {entry.port} · {runtime ? `Release ${runtime.releaseId}` : "Runtime head unavailable"}</span></div>
          <details><summary tabIndex={0}>Review details</summary><dl className="lan-rebind-entry-details">
            <div><dt>Application ID</dt><dd className="mono">{entry.appId}</dd></div>
            <div><dt>Serving deployment</dt><dd className="mono">{entry.servingDeploymentId}</dd></div>
            <div><dt>Serving release</dt><dd className="mono">{entry.servingReleaseId}</dd></div>
            <div><dt>Gateway profile digest</dt><dd className="mono">{entry.sourceProfileSpecDigest}</dd></div>
            <div><dt>Access digest</dt><dd className="mono">{entry.accessSpecDigest}</dd></div>
            <div><dt>Runtime generation</dt><dd>{runtime?.generation ?? "Unavailable"}</dd></div>
          </dl></details>
        </li>;
      })}</ol>
    </div>
    <details className="lan-rebind-identifiers"><summary tabIndex={0}>Technical review details</summary><dl className="lan-rebind-entry-details">
      <div><dt>Recorded operation</dt><dd className="mono">{review.draft.operationId}</dd></div>
      <div><dt>Current profile identifier</dt><dd className="mono">{review.predecessor.id}</dd></div>
      <div><dt>Current profile digest</dt><dd className="mono">{review.predecessor.specDigest}</dd></div>
      <div><dt>Proposal digest</dt><dd className="mono">{review.response.proposalDigest}</dd></div>
      <div><dt>New settings approval digest</dt><dd className="mono">{review.response.configureApprovalDigest}</dd></div>
      <div><dt>Application move approval digest</dt><dd className="mono">{review.response.rebindApprovalDigest}</dd></div>
    </dl></details>
  </>;
}

export function LANGatewayRebindPanel({ profile, disabled = false }: { profile: LANGatewayProfileRead | undefined; disabled?: boolean }) {
  const queryClient = useQueryClient();
  const candidates = profile?.candidates ?? [];
  const predecessor = profile?.desiredProfile;
  const [candidate, setCandidate] = useState("");
  const [portStart, setPortStart] = useState(8100);
  const [portEnd, setPortEnd] = useState(8119);
  const [review, setReview] = useState<RebindReview | null>(null);
  const applications = useQuery({ queryKey: ["apps"], queryFn: api.apps, enabled: Boolean(review), retry: false });
  const applicationNames = useMemo(() => new Map(applications.data?.items.map((application) => [application.id, application.name]) ?? []), [applications.data]);
  const [configureConfirmed, setConfigureConfirmed] = useState(false);
  const [rosterConfirmed, setRosterConfirmed] = useState(false);
  const [reviewError, setReviewError] = useState("");
  const [commitError, setCommitError] = useState("");
  const [pendingOperationId, setPendingOperationId] = useState<string | null>(readRebindLocator);
  const [status, setStatus] = useState<LANGatewayRebindStatus | null>(null);
  const [validatedAbsent, setValidatedAbsent] = useState(false);
  const [statusError, setStatusError] = useState("");
  const [commitResult, setCommitResult] = useState<LANGatewayRebindMutation | null>(null);
  const commitInFlight = useRef(false);
  const statusInFlight = useRef(false);
  const pendingOperationRef = useRef(pendingOperationId);
  const reviewErrorTarget = useRef<HTMLDivElement>(null);
  const commitErrorTarget = useRef<HTMLDivElement>(null);
  const reviewTrigger = useRef<HTMLButtonElement>(null);
  const pendingStatusTarget = useRef<HTMLDivElement>(null);
  const commitResultTarget = useRef<HTMLDivElement>(null);
  const portValidationId = useId();
  const [resolvingTerminal, setResolvingTerminal] = useState(false);

  const selectedCandidate = useMemo(() => candidates.find((item) => candidateID(item) === candidate), [candidate, candidates]);
  const validPool = Number.isInteger(portStart) && Number.isInteger(portEnd) && portStart >= 8100 && portEnd >= portStart && portEnd <= 8119;
  const successorProfile = selectedCandidate && validPool
    ? { interfaceId: selectedCandidate.interfaceId, selectedIpv4: selectedCandidate.selectedIpv4, portStart, portEnd }
    : null;
  const reviewExact = Boolean(review && revisionMatches(predecessor, review.predecessor));

  const inspect = useMutation({
    mutationFn: async (draft: RebindDraft) => {
      const response = await api.inspectLANGatewayRebind(draft);
      if (!predecessor || !validReview(response, draft, predecessor)) throw new Error("invalid_rebind_review");
      return { draft, response, predecessor } satisfies RebindReview;
    },
    onSuccess: (next) => {
      setReview(next);
      setConfigureConfirmed(false);
      setRosterConfirmed(false);
      setReviewError("");
      setCommitError("");
      setStatus(null);
      setValidatedAbsent(false);
      setStatusError("");
      setCommitResult(null);
    },
    onError: (error) => {
      setReviewError(error instanceof Error && error.message === "invalid_rebind_review"
        ? "Rig could not obtain one complete gateway and application review. Refresh the gateway state before trying again."
        : "Rig could not prepare this gateway change. Refresh the gateway state before trying again.");
    },
  });

  const commit = useMutation({
    mutationFn: (next: RebindReview) => api.commitLANGatewayRebind({
      operationId: next.draft.operationId,
      successorProfileRevisionId: next.draft.successorProfileRevisionId,
      successorProfileRevisionNumber: next.draft.successorProfileRevisionNumber,
      successorProfileOperationId: next.draft.successorProfileOperationId,
      successorProfile: next.draft.successorProfile,
      version: next.response.version,
      proposalDigest: next.response.proposalDigest,
      configureApprovalDigest: next.response.configureApprovalDigest,
      rebindApprovalDigest: next.response.rebindApprovalDigest,
    }),
    onSuccess: async (result, next) => {
      if (!verifiedTerminalMutation(result, next.draft.operationId)) {
        setCommitResult(null);
        setReview(null);
        setConfigureConfirmed(false);
        setRosterConfirmed(false);
        setStatusError("The result is not confirmed. Check this operation before making another change.");
        void queryClient.invalidateQueries({ queryKey: ["controller-recovery"] });
        return;
      }
      setCommitResult(result);
      setReview(null);
      setConfigureConfirmed(false);
      setRosterConfirmed(false);
      setCommitError("");
      setPendingOperationId(null);
      pendingOperationRef.current = null;
      writeRebindLocator(null);
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ["lan-gateway-profile"] }),
        queryClient.invalidateQueries({ queryKey: ["lan-gateway-upgrade"] }),
        queryClient.invalidateQueries({ queryKey: ["lan-app-access"] }),
      ]);
    },
    onError: (error, next) => {
      setConfigureConfirmed(false);
      setRosterConfirmed(false);
      setReview(null);
      if (error instanceof APIError && error.code === "csrf_failed") {
        setPendingOperationId(null);
        pendingOperationRef.current = null;
        writeRebindLocator(null);
        setReviewError("The security check failed before Rig handled this request. Prepare a fresh review before trying again.");
        return;
      }
      if (error instanceof APIError && error.status === 409) {
        setStatusError("The gateway or application list changed. Check this recorded operation before preparing a fresh review.");
        void queryClient.invalidateQueries({ queryKey: ["controller-recovery"] });
        return;
      }
      setStatusError("The result is not confirmed. Check this operation before making another change.");
      void queryClient.invalidateQueries({ queryKey: ["controller-recovery"] });
    },
  });

  useEffect(() => {
    if (!review || reviewExact || (!configureConfirmed && !rosterConfirmed)) return;
    setConfigureConfirmed(false);
    setRosterConfirmed(false);
  }, [configureConfirmed, review, reviewExact, rosterConfirmed]);
  useEffect(() => {
    if (!review || !disabled) return;
    setConfigureConfirmed(false);
    setRosterConfirmed(false);
  }, [disabled, review]);
  useEffect(() => {
    if (reviewError) window.setTimeout(() => reviewErrorTarget.current?.focus(), 0);
  }, [reviewError]);
  useEffect(() => {
    if (commitError) window.setTimeout(() => commitErrorTarget.current?.focus(), 0);
  }, [commitError]);
  useEffect(() => {
    if (pendingOperationId && !review) window.setTimeout(() => pendingStatusTarget.current?.focus(), 0);
  }, [pendingOperationId, review]);
  useEffect(() => {
    if (commitResult && !review) window.setTimeout(() => commitResultTarget.current?.focus(), 0);
  }, [commitResult, review]);

  const resetReview = () => {
    setReview(null);
    setConfigureConfirmed(false);
    setRosterConfirmed(false);
    setCommitError("");
  };
  const closeReview = () => {
    resetReview();
    window.setTimeout(() => reviewTrigger.current?.focus(), 0);
  };
  const changeDraft = () => {
    resetReview();
    setReviewError("");
    setStatus(null);
    setValidatedAbsent(false);
    setStatusError("");
    setCommitResult(null);
  };
  const requestReview = () => {
    if (!predecessor || !successorProfile || disabled || inspect.isPending || pendingOperationId) return;
    setReviewError("");
    inspect.mutate(newDraft(successorProfile, predecessor));
  };
  const apply = () => {
    if (!review || !reviewExact || disabled || pendingOperationId || !configureConfirmed || !rosterConfirmed || commit.isPending || commitInFlight.current) return;
    commitInFlight.current = true;
    setPendingOperationId(review.draft.operationId);
    pendingOperationRef.current = review.draft.operationId;
    writeRebindLocator(review.draft.operationId);
    setCommitError("");
    commit.mutate(review, { onSettled: () => { commitInFlight.current = false; } });
  };
  const checkStatus = async (operationId: string) => {
    if (statusInFlight.current || commit.isPending || commitInFlight.current || operationId !== pendingOperationRef.current) return;
    statusInFlight.current = true;
    setStatusError("");
    setValidatedAbsent(false);
    try {
      const next = await api.getLANGatewayRebind(operationId);
      if (!validStatus(next, operationId)) throw new Error("invalid_rebind_status");
      if (operationId === pendingOperationRef.current) {
        setStatus(next);
        if (next.active) void queryClient.invalidateQueries({ queryKey: ["controller-recovery"] });
      }
    } catch (error) {
      if (error instanceof APIError && error.status === 404 && error.code === "lan_gateway_rebind_not_found") {
        if (operationId === pendingOperationRef.current) {
          setStatus(null);
          setValidatedAbsent(true);
        }
        return;
      }
      setStatusError("Rig could not confirm this operation. Keep the recorded operation ID and check the controller before making another change.");
    } finally {
      statusInFlight.current = false;
    }
  };
  const confirmTerminalResolution = async () => {
    const operationId = pendingOperationId;
    if (!operationId || statusInFlight.current || commit.isPending || commitInFlight.current || (!validatedAbsent && (!status || !recordedTerminalStatus(status))) || resolvingTerminal) return;
    setResolvingTerminal(true);
    setStatusError("");
    try {
      const recovery = await api.getControllerRecovery();
      if (!normalControllerRecoveryCleared(recovery)) throw new Error("controller_recovery_still_active");
      if (operationId !== pendingOperationRef.current || commit.isPending || commitInFlight.current) return;
      setPendingOperationId(null);
      pendingOperationRef.current = null;
      writeRebindLocator(null);
      setValidatedAbsent(false);
      window.setTimeout(() => reviewTrigger.current?.focus(), 0);
    } catch {
      setStatusError("Rig could not confirm that recovery is clear. Keep the recorded operation ID and check the controller before making another change.");
    } finally {
      setResolvingTerminal(false);
    }
  };
  useEffect(() => {
    if (pendingOperationId) void checkStatus(pendingOperationId);
  // The URL value is untrusted and is resolved by one exact read only.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  if (!predecessor) return <section className="lan-rebind" aria-labelledby="lan-rebind-title"><div className="lan-section-heading"><div><h3 id="lan-rebind-title">Change LAN gateway</h3><p>Approve the current gateway profile and complete its gateway setup before preparing a replacement.</p></div></div></section>;

  return <section className="lan-rebind" aria-labelledby="lan-rebind-title">
    <div className="lan-section-heading"><div><h3 id="lan-rebind-title">Change LAN gateway</h3><p>Review a replacement gateway and every application currently sharing it. This keeps the current gateway in place until both approvals are applied together.</p></div></div>
    {!pendingOperationId && <form className="lan-profile-form" onSubmit={(event) => { event.preventDefault(); requestReview(); }} noValidate aria-busy={inspect.isPending || commit.isPending}>
      <fieldset disabled={disabled || inspect.isPending || commit.isPending || Boolean(review)}>
        <legend>New gateway settings</legend>
        <div className="field"><label htmlFor="lan-rebind-interface">Interface and IPv4 address</label><select id="lan-rebind-interface" value={candidate} onChange={(event) => { setCandidate(event.target.value); changeDraft(); }}><option value="">Choose an interface and address</option>{candidates.map((item) => <option key={candidateID(item)} value={candidateID(item)}>{candidateLabel(item)}</option>)}</select><small>Choose an available private IPv4 interface and address for the replacement gateway.</small></div>
        <div className="lan-port-pool"><div className="field"><label htmlFor="lan-rebind-port-start">First port</label><input id="lan-rebind-port-start" type="number" inputMode="numeric" min={8100} max={8119} value={portStart} aria-invalid={!validPool || undefined} aria-describedby={!validPool ? portValidationId : undefined} onChange={(event) => { setPortStart(Number(event.target.value)); changeDraft(); }}/></div><div className="field"><label htmlFor="lan-rebind-port-end">Last port</label><input id="lan-rebind-port-end" type="number" inputMode="numeric" min={8100} max={8119} value={portEnd} aria-invalid={!validPool || undefined} aria-describedby={!validPool ? portValidationId : undefined} onChange={(event) => { setPortEnd(Number(event.target.value)); changeDraft(); }}/></div></div>
      </fieldset>
      {!validPool && <p id={portValidationId} className="form-error" role="alert">Choose a port pool from 8100 through 8119, with the last port no lower than the first.</p>}
      {reviewError && <div ref={reviewErrorTarget} className="callout danger" role="alert" tabIndex={-1}><strong>Gateway review unavailable</strong><span>{reviewError}</span><button className="button small" type="button" onClick={requestReview}>Refresh review</button></div>}
      <button ref={reviewTrigger} className="button" type="submit" disabled={disabled || !successorProfile || inspect.isPending || commit.isPending || Boolean(review)}>{inspect.isPending ? "Preparing review…" : "Review gateway change"}</button>
    </form>}
    {pendingOperationId && <div ref={pendingStatusTarget} className="lan-pending" role="status" tabIndex={-1}><h4>Gateway change needs confirmation</h4><p>{validatedAbsent ? "Rig confirmed no recorded operation for this ID. Check that no gateway change remains active before returning to a fresh review." : status && recordedTerminalStatus(status) ? "Rig recorded a terminal outcome, but this screen has not verified the current gateway. Check that no gateway change remains active before returning to a fresh review." : "The result is not confirmed. Check this operation before making another change."}</p><dl className="lan-details"><div><dt>Recorded operation</dt><dd className="mono">{pendingOperationId}</dd></div></dl><div className="lan-actions"><button className="button small" type="button" disabled={commit.isPending || commitInFlight.current} onClick={() => void checkStatus(pendingOperationId)}>Check operation status</button>{(validatedAbsent || (status && recordedTerminalStatus(status))) && <button className="button small" type="button" disabled={commit.isPending || commitInFlight.current || resolvingTerminal} onClick={() => void confirmTerminalResolution()}>{resolvingTerminal ? "Checking for pending changes…" : "Return to gateway review"}</button>}</div></div>}
    {statusError && <div className="callout danger" role="alert"><strong>Operation status unavailable</strong><span>{statusError}</span></div>}
    {status && <div className="lan-pending" role="status"><h4>Gateway change status</h4><dl className="lan-details"><div><dt>Recorded operation</dt><dd className="mono">{status.operationId}</dd></div><div><dt>Phase</dt><dd>{readable(status.operation?.storedState)}</dd></div><div><dt>Disposition</dt><dd>{readable(status.operation?.terminalDisposition)}</dd></div><div><dt>Current gateway proof</dt><dd>{readable(status.currentProof)}</dd></div><div><dt>Terminal proof</dt><dd>{readable(status.terminalProof)}</dd></div><div><dt>Operation fence</dt><dd>{readable(status.operationFence)}</dd></div></dl></div>}
    {commitResult && <div ref={commitResultTarget} className="lan-pending" role="status" tabIndex={-1}><h4>Gateway change recorded</h4><p>{commitResult.recoveryRequired ? "Rig recorded this operation but requires recovery before it can confirm the result." : "Rig recorded the gateway change. Check the current gateway state before sharing application addresses."}</p><dl className="lan-details"><div><dt>Recorded operation</dt><dd className="mono">{commitResult.operation.operationId}</dd></div><div><dt>Phase</dt><dd>{readable(commitResult.operation.storedState)}</dd></div><div><dt>Disposition</dt><dd>{readable(commitResult.operation.terminalDisposition)}</dd></div><div><dt>Current gateway proof</dt><dd>{readable(commitResult.currentProof)}</dd></div><div><dt>Terminal proof</dt><dd>{readable(commitResult.terminalProof)}</dd></div><div><dt>Operation fence</dt><dd>{commitResult.fenceReleased ? "Released" : "Held"}</dd></div></dl></div>}
    {review && <Dialog title="Review gateway change" description="Confirm the new gateway settings and the complete application list. This request does not first change the current gateway profile." pending={commit.isPending} focusTitle close={closeReview}>
      {(!reviewExact || disabled) ? <div ref={commitErrorTarget} className="callout danger" role="alert" tabIndex={-1}><strong>Gateway review expired</strong><span>The gateway changed while this review was open. Both approvals were cleared.</span><div className="deployment-dialog-actions"><button className="button" type="button" disabled={commit.isPending} onClick={closeReview}>Cancel</button><button className="button small" type="button" disabled={disabled || commit.isPending} onClick={() => { resetReview(); requestReview(); }}>Refresh review</button></div></div> : <>
        <ReviewDetails review={review} applicationNames={applicationNames}/>
        {commitError && <div ref={commitErrorTarget} className="callout danger" role="alert" tabIndex={-1}><strong>Gateway change was not applied</strong><span>{commitError}</span></div>}
        <div className="lan-rebind-consent">
          <label><input type="checkbox" checked={configureConfirmed} disabled={commit.isPending} onChange={(event) => setConfigureConfirmed(event.target.checked)}/><span>I approve these new gateway settings.</span></label>
          <label><input type="checkbox" checked={rosterConfirmed} disabled={commit.isPending} onChange={(event) => setRosterConfirmed(event.target.checked)}/><span>I approve moving all {review.response.roster.length} listed applications to this gateway.</span></label>
        </div>
        <div className="deployment-dialog-actions"><button className="button" type="button" disabled={commit.isPending} onClick={closeReview}>Cancel</button><button className="button primary" type="button" disabled={disabled || !configureConfirmed || !rosterConfirmed || commit.isPending} onClick={apply}>{commit.isPending ? "Applying gateway change…" : "Apply gateway change"}</button></div>
      </>}
    </Dialog>}
  </section>;
}

function recoveryPolicyText(policy: string | undefined) {
  if (policy === "forward_only") return "Rig can finish this gateway change.";
  if (policy === "rollback_only") return "Rig can restore the previous gateway.";
  return "Rig will use the saved operation state to finish the change or restore the previous gateway.";
}

function RecoveryStatusFacts({ status }: { status: LANGatewayRebindStatus }) {
  return <dl className="recovery-facts">
    <div><dt>Recorded operation</dt><dd className="mono">{status.operationId}</dd></div>
    <div><dt>Gateway phase</dt><dd>{readable(status.operation?.storedState)}</dd></div>
    <div><dt>Database commit recorded</dt><dd>{status.operation ? status.operation.databaseCommitObserved ? "Yes" : "No" : "Unavailable"}</dd></div>
    <div><dt>Recorded disposition</dt><dd>{readable(status.operation?.terminalDisposition)}</dd></div>
    <div><dt>Protected progress</dt><dd>{readable(status.protected?.phase)}</dd></div>
    <div><dt>Current gateway check</dt><dd>{readable(status.currentProof)}</dd></div>
    <div><dt>Terminal operation proof</dt><dd>{readable(status.terminalProof)}</dd></div>
    <div><dt>Operation fence</dt><dd>{readable(status.operationFence)}</dd></div>
    <div><dt>Recovery available</dt><dd>{status.allowedActions.includes("reconcile") ? "Yes" : status.availability === "available" && status.active ? "Not yet reviewed" : "Unavailable"}</dd></div>
  </dl>;
}

export function LANGatewayRebindRecoveryScreen({ operationId, onSignOut, onCheckSystemStatus }: {
  operationId: string;
  onSignOut: () => void | Promise<void>;
  onCheckSystemStatus: () => Promise<void>;
}) {
  const heading = useRef<HTMLHeadingElement>(null);
  const errorTarget = useRef<HTMLDivElement>(null);
  const recoveryReviewTarget = useRef<HTMLDivElement>(null);
  const mutationInFlight = useRef(false);
  const requestEpoch = useRef(0);
  const [status, setStatus] = useState<LANGatewayRebindStatus | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [review, setReview] = useState<LANGatewayRebindRecoveryReview | null>(null);
  const [confirmed, setConfirmed] = useState(false);
  const [pending, setPending] = useState(false);
  const [checkingStatus, setCheckingStatus] = useState(false);
  const [unknownResult, setUnknownResult] = useState(false);

  useEffect(() => { heading.current?.focus(); }, []);
  useEffect(() => {
    if (review) window.setTimeout(() => recoveryReviewTarget.current?.focus(), 0);
  }, [review]);
  const loadStatus = async (focusError = false) => {
    const epoch = ++requestEpoch.current;
    setLoading(true);
    setError("");
    setConfirmed(false);
    try {
      const next = await api.getLANGatewayRebind(operationId);
      if (epoch !== requestEpoch.current) return;
      if (!validStatus(next, operationId)) throw new Error("invalid_rebind_status");
      setStatus(next);
      setUnknownResult(false);
    } catch {
      if (epoch !== requestEpoch.current) return;
      setStatus(null);
      setReview(null);
      setError("Recovery cannot be confirmed. Check recovery again or sign out before taking further action.");
      if (focusError) window.setTimeout(() => errorTarget.current?.focus(), 0);
    } finally {
      if (epoch === requestEpoch.current) setLoading(false);
    }
  };
  useEffect(() => {
    void loadStatus();
    return () => { requestEpoch.current += 1; };
  }, [operationId]);

  const recoveryCanBeReviewed = Boolean(status?.availability === "available" && status.active);
  const reviewRecovery = async () => {
    if (!recoveryCanBeReviewed || pending) return;
    const epoch = ++requestEpoch.current;
    setPending(true);
    setError("");
    setConfirmed(false);
    try {
      const next = await api.inspectLANGatewayRebindRecovery(operationId);
      if (epoch !== requestEpoch.current) return;
      if (!validRecoveryReview(next, operationId) || !next.status.allowedActions.includes("reconcile")) throw new Error("invalid_recovery_review");
      setReview(next);
      setStatus(next.status);
    } catch {
      if (epoch !== requestEpoch.current) return;
      setReview(null);
      setUnknownResult(true);
      setError("Recovery cannot be confirmed. Check recovery again before deciding whether to continue.");
      window.setTimeout(() => errorTarget.current?.focus(), 0);
    } finally {
      if (epoch === requestEpoch.current) setPending(false);
    }
  };
  const reconcile = async () => {
    if (!review || !confirmed || pending || mutationInFlight.current) return;
    mutationInFlight.current = true;
    const epoch = ++requestEpoch.current;
    setPending(true);
    setError("");
    try {
      const result = await api.reconcileLANGatewayRebind(operationId, {
        version: review.version,
        action: "reconcile",
        recoveryDigest: review.recoveryDigest!,
        recoveryApprovalDigest: review.recoveryApprovalDigest!,
      });
      if (epoch !== requestEpoch.current) return;
      if (!validStatus(result.status, operationId)) throw new Error("invalid_reconcile_result");
      setStatus(result.status);
      setReview(null);
      setConfirmed(false);
    } catch (failure) {
      if (epoch !== requestEpoch.current) return;
      setConfirmed(false);
      setReview(null);
      if (failure instanceof APIError && failure.status === 409) {
        setUnknownResult(true);
        setError("The gateway or application list changed. Check this recorded operation before preparing a fresh recovery review.");
      } else if (failure instanceof APIError && failure.code === "csrf_failed") {
        setUnknownResult(true);
        setError("The security check failed before Rig handled this request. Check operation status, then prepare a fresh recovery review.");
      } else {
        setUnknownResult(true);
        setError("The result is not confirmed. Check this operation before making another change.");
      }
      window.setTimeout(() => errorTarget.current?.focus(), 0);
    } finally {
      mutationInFlight.current = false;
      if (epoch === requestEpoch.current) setPending(false);
    }
  };
  const checkAfterRestart = async () => {
    if (checkingStatus) return;
    setCheckingStatus(true);
    try { await onCheckSystemStatus(); } finally { setCheckingStatus(false); }
  };
  const completed = Boolean(status && verifiedTerminalStatus(status));
  const terminalRecorded = Boolean(status && recordedTerminalStatus(status));

  return <main className="recovery-shell"><section className="recovery-card" aria-labelledby="rebind-recovery-title">
    <header className="recovery-header"><div className="auth-brand"><b aria-hidden="true">h&gt;</b><span>hostd</span></div><button className="button" type="button" onClick={() => void onSignOut()}>Sign out</button></header>
    <div className="recovery-heading"><p className="recovery-kicker">Local recovery console</p><h1 id="rebind-recovery-title" ref={heading} tabIndex={-1}>LAN gateway recovery required</h1><p>Rig isolated the normal operator console while it checks one recorded gateway change.</p></div>
    {loading && <div className="recovery-loading" role="status" aria-live="polite" aria-busy="true">Checking the recorded gateway operation…</div>}
    {error && <div className="callout danger" ref={errorTarget} tabIndex={-1} role="alert"><strong>Recovery cannot be confirmed</strong><span>{error}</span></div>}
    {!loading && status && <>
      <RecoveryStatusFacts status={status}/>
      {completed ? <div className="callout info" role="status"><strong>The operation is complete.</strong><span>Restart Rig, then check system status.</span></div> : terminalRecorded ? <div className="callout warning" role="status"><strong>Terminal outcome recorded</strong><span>Rig has not verified the current gateway from this historical record. Restart Rig, then check system status.</span></div> : status.historicalTerminal ? <div className="callout warning" role="status"><strong>Historical outcome recorded</strong><span>Rig cannot yet confirm the current gateway or terminal proof. Keep recovery open and check the recorded operation again.</span></div> : <p className="lan-muted">{recoveryPolicyText(status.recoveryPolicy)}</p>}
      {unknownResult && <div className="callout warning" role="status">The result is not confirmed. Check this operation before making another change.</div>}
      {!completed && !terminalRecorded && !review && !unknownResult && recoveryCanBeReviewed && <button className="button primary" type="button" disabled={pending} onClick={() => void reviewRecovery()}>{pending ? "Reviewing recovery…" : "Review recovery"}</button>}
      {!completed && !terminalRecorded && !review && (!recoveryCanBeReviewed || unknownResult) && <button className="button" type="button" disabled={pending} onClick={() => void loadStatus(true)}>{unknownResult ? "Check operation status" : "Check recovery again"}</button>}
      {(completed || terminalRecorded) && <button className="button primary" type="button" disabled={checkingStatus} onClick={() => void checkAfterRestart()}>{checkingStatus ? "Checking system status…" : "Check system status after restart"}</button>}
      {review && <div ref={recoveryReviewTarget} className="recovery-consent" tabIndex={-1}><div className="callout info" role="status"><strong>Recovery review ready</strong><span>{recoveryPolicyText(review.status.recoveryPolicy)}</span></div><label><input type="checkbox" checked={confirmed} disabled={pending} onChange={(event) => setConfirmed(event.target.checked)}/><span>I approve recovery of this gateway operation.</span></label><div className="recovery-actions"><button className="button" type="button" disabled={pending} onClick={() => { setReview(null); setConfirmed(false); }}>Cancel</button><button className="button primary" type="button" disabled={!confirmed || pending} onClick={() => void reconcile()}>{pending ? "Recovering operation…" : "Recover this operation"}</button></div></div>}
    </>}
    {!loading && !status && <div className="recovery-actions"><button className="button primary" type="button" onClick={() => void loadStatus(true)}>Check recovery again</button><button className="button" type="button" onClick={() => void onSignOut()}>Sign out</button></div>}
  </section></main>;
}
