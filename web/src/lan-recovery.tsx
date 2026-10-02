import { useCallback, useEffect, useRef, useState, type ReactNode } from "react";
import { useQueryClient } from "@tanstack/react-query";
import {
  APIError,
  api,
  isGatewayReconciliationRequired,
  subscribeGatewayReconciliationRequired,
  validSystemStatus,
  type LANAppDisableClaim,
  type LANAppDisableRead,
  type LANAppGrantClaim,
  type LANAppGrantRead,
  type LANRecoveryHead,
} from "./api";

type SignOut = () => void | Promise<void>;
type RecoveryReview =
  | { kind: "lan_grant"; head: LANRecoveryHead; observation: LANAppGrantRead }
  | { kind: "lan_disable"; head: LANRecoveryHead; observation: LANAppDisableRead };

const recoveryHeadFields = [
  "kind",
  "appId",
  "operationId",
  "batch",
  "batchPosition",
  "batchCount",
  "claimState",
  "accessRevisionId",
  "accessRevisionNumber",
  "allocationId",
  "ownerOperationId",
  "port",
  "approvalDigest",
] as const satisfies readonly (keyof LANRecoveryHead)[];

const grantClaimFields = [
  "attemptId",
  "appId",
  "accessRevisionId",
  "accessRevisionNumber",
  "allocationId",
  "ownerOperationId",
  "port",
  "state",
  "updatedAt",
] as const satisfies readonly (keyof LANAppGrantClaim)[];

const disableClaimFields = [
  "operationId",
  "appId",
  "accessRevisionId",
  "accessRevisionNumber",
  "allocationId",
  "approvalDigest",
  "port",
  "state",
  "updatedAt",
  "releasedAt",
] as const satisfies readonly (keyof LANAppDisableClaim)[];

const uuidPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;
const digestPattern = /^[0-9a-f]{64}$/;
const recoveryClaimStates = new Set(["prepared", "applying", "db_active", "uncertain", "committed", "rolled_back", "withdrawing"]);
const grantAvailabilities = new Set(["unknown", "committed", "pending_published", "pending_reload_only", "withdrawn_pending_reconciliation"]);
const disableAvailabilities = new Set(["unknown", "pending_published", "withdrawn_pending_resolution", "disabled"]);

function record(value: unknown): Record<string, unknown> | null {
  return value !== null && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : null;
}

function requiredString(value: unknown) {
  return typeof value === "string" && value.length > 0;
}

function optionalString(value: unknown) {
  return value === undefined || typeof value === "string";
}

function validRecoveryHead(value: unknown): value is LANRecoveryHead {
  const head = record(value);
  if (!head) return false;
  return (head.kind === "lan_grant" || head.kind === "lan_disable") &&
    [head.appId, head.operationId, head.accessRevisionId, head.allocationId, head.ownerOperationId].every((item) => typeof item === "string" && uuidPattern.test(item)) &&
    typeof head.approvalDigest === "string" && digestPattern.test(head.approvalDigest) &&
    typeof head.batch === "boolean" &&
    Number.isInteger(head.batchPosition) && Number(head.batchPosition) >= 1 && Number(head.batchPosition) <= 64 &&
    Number.isInteger(head.batchCount) && Number(head.batchCount) >= 1 && Number(head.batchCount) <= 64 &&
    Number(head.batchPosition) <= Number(head.batchCount) &&
    typeof head.claimState === "string" && recoveryClaimStates.has(head.claimState) &&
    Number.isSafeInteger(head.accessRevisionNumber) && Number(head.accessRevisionNumber) >= 1 &&
    Number.isInteger(head.port) && Number(head.port) >= 8100 && Number(head.port) <= 8119;
}

function validGrantRead(value: unknown): value is LANAppGrantRead {
  const read = record(value);
  const claim = record(read?.claim);
  const observed = record(read?.observed);
  return Boolean(claim && observed &&
    [claim.attemptId, claim.appId, claim.accessRevisionId, claim.allocationId, claim.ownerOperationId].every((item) => requiredString(item)) &&
    requiredString(claim.state) && requiredString(claim.updatedAt) &&
    Number.isSafeInteger(claim.accessRevisionNumber) && Number.isInteger(claim.port) &&
    typeof observed.availability === "string" && grantAvailabilities.has(observed.availability) && optionalString(observed.observedAt));
}

