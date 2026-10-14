import { useEffect, useRef, useState } from "react";
import {
  api,
  type LANGatewayUpgradeRead,
} from "./api";

type SignOut = () => void | Promise<void>;
type UpgradeClaim = NonNullable<LANGatewayUpgradeRead["desiredClaim"]>;
type UpgradeProposal = NonNullable<LANGatewayUpgradeRead["proposal"]>;
type UpgradeSnapshot = {
  claim: UpgradeClaim;
  proposal: UpgradeProposal;
  observed: {
    availability: "serving" | "unavailable" | "unknown";
    operationId: string;
  };
};

const uuidPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;
const digestPattern = /^[0-9a-f]{64}$/;
const claimStates = new Set(["prepared", "serving", "unresolved", "committed", "rolled_back"]);
const observations = new Set(["serving", "unavailable", "unknown"]);

function record(value: unknown): Record<string, unknown> | null {
  return value !== null && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : null;
}

function exactUUID(value: unknown) {
  return typeof value === "string" && uuidPattern.test(value);
}

function exactDigest(value: unknown) {
  return typeof value === "string" && digestPattern.test(value);
}

function exactTimestamp(value: unknown) {
  return typeof value === "string" && value.length > 0 && Number.isFinite(Date.parse(value));
}

function readable(value: string) {
  return value.replaceAll("_", " ");
}

function validRecoveryPin(value: unknown, operationId: string) {
  const response = record(value);
  const locator = record(response?.recovery);
  return Boolean(response && response.version === 1 && response.mode === "recovery_only" &&
    locator?.kind === "gateway_upgrade" && locator.operationId === operationId && exactUUID(operationId));
}

function normalRecoveryCleared(value: unknown) {
  const response = record(value);
  return Boolean(response && response.version === 1 && response.mode === "normal" && !("recovery" in response));
}

function exactUpgradeSnapshot(value: unknown, operationId: string): UpgradeSnapshot {
  const read = record(value);
  const claim = record(read?.desiredClaim);
  const proposal = record(read?.proposal);
  const observed = record(read?.observed);
  if (!read || !claim || !proposal || !observed ||
      claim.operationId !== operationId || !exactUUID(claim.operationId) ||
      !exactUUID(claim.profileRevisionId) ||
      !Number.isSafeInteger(claim.profileRevisionNumber) || Number(claim.profileRevisionNumber) < 1 ||
      !exactDigest(claim.profileSpecDigest) || typeof claim.state !== "string" || !claimStates.has(claim.state) ||
      !exactTimestamp(claim.updatedAt) ||
      !exactUUID(proposal.profileRevisionId) ||
      !Number.isSafeInteger(proposal.profileRevisionNumber) || Number(proposal.profileRevisionNumber) < 1 ||
      !exactDigest(proposal.actionDigest) ||
      proposal.profileRevisionId !== claim.profileRevisionId ||
      proposal.profileRevisionNumber !== claim.profileRevisionNumber ||
      !exactUUID(observed.operationId) || observed.operationId !== operationId ||
      typeof observed.availability !== "string" || !observations.has(observed.availability)) {
    throw new Error("invalid_gateway_upgrade_recovery");
  }
  return {
    claim: {
      operationId: String(claim.operationId),
      profileRevisionId: String(claim.profileRevisionId),
      profileRevisionNumber: Number(claim.profileRevisionNumber),
      profileSpecDigest: String(claim.profileSpecDigest),
      state: String(claim.state),
      updatedAt: String(claim.updatedAt),
    },
    proposal: {
      profileRevisionId: String(proposal.profileRevisionId),
      profileRevisionNumber: Number(proposal.profileRevisionNumber),
      actionDigest: String(proposal.actionDigest),
    },
    observed: {
      operationId: String(observed.operationId),
      availability: observed.availability as UpgradeSnapshot["observed"]["availability"],
    },
  };
}

function sameSnapshot(left: UpgradeSnapshot, right: UpgradeSnapshot) {
  return left.claim.operationId === right.claim.operationId &&
    left.claim.profileRevisionId === right.claim.profileRevisionId &&
    left.claim.profileRevisionNumber === right.claim.profileRevisionNumber &&
    left.claim.profileSpecDigest === right.claim.profileSpecDigest &&
    left.claim.state === right.claim.state &&
    left.claim.updatedAt === right.claim.updatedAt &&
    left.proposal.profileRevisionId === right.proposal.profileRevisionId &&
    left.proposal.profileRevisionNumber === right.proposal.profileRevisionNumber &&
    left.proposal.actionDigest === right.proposal.actionDigest &&
    left.observed.operationId === right.observed.operationId &&
    left.observed.availability === right.observed.availability;
}

