import { useEffect, useId, useMemo, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  api,
  type LANGatewayCandidate,
  type LANGatewayProfileRead,
  type LANGatewayProfileSpec,
  type LANGatewayUpgradeProposal,
} from "./api";
import { Dialog } from "./dialog";

function administrator(role: string) {
  return role.trim().toLowerCase() === "administrator";
}

function candidateID(candidate: LANGatewayCandidate) {
  return `${candidate.interfaceId}\u0000${candidate.selectedIpv4}`;
}

function profileSpecMatches(left: LANGatewayProfileSpec, right: LANGatewayProfileSpec) {
  return left.interfaceId === right.interfaceId && left.selectedIpv4 === right.selectedIpv4 &&
    left.portStart === right.portStart && left.portEnd === right.portEnd;
}

function profileProposalIsExact(response: LANGatewayProfileRead, spec: LANGatewayProfileSpec) {
  return Boolean(response.proposal && /^[a-f0-9]{64}$/.test(response.proposal.approvalDigest) &&
    profileSpecMatches(response.proposal.spec, spec));
}

function poolText(spec: Pick<LANGatewayProfileSpec, "portStart" | "portEnd">) {
  return spec.portStart === spec.portEnd ? String(spec.portStart) : `${spec.portStart}–${spec.portEnd}`;
}

function candidateLabel(candidate: LANGatewayCandidate) {
  return `${candidate.name} · ${candidate.selectedIpv4}${candidate.prefix ? `/${candidate.prefix}` : ""}`;
}

function readableState(value: string) {
  return value.replaceAll("_", " ");
}

type GatewayUpgradeClaim = {
  operationId: string;
  profileRevisionNumber: number;
  state: string;
  updatedAt: string;
};

type GatewayUpgradeObservation = {
  availability: string;
};

type GatewayProfileRevisionIdentity = {
  id: string;
  revisionNumber: number;
};

function proposalMatchesProfile(proposal: LANGatewayUpgradeProposal | undefined, profile: GatewayProfileRevisionIdentity | undefined) {
  return Boolean(proposal && profile && proposal.profileRevisionId === profile.id && proposal.profileRevisionNumber === profile.revisionNumber);
}

function sameUpgradeProposal(left: LANGatewayUpgradeProposal | undefined, right: LANGatewayUpgradeProposal | undefined) {
  return Boolean(left && right && left.profileRevisionId === right.profileRevisionId &&
    left.profileRevisionNumber === right.profileRevisionNumber && left.actionDigest === right.actionDigest);
}

function gatewayUpgradeGuidance(claim: GatewayUpgradeClaim | undefined, observed: GatewayUpgradeObservation) {
  if (!claim) return null;
  if (claim.state === "committed") {
    if (observed.availability === "serving") {
      return { title: "Gateway upgrade committed and observed serving", body: "This gateway is ready for a separately approved application LAN route. Rig will not start a second gateway upgrade." };
    }
    return { title: "Gateway upgrade committed; live state needs attention", body: "Do not start another gateway upgrade while the committed operation is observed as unavailable or unknown. Check the gateway state and use the controller recovery flow if it does not become serving." };
  }
  if (claim.state === "prepared" || claim.state === "serving") {
    return { title: "Gateway upgrade is in progress", body: "This exact gateway operation is still prepared or serving. Check the gateway state; Rig will not start a competing upgrade." };
  }
  if (claim.state === "unresolved") {
    return { title: "Gateway upgrade recovery required", body: "The controller could not establish a safe terminal result for this gateway operation. Keep LAN sharing closed and use the controller recovery flow before any further upgrade." };
  }
  return { title: "Gateway upgrade state needs review", body: "Rig will not start another gateway upgrade until the current gateway state is understood." };
}

type ProfileReview = {
  response: LANGatewayProfileRead;
  spec: LANGatewayProfileSpec;
};