function validDisableRead(value: unknown): value is LANAppDisableRead {
  const read = record(value);
  const claim = record(read?.claim);
  const observed = record(read?.observed);
  return Boolean(claim && observed &&
    [claim.operationId, claim.appId, claim.accessRevisionId, claim.allocationId, claim.approvalDigest].every((item) => requiredString(item)) &&
    requiredString(claim.state) && requiredString(claim.updatedAt) && optionalString(claim.releasedAt) &&
    Number.isSafeInteger(claim.accessRevisionNumber) && Number.isInteger(claim.port) &&
    typeof observed.availability === "string" && disableAvailabilities.has(observed.availability) && optionalString(observed.observedAt));
}

function exactRecoveryHead(value: unknown) {
  const head = record(value);
  if (head && typeof head.kind === "string" && head.kind !== "lan_grant" && head.kind !== "lan_disable") {
    throw new Error("unsupported_recovery_kind");
  }
  if (!validRecoveryHead(value)) throw new Error("invalid_recovery_head");
  return value;
}

function sameRecoveryHead(left: LANRecoveryHead, right: LANRecoveryHead) {
  return recoveryHeadFields.every((field) => left[field] === right[field]);
}

function grantClaimMatchesHead(claim: LANAppGrantClaim, head: LANRecoveryHead, includeState = true) {
  return claim.attemptId === head.operationId &&
    claim.appId === head.appId &&
    claim.accessRevisionId === head.accessRevisionId &&
    claim.accessRevisionNumber === head.accessRevisionNumber &&
    claim.allocationId === head.allocationId &&
    claim.ownerOperationId === head.ownerOperationId &&
    claim.port === head.port &&
    (!includeState || claim.state === head.claimState);
}

function disableClaimMatchesHead(claim: LANAppDisableClaim, head: LANRecoveryHead, includeState = true) {
  return claim.operationId === head.operationId &&
    claim.appId === head.appId &&
    claim.accessRevisionId === head.accessRevisionId &&
    claim.accessRevisionNumber === head.accessRevisionNumber &&
    claim.allocationId === head.allocationId &&
    claim.port === head.port &&
    claim.approvalDigest === head.approvalDigest &&
    (!includeState || claim.state === head.claimState);
}

function reviewMatchesHead(review: RecoveryReview, head = review.head) {
  if (!sameRecoveryHead(review.head, head) || review.kind !== head.kind) return false;
  return review.kind === "lan_grant"
    ? grantClaimMatchesHead(review.observation.claim, head)
    : disableClaimMatchesHead(review.observation.claim, head);
}

function sameReviewedObservation(reviewed: RecoveryReview, current: RecoveryReview) {
  if (reviewed.kind !== current.kind) return false;
  if (reviewed.kind === "lan_grant" && current.kind === "lan_grant") {
    return grantClaimFields.every((field) => reviewed.observation.claim[field] === current.observation.claim[field]) &&
      reviewed.observation.observed.availability === current.observation.observed.availability;
  }
  if (reviewed.kind === "lan_disable" && current.kind === "lan_disable") {
    return disableClaimFields.every((field) => reviewed.observation.claim[field] === current.observation.claim[field]) &&
      reviewed.observation.observed.availability === current.observation.observed.availability;
  }
  return false;
}

function recoveryKind(head: LANRecoveryHead): "lan_grant" | "lan_disable" {
  if (head.kind === "lan_grant" || head.kind === "lan_disable") return head.kind;
  throw new Error("unsupported_recovery_kind");
}

async function readExactObservation(head: LANRecoveryHead): Promise<RecoveryReview> {
  const kind = recoveryKind(head);
  if (kind === "lan_grant") {
    const observation = await api.lanGrant(head.appId, head.operationId);
    if (!validGrantRead(observation)) throw new Error("invalid_recovery_observation");
    return { kind, head, observation };
  }
  const observation = await api.lanDisable(head.appId, head.operationId);
  if (!validDisableRead(observation)) throw new Error("invalid_recovery_observation");
  return { kind, head, observation };
}