function resumableSnapshot(snapshot: UpgradeSnapshot) {
  return snapshot.claim.state === "prepared" || snapshot.claim.state === "serving" || snapshot.claim.state === "unresolved";
}
function terminalServing(snapshot: UpgradeSnapshot | null) {
  return Boolean(snapshot && (snapshot.claim.state === "committed" || snapshot.claim.state === "rolled_back") &&
    snapshot.observed.availability === "serving" && snapshot.observed.operationId === snapshot.claim.operationId);
}

function UpgradeFacts({ snapshot }: { snapshot: UpgradeSnapshot }) {
  return <dl className="recovery-facts">
    <div><dt>Recorded operation</dt><dd className="mono">{snapshot.claim.operationId}</dd></div>
    <div><dt>Gateway upgrade state</dt><dd>{readable(snapshot.claim.state)}</dd></div>
    <div><dt>Gateway profile revision</dt><dd>{snapshot.claim.profileRevisionNumber}</dd></div>
    <div><dt>Gateway profile ID</dt><dd className="mono">{snapshot.claim.profileRevisionId}</dd></div>
    <div><dt>Gateway profile digest</dt><dd className="mono">{snapshot.claim.profileSpecDigest}</dd></div>
    <div><dt>Recorded update</dt><dd>{snapshot.claim.updatedAt}</dd></div>
    <div><dt>Upgrade action digest</dt><dd className="mono">{snapshot.proposal.actionDigest}</dd></div>
    <div><dt>Observed gateway state</dt><dd>{readable(snapshot.observed.availability)}</dd></div>
    <div><dt>Observed operation</dt><dd className="mono">{snapshot.observed.operationId}</dd></div>
  </dl>;
}