export function LANGatewayPanel({ role }: { role: string }) {
  const queryClient = useQueryClient();
  const canManage = administrator(role);
  const profile = useQuery({
    queryKey: ["lan-gateway-profile"],
    queryFn: () => api.lanGatewayProfile(),
    enabled: canManage,
    retry: false,
    refetchOnWindowFocus: false,
  });
  const upgrade = useQuery({
    queryKey: ["lan-gateway-upgrade"],
    queryFn: api.lanGatewayUpgrade,
    enabled: canManage,
    retry: false,
    refetchOnWindowFocus: false,
  });
  const [candidate, setCandidate] = useState("");
  const [portStart, setPortStart] = useState(8100);
  const [portEnd, setPortEnd] = useState(8119);
  const [profileReview, setProfileReview] = useState<ProfileReview | null>(null);
  const [upgradeReview, setUpgradeReview] = useState<LANGatewayUpgradeProposal | null>(null);
  const [message, setMessage] = useState("");
  const [reviewError, setReviewError] = useState("");
  const [focusAfterUpgrade, setFocusAfterUpgrade] = useState(false);
  const portValidationId = useId();
  const gatewayUpgradeHeading = useRef<HTMLHeadingElement>(null);
  const gatewayUpgradeGuidanceTarget = useRef<HTMLDivElement>(null);
  const profileApprovalErrorTarget = useRef<HTMLDivElement>(null);
  const gatewayUpgradeErrorTarget = useRef<HTMLDivElement>(null);

  const candidates = profile.data?.candidates ?? [];
  const desired = profile.data?.desiredProfile;
  useEffect(() => {
    const matchingDesired = desired && candidates.find((item) => item.interfaceId === desired.spec.interfaceId && item.selectedIpv4 === desired.spec.selectedIpv4);
    const selected = candidates.find((item) => candidateID(item) === candidate);
    if (selected) return;
    const next = matchingDesired ?? candidates[0];
    if (!next) return;
    setCandidate(candidateID(next));
    setPortStart(desired?.spec.portStart ?? 8100);
    setPortEnd(desired?.spec.portEnd ?? 8119);
  }, [candidate, candidates, desired]);

  const selectedCandidate = useMemo(() => candidates.find((item) => candidateID(item) === candidate), [candidate, candidates]);
  const validPortPool = Number.isInteger(portStart) && Number.isInteger(portEnd) &&
    portStart >= 8100 && portStart <= 8119 && portEnd >= portStart && portEnd <= 8119
  const selectedSpec = selectedCandidate && validPortPool
    ? { interfaceId: selectedCandidate.interfaceId, selectedIpv4: selectedCandidate.selectedIpv4, portStart, portEnd }
    : null;
  const requestProfileReview = useMutation({
    mutationFn: (spec: LANGatewayProfileSpec) => api.lanGatewayProfile(spec),
    onSuccess: (response, spec) => {
      if (!profileProposalIsExact(response, spec)) {
        setReviewError("Rig could not obtain an exact approval digest for this profile. Refresh the interface list before reviewing it.");
        return;
      }
      setReviewError("");
      setProfileReview({ response, spec });
    },
    onError: (error) => setReviewError(error instanceof Error ? error.message : "Could not review the LAN gateway profile."),
  });
  const configureProfile = useMutation({
    mutationFn: (review: ProfileReview) => api.configureLANGatewayProfile({
      operationId: crypto.randomUUID(),
      expectedRevisionNumber: review.response.expectedRevisionNumber,
      spec: review.spec,
      approvalDigest: review.response.proposal!.approvalDigest,
    }),
    onSuccess: async (result) => {
      setProfileReview(null);
      setMessage(`Desired LAN gateway profile revision ${result.profile.revisionNumber} was saved. It has not opened a LAN listener.`);
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ["lan-gateway-profile"] }),
        queryClient.invalidateQueries({ queryKey: ["lan-gateway-upgrade"] }),
      ]);
    },
  });
  const upgradeGateway = useMutation({
    mutationFn: (proposal: LANGatewayUpgradeProposal) => api.upgradeLANGateway({
      operationId: crypto.randomUUID(),
      profileRevisionId: proposal.profileRevisionId,
      profileRevisionNumber: proposal.profileRevisionNumber,
      actionDigest: proposal.actionDigest,
    }),
    onSuccess: async (result) => {
      setUpgradeReview(null);
      setMessage(`Gateway operation ${result.claim.operationId} is ${result.claim.state}. Check the observed gateway state before sharing an application.`);
      await queryClient.invalidateQueries({ queryKey: ["lan-gateway-upgrade"] });
      setFocusAfterUpgrade(true);
    },
  });

  const refresh = async () => {
    setReviewError("");
    setMessage("Checking the current LAN gateway state.");
    const [profileResult, upgradeResult] = await Promise.all([profile.refetch(), upgrade.refetch()]);
    setMessage(profileResult.isError || upgradeResult.isError
      ? "Rig could not refresh the complete LAN gateway state. Review the reported error before approving a change."
      : "LAN gateway state refreshed.");
  };

  const profileBusy = requestProfileReview.isPending || configureProfile.isPending;
  const upgradeBusy = upgradeGateway.isPending;
  const configuredProfile = profile.data?.desiredProfile;
  const proposal = upgrade.data?.proposal;
  const desiredClaim = upgrade.data?.desiredClaim;
  const upgradeGuidance = gatewayUpgradeGuidance(desiredClaim, upgrade.data?.observed ?? { availability: "unknown" });
  const readsChecking = profile.isFetching || upgrade.isFetching;
  const proposalMatchesCurrentProfile = proposalMatchesProfile(proposal, configuredProfile);
  const canReviewUpgrade = Boolean(profile.isSuccess && upgrade.isSuccess && !readsChecking && !desiredClaim &&
    proposalMatchesCurrentProfile && proposal && /^[a-f0-9]{64}$/.test(proposal.actionDigest));
  const canConfirmUpgrade = Boolean(canReviewUpgrade && sameUpgradeProposal(upgradeReview ?? undefined, proposal));
  const openUpgradeReview = () => {
    if (!canReviewUpgrade || !proposal) {
      setMessage("Gateway state changed before this upgrade could be reviewed. Refresh the profile and gateway state before trying again.");
      return;
    }
    setUpgradeReview(proposal);
  };
  const confirmUpgradeReview = () => {
    if (!upgradeReview || !canConfirmUpgrade) {
      setUpgradeReview(null);
      setMessage("Gateway state changed while this review was open. Refresh the profile and gateway state before opening another review.");
      return;
    }
    upgradeGateway.mutate(upgradeReview);
  };

  useEffect(() => {
    if (!focusAfterUpgrade) return;
    const timer = window.setTimeout(() => {
      (gatewayUpgradeGuidanceTarget.current ?? gatewayUpgradeHeading.current)?.focus();
      setFocusAfterUpgrade(false);
    }, 0);
    return () => window.clearTimeout(timer);
  }, [focusAfterUpgrade, upgrade.data?.desiredClaim?.state, upgrade.isFetching]);

  useEffect(() => {
    if (configureProfile.isError && profileReview) profileApprovalErrorTarget.current?.focus();
  }, [configureProfile.isError, profileReview]);

  useEffect(() => {
    if (upgradeReview && (upgradeGateway.isError || !canConfirmUpgrade)) gatewayUpgradeErrorTarget.current?.focus();
  }, [upgradeGateway.isError, upgradeReview, canConfirmUpgrade]);

  if (!canManage) {
    return <section className="lan-gateway-panel" aria-labelledby="lan-gateway-title">
      <div className="lan-heading"><div><h2 id="lan-gateway-title">LAN gateway</h2><p>LAN gateway details and controls require an administrator.</p></div></div>
    </section>;
  }

  return <section className="lan-gateway-panel" aria-labelledby="lan-gateway-title">
    <div className="lan-heading"><div><h2 id="lan-gateway-title">LAN gateway</h2><p>Choose the controller interface and HTTP port pool for explicit LAN sharing.</p></div><button className="button small" type="button" disabled={profile.isFetching || upgrade.isFetching || profileBusy || upgradeBusy} onClick={() => void refresh()}>Check gateway state</button></div>
    <span className="sr-only" role="status" aria-live="polite" aria-atomic="true">{profileBusy ? "Reviewing LAN gateway profile." : upgradeBusy ? "Applying LAN gateway upgrade." : message}</span>
    {message && <p className="lan-message">{message}</p>}
    {profile.isLoading ? <p role="status">Loading LAN gateway profile…</p> : profile.isError ? <div className="callout danger" role="status"><strong>LAN gateway profile could not be loaded.</strong><span>{profile.error.message}</span><button className="button small" type="button" onClick={() => void profile.refetch()}>Retry profile check</button></div> : <>
      {configuredProfile ? <dl className="lan-details"><div><dt>Desired profile revision</dt><dd>{configuredProfile.revisionNumber}</dd></div><div><dt>Interface</dt><dd>{configuredProfile.spec.interfaceId}</dd></div><div><dt>Selected IPv4</dt><dd>{configuredProfile.spec.selectedIpv4}</dd></div><div><dt>Port pool</dt><dd>{poolText(configuredProfile.spec)}</dd></div></dl> : <div className="callout info"><strong>LAN sharing is not configured.</strong><span>Approving a profile records desired state only. It does not bind a port or publish an address.</span></div>}
      {candidates.length === 0 ? <div className="callout warning"><strong>No private IPv4 interface is available.</strong><span>Connect a private RFC 1918 network to this controller, then check the gateway state again.</span></div> : <form className="lan-profile-form" onSubmit={(event) => { event.preventDefault(); setReviewError(""); if (selectedSpec) requestProfileReview.mutate(selectedSpec); }} noValidate aria-busy={profileBusy}>
        <div className="field"><label htmlFor="lan-interface">Interface and IPv4 address</label><select id="lan-interface" value={candidate} disabled={profileBusy} onChange={(event) => setCandidate(event.target.value)}>{candidates.map((item) => <option key={candidateID(item)} value={candidateID(item)}>{candidateLabel(item)}</option>)}</select><small>Only current private IPv4 interface and address pairs are selectable.</small></div>
        <div className="lan-port-pool"><div className="field"><label htmlFor="lan-port-start">First port</label><input id="lan-port-start" type="number" inputMode="numeric" min={8100} max={8119} value={portStart} disabled={profileBusy} aria-invalid={!validPortPool || undefined} aria-describedby={!validPortPool ? portValidationId : undefined} onChange={(event) => setPortStart(Number(event.target.value))}/></div><div className="field"><label htmlFor="lan-port-end">Last port</label><input id="lan-port-end" type="number" inputMode="numeric" min={8100} max={8119} value={portEnd} disabled={profileBusy} aria-invalid={!validPortPool || undefined} aria-describedby={!validPortPool ? portValidationId : undefined} onChange={(event) => setPortEnd(Number(event.target.value))}/></div></div>
        {!validPortPool && <p id={portValidationId} className="form-error" role="alert">Choose a port pool from 8100 through 8119, with the last port no lower than the first.</p>}
        {reviewError && <div className="callout danger" role="alert"><strong>Profile review unavailable</strong><span>{reviewError}</span><button className="button small" type="button" onClick={() => void refresh()}>Refresh profile summary</button></div>}
        <button className="button" type="submit" disabled={!selectedSpec || profileBusy}>{requestProfileReview.isPending ? "Preparing review…" : "Review LAN gateway profile"}</button>
      </form>}
    </>}
    <section className="lan-upgrade" aria-labelledby="lan-upgrade-title">
      <div className="lan-section-heading"><div><h3 ref={gatewayUpgradeHeading} id="lan-upgrade-title" tabIndex={-1}>Gateway upgrade</h3><p>The existing controller-host route stays separate while a v2 gateway is approved and observed.</p></div></div>
      {upgrade.isLoading ? <p role="status">Loading gateway observation…</p> : upgrade.isError ? <div className="callout danger" role="status"><strong>Gateway state could not be loaded.</strong><span>{upgrade.error.message}</span><button className="button small" type="button" onClick={() => void upgrade.refetch()}>Retry gateway check</button></div> : <>
        <dl className="lan-details"><div><dt>Observed gateway state</dt><dd>{readableState(upgrade.data?.observed.availability ?? "unknown")}</dd></div>{upgrade.data?.observed.operationId && <div><dt>Observed operation</dt><dd className="mono">{upgrade.data.observed.operationId}</dd></div>}{desiredClaim && <><div><dt>Desired gateway state</dt><dd>{readableState(desiredClaim.state)}</dd></div><div><dt>Desired operation</dt><dd className="mono">{desiredClaim.operationId}</dd></div><div><dt>Claim profile revision</dt><dd>{desiredClaim.profileRevisionNumber}</dd></div><div><dt>Claim updated</dt><dd>{desiredClaim.updatedAt}</dd></div></>}</dl>
        {upgradeGuidance ? <div ref={gatewayUpgradeGuidanceTarget} className="callout warning" role="status" tabIndex={-1}><strong>{upgradeGuidance.title}</strong><span>{upgradeGuidance.body}</span></div> : !configuredProfile ? <p className="lan-muted">Approve a LAN gateway profile before reviewing the gateway upgrade.</p> : !canReviewUpgrade ? <div className="callout warning" role="status"><strong>Gateway confirmation is unavailable.</strong><span>{readsChecking ? "Rig is checking the current gateway profile and observation. Wait for that check to finish before reviewing an upgrade." : proposal && !proposalMatchesCurrentProfile ? "The available gateway proposal does not match the current desired profile. Refresh the gateway state before reviewing an upgrade." : "Refresh the gateway state to obtain an exact action digest. Rig will not start an upgrade without it."}</span></div> : <button className="button" type="button" disabled={upgradeBusy} onClick={openUpgradeReview}>Review gateway upgrade</button>}
      </>}
    </section>
    {profileReview && <Dialog title="Review LAN gateway profile" description="Confirm the exact controller interface, address, pool, and approval digest. This saves desired state only." pending={configureProfile.isPending} focusTitle close={() => setProfileReview(null)}>
      <dl className="lan-review-details"><div><dt>Profile revision</dt><dd>New revision {profileReview.response.expectedRevisionNumber + 1}</dd></div><div><dt>Interface</dt><dd>{profileReview.spec.interfaceId}</dd></div><div><dt>Private IPv4</dt><dd>{profileReview.spec.selectedIpv4}</dd></div><div><dt>Port pool</dt><dd>{poolText(profileReview.spec)}</dd></div><div><dt>Approval digest</dt><dd className="mono">{profileReview.response.proposal?.approvalDigest}</dd></div></dl>
      {configureProfile.isError ? <div ref={profileApprovalErrorTarget} className="callout danger" role="alert" tabIndex={-1}><strong>Profile approval was not completed.</strong><span>{configureProfile.error.message}</span><button className="button small" type="button" onClick={() => { setProfileReview(null); void refresh(); }}>Refresh profile summary</button></div> : <div className="deployment-dialog-actions"><button className="button" type="button" disabled={configureProfile.isPending} onClick={() => setProfileReview(null)}>Cancel</button><button className="button primary" type="button" disabled={configureProfile.isPending} onClick={() => configureProfile.mutate(profileReview)}>{configureProfile.isPending ? "Approving…" : "Approve desired profile"}</button></div>}
    </Dialog>}
    {upgradeReview && <Dialog title="Review LAN gateway upgrade" description="Confirm this exact gateway action. It can change the attested gateway, but does not itself share an application." pending={upgradeGateway.isPending} focusTitle close={() => setUpgradeReview(null)}>
      <dl className="lan-review-details"><div><dt>Profile revision</dt><dd>{upgradeReview.profileRevisionNumber}</dd></div><div><dt>Profile identifier</dt><dd className="mono">{upgradeReview.profileRevisionId}</dd></div><div><dt>Action digest</dt><dd className="mono">{upgradeReview.actionDigest}</dd></div></dl>
      {upgradeGateway.isError ? <div ref={gatewayUpgradeErrorTarget} className="callout danger" role="alert" tabIndex={-1}><strong>Gateway upgrade was not completed.</strong><span>{upgradeGateway.error.message}</span><button className="button small" type="button" onClick={() => { setUpgradeReview(null); void refresh(); }}>Refresh gateway state</button></div> : !canConfirmUpgrade ? <div ref={gatewayUpgradeErrorTarget} className="callout warning" role="alert" tabIndex={-1}><strong>Gateway review expired</strong><span>The current profile or gateway proposal no longer matches this review. Refresh the gateway state before opening another review.</span><button className="button small" type="button" onClick={() => { setUpgradeReview(null); void refresh(); }}>Refresh gateway state</button></div> : <div className="deployment-dialog-actions"><button className="button" type="button" disabled={upgradeGateway.isPending} onClick={() => setUpgradeReview(null)}>Cancel</button><button className="button primary" type="button" disabled={upgradeGateway.isPending} onClick={confirmUpgradeReview}>{upgradeGateway.isPending ? "Upgrading…" : "Approve gateway upgrade"}</button></div>}
    </Dialog>}
  </section>;
}