function recoveryReadError(error: unknown) {
  if (error instanceof Error && error.message === "unsupported_recovery_kind") {
    return "This recovery kind is not supported by this console. Restart Rig with a supported recovery build before continuing.";
  }
  if (error instanceof APIError && error.status === 403) {
    return "Administrator access is required to inspect and reconcile LAN recovery.";
  }
  if (error instanceof APIError && error.status === 503) {
    return "Rig could not prove one exact recovery operation. Restart Rig and sign in again before continuing.";
  }
  return "Rig could not load a complete, exact recovery review. Sign out and inspect the controller before continuing.";
}

function postError(error: unknown) {
  if (error instanceof APIError && error.status === 403) {
    return { blocking: true, message: "Administrator access is required to reconcile LAN recovery." };
  }
  if (error instanceof APIError && error.status === 503) {
    return { blocking: true, message: "Rig can no longer prove this recovery operation. Restart Rig before continuing." };
  }
  if (error instanceof APIError) {
    return { blocking: false, message: "Rig rejected the reconciliation request. Load a fresh review before deciding whether to try again." };
  }
  return { blocking: false, message: "The reconciliation result is unknown. The request was not repeated. Inspect Rig, then load a fresh review before deciding whether to try again." };
}

function RecoveryFacts({ review }: { review: RecoveryReview }) {
  const { head } = review;
  return <dl className="recovery-facts">
    <div><dt>Recovery kind</dt><dd>{review.kind === "lan_grant" ? "Grant LAN access" : "Disable LAN access"}</dd></div>
    <div><dt>Application ID</dt><dd className="mono">{head.appId}</dd></div>
    <div><dt>{review.kind === "lan_grant" ? "Attempt ID" : "Operation ID"}</dt><dd className="mono">{head.operationId}</dd></div>
    <div><dt>Claim state</dt><dd>{head.claimState}</dd></div>
    <div><dt>Batch position</dt><dd>{head.batchPosition} of {head.batchCount}</dd></div>
    <div><dt>Access revision</dt><dd><span className="mono">{head.accessRevisionId}</span> · revision {head.accessRevisionNumber}</dd></div>
    <div><dt>Allocation ID</dt><dd className="mono">{head.allocationId}</dd></div>
    <div><dt>Owner operation ID</dt><dd className="mono">{head.ownerOperationId}</dd></div>
    <div><dt>Port</dt><dd>{head.port}</dd></div>
    <div><dt>Approval digest</dt><dd className="mono">{head.approvalDigest}</dd></div>
    <div><dt>Observed disposition</dt><dd>{review.observation.observed.availability}</dd></div>
  </dl>;
}