export function LANGatewayUpgradeRecoveryScreen({ operationId, onSignOut, onCheckSystemStatus }: {
  operationId: string;
  onSignOut: SignOut;
  onCheckSystemStatus: () => Promise<void>;
}) {
  const heading = useRef<HTMLHeadingElement>(null);
  const errorTarget = useRef<HTMLDivElement>(null);
  const reviewTarget = useRef<HTMLDivElement>(null);
  const statusTarget = useRef<HTMLDivElement>(null);
  const reviewButton = useRef<HTMLButtonElement>(null);
  const operationKey = `gateway_upgrade:${operationId}`;
  const operationKeyRef = useRef(operationKey);
  const epoch = useRef(0);
  const inFlight = useRef<string | null>(null);
  const systemCheckInFlight = useRef(false);
  const [snapshot, setSnapshot] = useState<UpgradeSnapshot | null>(null);
  const [review, setReview] = useState<UpgradeSnapshot | null>(null);
  const [confirmed, setConfirmed] = useState(false);
  const [pending, setPending] = useState(false);
  const [checkingSystem, setCheckingSystem] = useState(false);
  const [unknownResult, setUnknownResult] = useState(false);
  const [normalDiscovered, setNormalDiscovered] = useState(false);
  const [error, setError] = useState("");

  const current = (requestEpoch: number, key: string) => epoch.current === requestEpoch && operationKeyRef.current === key;
  const begin = (key: string) => {
    if (inFlight.current) return false;
    inFlight.current = key;
    setPending(true);
    return true;
  };
  const finish = (key: string) => {
    if (inFlight.current === key) inFlight.current = null;
    if (operationKeyRef.current === key) setPending(false);
  };

  useEffect(() => {
    operationKeyRef.current = operationKey;
    epoch.current += 1;
    inFlight.current = null;
    systemCheckInFlight.current = false;
    setSnapshot(null);
    setReview(null);
    setConfirmed(false);
    setPending(false);
    setCheckingSystem(false);
    setUnknownResult(false);
    setNormalDiscovered(false);
    setError("");
    document.title = "LAN gateway upgrade recovery · hostd";
    heading.current?.focus();
    return () => { epoch.current += 1; };
  }, [operationKey]);

  useEffect(() => {
    if (review) window.setTimeout(() => reviewTarget.current?.focus(), 0);
  }, [review]);

  const readPinnedSnapshot = async (locator: unknown) => {
    if (!validRecoveryPin(locator, operationId)) throw new Error("invalid_upgrade_recovery_locator");
    return exactUpgradeSnapshot(await api.lanGatewayUpgrade(), operationId);
  };
  const readExactSnapshot = async () => readPinnedSnapshot(await api.getControllerRecovery());

  const focusError = () => window.setTimeout(() => errorTarget.current?.focus(), 0);
  const focusStatus = () => window.setTimeout(() => statusTarget.current?.focus(), 0);

  const reviewRecovery = async () => {
    const key = operationKey;
    if (!begin(key)) return;
    const requestEpoch = ++epoch.current;
    setError("");
    setConfirmed(false);
    setReview(null);
    setNormalDiscovered(false);
    try {
      const next = await readExactSnapshot();
      if (!current(requestEpoch, key)) return;
      setSnapshot(next);
      setReview(resumableSnapshot(next) ? next : null);
      setUnknownResult(false);
      if (!resumableSnapshot(next)) focusStatus();
    } catch {
      if (!current(requestEpoch, key)) return;
      setSnapshot(null);
      setUnknownResult(false);
      setError("Recovery cannot be confirmed. Check recovery again or sign out before taking further action.");
      focusError();
    } finally {
      finish(key);
    }
  };

  const checkOperationStatus = async () => {
    const key = operationKey;
    if (!begin(key)) return;
    const requestEpoch = ++epoch.current;
    setError("");
    setConfirmed(false);
    setReview(null);
    setNormalDiscovered(false);
    try {
      const locator = await api.getControllerRecovery();
      if (!current(requestEpoch, key)) return;
      if (normalRecoveryCleared(locator)) {
        setSnapshot(null);
        setUnknownResult(false);
        setNormalDiscovered(true);
        focusStatus();
        return;
      }
      const next = await readPinnedSnapshot(locator);
      if (!current(requestEpoch, key)) return;
      setSnapshot(next);
      setUnknownResult(false);
      focusStatus();
    } catch {
      if (!current(requestEpoch, key)) return;
      setSnapshot(null);
      setUnknownResult(true);
      setError("Recovery cannot be confirmed. Check operation status again or sign out before taking further action.");
      focusError();
    } finally {
      finish(key);
    }
  };

  const recover = async () => {
    const key = operationKey;
    const reviewed = review;
    if (!reviewed || !confirmed || !begin(key)) return;
    const requestEpoch = ++epoch.current;
    setError("");
    setConfirmed(false);
    try {
      const fresh = await readExactSnapshot();
      if (!current(requestEpoch, key)) return;
      if (!sameSnapshot(reviewed, fresh)) {
        setSnapshot(fresh);
        setReview(null);
        setError("The gateway upgrade changed during review. Review the latest details before recovering this operation.");
        focusError();
        return;
      }
      await api.recoverLANGatewayUpgrade({
        operationId: reviewed.claim.operationId,
        profileRevisionId: reviewed.proposal.profileRevisionId,
        profileRevisionNumber: reviewed.proposal.profileRevisionNumber,
        actionDigest: reviewed.proposal.actionDigest,
      });
      if (!current(requestEpoch, key)) return;
      setReview(null);
      setSnapshot(null);
      setUnknownResult(true);
      setError("The result is not confirmed. Check operation status before making another recovery request.");
      focusError();
    } catch {
      if (!current(requestEpoch, key)) return;
      setReview(null);
      setSnapshot(null);
      setUnknownResult(true);
      setError("The result is not confirmed. Check operation status before making another recovery request.");
      focusError();
    } finally {
      finish(key);
    }
  };

  const checkAfterRestart = async () => {
    if ((!terminalServing(snapshot) && !normalDiscovered) || systemCheckInFlight.current) return;
    systemCheckInFlight.current = true;
    setCheckingSystem(true);
    try {
      await onCheckSystemStatus();
    } finally {
      systemCheckInFlight.current = false;
      setCheckingSystem(false);
    }
  };

  const showTerminalCheck = terminalServing(snapshot);
  const canReview = Boolean(snapshot && !showTerminalCheck && resumableSnapshot(snapshot));

  return <main className="recovery-shell"><section className="recovery-card" aria-labelledby="gateway-upgrade-recovery-title">
    <header className="recovery-header"><div className="auth-brand"><b aria-hidden="true">h&gt;</b><span>hostd</span></div><button className="button" type="button" onClick={() => void onSignOut()}>Sign out</button></header>
    <div className="recovery-heading"><p className="recovery-kicker">Local recovery console</p><h1 id="gateway-upgrade-recovery-title" ref={heading} tabIndex={-1}>LAN gateway upgrade recovery required</h1><p>Review the recorded gateway upgrade. Rig requires the administrator who originally approved this upgrade before it can continue.</p></div>
    {error && <div className="callout danger" ref={errorTarget} tabIndex={-1} role="alert"><strong>Recovery cannot be confirmed</strong><span>{error}</span></div>}
    {snapshot && <UpgradeFacts snapshot={snapshot}/>}
    {!snapshot && !normalDiscovered && !unknownResult && !error && <div className="callout info" role="status"><strong>Recorded gateway upgrade</strong><span>Review the saved operation before deciding whether recovery can continue.</span></div>}
    {normalDiscovered && <div className="callout info" role="status" ref={statusTarget} tabIndex={-1}><strong>System status confirmation required</strong><span>Rig reports no startup recovery operation. This does not confirm the upgrade outcome or a serving gateway. Check system status before returning to the console.</span></div>}
    {snapshot && !review && canReview && <div className="callout info" role="status" ref={statusTarget} tabIndex={-1}><strong>Operation status checked</strong><span>The recorded upgrade is {readable(snapshot.claim.state)}. Review upgrade recovery to continue.</span></div>}
    {snapshot && showTerminalCheck && <div className="callout warning" role="status" ref={statusTarget} tabIndex={-1}><strong>Recorded gateway upgrade is terminal</strong><span>Rig reports a serving gateway for this recorded operation. Restart Rig, then check system status.</span></div>}
    {snapshot && !showTerminalCheck && (snapshot.claim.state === "committed" || snapshot.claim.state === "rolled_back") && <div className="callout warning" role="status" ref={statusTarget} tabIndex={-1}><strong>Terminal outcome needs a live gateway check</strong><span>The recorded upgrade is terminal, but Rig has not confirmed a serving gateway. Keep recovery open and check operation status.</span></div>}
    {unknownResult && <div className="callout warning" role="status"><strong>The result is not confirmed</strong><span>Check this operation before making another recovery request.</span></div>}
    {!review && !normalDiscovered && !unknownResult && !showTerminalCheck && !snapshot && <div className="recovery-actions"><button ref={reviewButton} className="button primary" type="button" disabled={pending} onClick={() => void reviewRecovery()}>{pending ? "Reviewing recovery…" : "Review upgrade recovery"}</button></div>}
    {!review && !normalDiscovered && !unknownResult && canReview && <div className="recovery-actions"><button ref={reviewButton} className="button primary" type="button" disabled={pending} onClick={() => void reviewRecovery()}>{pending ? "Reviewing recovery…" : "Review upgrade recovery"}</button></div>}
    {!review && !normalDiscovered && (!snapshot || unknownResult || (!canReview && !showTerminalCheck)) && <div className="recovery-actions"><button className="button primary" type="button" disabled={pending} onClick={() => void checkOperationStatus()}>{pending ? "Checking operation…" : unknownResult ? "Check operation status" : "Check recovery again"}</button></div>}
    {(showTerminalCheck || normalDiscovered) && <div className="recovery-actions"><button className="button primary" type="button" disabled={checkingSystem} onClick={() => void checkAfterRestart()}>{checkingSystem ? "Checking system status…" : "Check system status after restart"}</button></div>}
    {review && <div className="recovery-consent" ref={reviewTarget} tabIndex={-1}>
      <div className="callout info" role="status"><strong>Recovery review ready</strong><span>Rig will determine the safe outcome from the recorded operation and current runtime state.</span></div>
      <label><input type="checkbox" checked={confirmed} disabled={pending} onChange={(event) => setConfirmed(event.target.checked)}/><span>I approve recovery of this gateway upgrade.</span></label>
      <div className="recovery-actions"><button className="button" type="button" disabled={pending} onClick={() => { setReview(null); setConfirmed(false); window.setTimeout(() => reviewButton.current?.focus(), 0); }}>Cancel</button><button className="button primary" type="button" disabled={!confirmed || pending} onClick={() => void recover()}>{pending ? "Recovering upgrade…" : "Recover this upgrade"}</button></div>
    </div>}
  </section></main>;
}