export function LANRecoveryScreen({ onSignOut, onCheckSystemStatus }: {
  onSignOut: SignOut;
  onCheckSystemStatus: () => Promise<void>;
}) {
  const heading = useRef<HTMLHeadingElement>(null);
  const errorSummary = useRef<HTMLDivElement>(null);
  const requestErrorSummary = useRef<HTMLDivElement>(null);
  const completionSummary = useRef<HTMLDivElement>(null);
  const mounted = useRef(true);
  const reconcileInFlight = useRef(false);
  const [review, setReview] = useState<RecoveryReview | null>(null);
  const [loading, setLoading] = useState(true);
  const [confirmed, setConfirmed] = useState(false);
  const [pending, setPending] = useState(false);
  const [blockingError, setBlockingError] = useState("");
  const [requestError, setRequestError] = useState("");
  const [completed, setCompleted] = useState(false);
  const [statusMessage, setStatusMessage] = useState("");
  const [checkingStatus, setCheckingStatus] = useState(false);

  useEffect(() => {
    mounted.current = true;
    document.title = "LAN recovery · hostd";
    heading.current?.focus();
    return () => { mounted.current = false; };
  }, []);

  const loadReview = useCallback(async (focusError = true, focusSuccess = false) => {
    setLoading(true);
    setBlockingError("");
    setRequestError("");
    setStatusMessage("");
    setConfirmed(false);
    try {
      const head = exactRecoveryHead(await api.lanRecoveryHead());
      const next = await readExactObservation(head);
      if (!reviewMatchesHead(next)) throw new Error("inconsistent_recovery_observation");
      if (!mounted.current) return false;
      setReview(next);
      setCompleted(false);
      if (focusSuccess) window.setTimeout(() => heading.current?.focus(), 0);
      return true;
    } catch (error) {
      if (!mounted.current) return false;
      setReview(null);
      setBlockingError(recoveryReadError(error));
      if (focusError) window.setTimeout(() => errorSummary.current?.focus(), 0);
      return false;
    } finally {
      if (mounted.current) setLoading(false);
    }
  }, []);

  useEffect(() => { void loadReview(); }, [loadReview]);

  const reconcile = async () => {
    if (!review || !confirmed || pending || blockingError || reconcileInFlight.current) return;
    reconcileInFlight.current = true;
    setPending(true);
    setRequestError("");
    setStatusMessage("");
    try {
      const firstHead = exactRecoveryHead(await api.lanRecoveryHead());
      if (!sameRecoveryHead(review.head, firstHead) || recoveryKind(firstHead) !== review.kind) {
        throw new Error("changed_recovery_head");
      }
      const currentObservation = await readExactObservation(firstHead);
      if (!reviewMatchesHead(currentObservation, firstHead) || !sameReviewedObservation(review, currentObservation)) {
        throw new Error("inconsistent_recovery_observation");
      }
      const secondHead = exactRecoveryHead(await api.lanRecoveryHead());
      if (!sameRecoveryHead(firstHead, secondHead) || !sameRecoveryHead(review.head, secondHead) || !reviewMatchesHead(currentObservation, secondHead)) {
        throw new Error("changed_recovery_head");
      }

      if (review.kind === "lan_grant") {
        const result = await api.grantLANAccess(secondHead.appId, {
          attemptId: secondHead.operationId,
          accessRevisionId: secondHead.accessRevisionId,
          accessRevisionNumber: secondHead.accessRevisionNumber,
          approvalDigest: secondHead.approvalDigest,
        });
        if (!grantClaimMatchesHead(result.claim, secondHead, false)) throw new Error("ambiguous_recovery_response");
      } else {
        const result = await api.disableLANAccess(secondHead.appId, {
          operationId: secondHead.operationId,
          accessRevisionId: secondHead.accessRevisionId,
          accessRevisionNumber: secondHead.accessRevisionNumber,
          allocationId: secondHead.allocationId,
          approvalDigest: secondHead.approvalDigest,
        });
        if (!disableClaimMatchesHead(result.claim, secondHead, false)) throw new Error("ambiguous_recovery_response");
      }
      setConfirmed(false);
      setCompleted(true);
      setStatusMessage("Reconciliation completed for this recovery head. Restart Rig, then check system status.");
      window.setTimeout(() => completionSummary.current?.focus(), 0);
    } catch (error) {
      setConfirmed(false);
      if (error instanceof Error && (error.message === "changed_recovery_head" || error.message === "inconsistent_recovery_observation" || error.message === "unsupported_recovery_kind")) {
        setBlockingError("The recovery head or its exact observation changed during review. No request was sent. Restart Rig before continuing.");
      } else {
        const failure = postError(error);
        if (failure.blocking) {
          setBlockingError(failure.message);
        } else {
          setRequestError(failure.message);
          window.setTimeout(() => requestErrorSummary.current?.focus(), 0);
        }
      }
    } finally {
      reconcileInFlight.current = false;
      setPending(false);
    }
  };

  const checkAfterRestart = async () => {
    if (checkingStatus) return;
    setCheckingStatus(true);
    setRequestError("");
    setStatusMessage("");
    try {
      await onCheckSystemStatus();
    } finally {
      if (mounted.current) setCheckingStatus(false);
    }
  };

  return <main className="recovery-shell">
    <section className="recovery-card" aria-labelledby="recovery-title">
      <header className="recovery-header">
        <div className="auth-brand"><b aria-hidden="true">h&gt;</b><span>hostd</span></div>
        <button className="button" type="button" onClick={() => void onSignOut()}>Sign out</button>
      </header>
      <div className="recovery-heading">
        <p className="recovery-kicker">Local recovery console</p>
        <h1 id="recovery-title" ref={heading} tabIndex={-1}>LAN reconciliation required</h1>
        <p>Rig isolated the normal operator console until one proved LAN operation is deliberately reconciled.</p>
      </div>

      {loading && <div className="recovery-loading" role="status" aria-live="polite" aria-busy="true">Loading the exact recovery head…</div>}
      {blockingError && <div className="callout danger" ref={errorSummary} tabIndex={-1} role="alert"><strong>Recovery is blocked</strong><span>{blockingError}</span></div>}

      {!loading && review && <>
        <RecoveryFacts review={review}/>
        {requestError && <div className="callout danger" ref={requestErrorSummary} tabIndex={-1} role="alert"><strong>Reconciliation was not confirmed</strong><span>{requestError}</span></div>}
        {statusMessage && <div className="callout info" ref={completionSummary} tabIndex={-1} role="status" aria-live="polite"><span>{statusMessage}</span></div>}

        {!completed && !requestError && <div className="recovery-consent">
          <label><input type="checkbox" checked={confirmed} onChange={(event) => setConfirmed(event.target.checked)} disabled={pending || Boolean(blockingError)}/><span>I reviewed every recovery field above and want Rig to reconcile this exact operation.</span></label>
          <button className="button primary" type="button" aria-disabled={blockingError ? "true" : undefined} disabled={!blockingError && (!confirmed || pending)} onClick={() => void reconcile()}>{blockingError ? "Reconciliation blocked" : pending ? "Rechecking exact state…" : review.kind === "lan_grant" ? "Reconcile exact grant" : "Reconcile exact disable"}</button>
        </div>}
        {requestError && <button className="button" type="button" onClick={() => void loadReview(true, true)}>Load a fresh recovery review</button>}
        {completed && <button className="button primary" type="button" disabled={checkingStatus} onClick={() => void checkAfterRestart()}>{checkingStatus ? "Checking system status…" : "Check system status after restart"}</button>}
      </>}
    </section>
  </main>;
}

export const NORMAL_STATUS_RECHECK_MS = 30_000;
export const NORMAL_STATUS_PROBE_TIMEOUT_MS = 5_000;

type OperatorMode = "checking" | "normal" | "recovery" | "indeterminate";
type ProbeOptions = { hideNormal: boolean; allowRecoveryExit: boolean; restoreFocus: boolean };

export function AuthenticatedOperatorGate({ onSignOut, children }: { onSignOut: SignOut; children: ReactNode }) {
  const queryClient = useQueryClient();
  const heading = useRef<HTMLHeadingElement>(null);
  const probeSequence = useRef(0);
  const recoveryLatched = useRef(false);
  const isolateQueryCache = useRef(false);
  const clearingCache = useRef(false);
  const restoreFocusOnNormal = useRef(false);
  const [mode, setMode] = useState<OperatorMode>("checking");
  const modeRef = useRef<OperatorMode>(mode);
  modeRef.current = mode;

  const clearNormalState = useCallback(() => {
    if (clearingCache.current) return;
    clearingCache.current = true;
    try {
      void queryClient.cancelQueries();
      queryClient.clear();
    } finally {
      clearingCache.current = false;
    }
  }, [queryClient]);

  const enterRecovery = useCallback(() => {
    probeSequence.current += 1;
    recoveryLatched.current = true;
    isolateQueryCache.current = true;
    restoreFocusOnNormal.current = false;
    clearNormalState();
    setMode("recovery");
  }, [clearNormalState]);

  const enterIndeterminate = useCallback(() => {
    probeSequence.current += 1;
    recoveryLatched.current = false;
    isolateQueryCache.current = true;
    restoreFocusOnNormal.current = false;
    clearNormalState();
    setMode("indeterminate");
  }, [clearNormalState]);

  const probe = useCallback(async ({ hideNormal, allowRecoveryExit, restoreFocus }: ProbeOptions) => {
    if (recoveryLatched.current && !allowRecoveryExit) {
      setMode("recovery");
      return;
    }
    const sequence = ++probeSequence.current;
    if (hideNormal) setMode("checking");
    let timeoutId: number | undefined;
    try {
      const timeout = new Promise<never>((_, reject) => {
        timeoutId = window.setTimeout(() => reject(new Error("status_probe_timeout")), NORMAL_STATUS_PROBE_TIMEOUT_MS);
      });
      const status = await Promise.race([api.status(), timeout]);
      if (sequence !== probeSequence.current || (recoveryLatched.current && !allowRecoveryExit)) return;
      if (!validSystemStatus(status)) throw new Error("invalid_system_status");
      if (allowRecoveryExit) {
        recoveryLatched.current = false;
        clearNormalState();
      }
      isolateQueryCache.current = false;
      queryClient.setQueryData(["system-status"], status);
      restoreFocusOnNormal.current = restoreFocus;
      setMode("normal");
    } catch (error) {
      if (sequence !== probeSequence.current) return;
      if (isGatewayReconciliationRequired(error)) enterRecovery();
      else enterIndeterminate();
    } finally {
      if (timeoutId !== undefined) window.clearTimeout(timeoutId);
    }
  }, [clearNormalState, enterIndeterminate, enterRecovery, queryClient]);

  useEffect(() => queryClient.getQueryCache().subscribe(() => {
    if (!isolateQueryCache.current || clearingCache.current || queryClient.getQueryCache().getAll().length === 0) return;
    clearNormalState();
  }), [clearNormalState, queryClient]);
  useEffect(() => subscribeGatewayReconciliationRequired(enterRecovery), [enterRecovery]);
  useEffect(() => {
    void probe({ hideNormal: true, allowRecoveryExit: false, restoreFocus: false });
    return () => { probeSequence.current += 1; };
  }, [probe]);
  useEffect(() => {
    const recheckVisibleNormalMode = () => {
      if (modeRef.current === "normal") void probe({ hideNormal: true, allowRecoveryExit: false, restoreFocus: true });
    };
    const recheckOnVisibility = () => {
      if (document.visibilityState === "visible") recheckVisibleNormalMode();
    };
    const interval = window.setInterval(() => {
      if (modeRef.current === "normal" && document.visibilityState === "visible") {
        void probe({ hideNormal: false, allowRecoveryExit: false, restoreFocus: false });
      }
    }, NORMAL_STATUS_RECHECK_MS);
    window.addEventListener("focus", recheckVisibleNormalMode);
    document.addEventListener("visibilitychange", recheckOnVisibility);
    return () => {
      window.clearInterval(interval);
      window.removeEventListener("focus", recheckVisibleNormalMode);
      document.removeEventListener("visibilitychange", recheckOnVisibility);
    };
  }, [probe]);

  useEffect(() => {
    if (mode === "indeterminate") {
      document.title = "System status unavailable · hostd";
      heading.current?.focus();
    }
  }, [mode]);
  useEffect(() => {
    if (mode !== "normal" || !restoreFocusOnNormal.current) return;
    restoreFocusOnNormal.current = false;
    const timeout = window.setTimeout(() => document.getElementById("main")?.focus(), 0);
    return () => window.clearTimeout(timeout);
  }, [mode]);

  if (mode === "normal") return children;
  if (mode === "recovery") return <LANRecoveryScreen onSignOut={onSignOut} onCheckSystemStatus={() => probe({ hideNormal: true, allowRecoveryExit: true, restoreFocus: true })}/>;
  if (mode === "checking") return <main className="recovery-shell"><section className="recovery-card"><header className="recovery-header"><div className="auth-brand"><b aria-hidden="true">h&gt;</b><span>hostd</span></div><button className="button" type="button" onClick={() => void onSignOut()}>Sign out</button></header><div className="recovery-loading" role="status" aria-live="polite" aria-busy="true">Checking Rig system status…</div></section></main>;
  return <main className="recovery-shell">
    <section className="recovery-card recovery-indeterminate" aria-labelledby="status-title">
      <div className="auth-brand"><b aria-hidden="true">h&gt;</b><span>hostd</span></div>
      <h1 id="status-title" ref={heading} tabIndex={-1}>System status unavailable</h1>
      <div className="callout danger" role="alert">Rig could not prove whether the normal console or recovery console is safe to open.</div>
      <div className="recovery-actions"><button className="button primary" type="button" onClick={() => void probe({ hideNormal: true, allowRecoveryExit: true, restoreFocus: true })}>Retry</button><button className="button" type="button" onClick={() => void onSignOut()}>Sign out</button></div>
    </section>
  </main>;
}
