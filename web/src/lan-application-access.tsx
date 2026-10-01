import { useEffect, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  api,
  lanAccessObservationFresh,
  verifiedLANAccessURL,
  type LANAppAccessRevision,
  type LANAppAccessReservationMutation,
  type LANAppGrantClaim,
} from "./api";
import { Dialog } from "./dialog";
import { useLocalRouteExpiry } from "./use-local-route-expiry";

function administrator(role: string) {
  return role.trim().toLowerCase() === "administrator";
}

type ReservationReview = {
  operationId: string;
  expectedRevisionNumber: number;
  reservation: LANAppAccessReservationMutation;
};

type ReservationRequest = {
  operationId: string;
  expectedRevisionNumber: number;
  profileRevisionId: string;
  profileRevisionNumber: number;
};

type ClaimIdentity = Pick<LANAppGrantClaim, "attemptId" | "state" | "updatedAt">;

function shortState(value: string) {
  return value.replaceAll("_", " ");
}

export function LANApplicationAccessPanel({ appId, role, enabled }: { appId: string; role: string; enabled: boolean }) {
  const queryClient = useQueryClient();
  const canManage = administrator(role);
  const access = useQuery({
    queryKey: ["lan-app-access", appId],
    queryFn: () => api.lanAccess(appId),
    enabled: enabled && canManage,
    retry: false,
    refetchOnWindowFocus: false,
  });
  const profile = useQuery({
    queryKey: ["lan-gateway-profile"],
    queryFn: () => api.lanGatewayProfile(),
    enabled: enabled && canManage,
    retry: false,
    refetchOnWindowFocus: false,
  });
  const [reserved, setReserved] = useState<ReservationReview | null>(null);
  const [reservationReview, setReservationReview] = useState<ReservationReview | null>(null);
  const [unresolvedReservation, setUnresolvedReservation] = useState<ReservationRequest | null>(null);
  const [approved, setApproved] = useState<LANAppAccessRevision | null>(null);
  const [grantReview, setGrantReview] = useState<LANAppAccessRevision | null>(null);
  const [claim, setClaim] = useState<ClaimIdentity | null>(null);
  const [message, setMessage] = useState("");
  const [focusAfterReservationReview, setFocusAfterReservationReview] = useState(false);
  const [focusAfterApproval, setFocusAfterApproval] = useState(false);
  const [focusAfterGrant, setFocusAfterGrant] = useState(false);
  const reservationReviewButton = useRef<HTMLButtonElement>(null);
  const activationReviewButton = useRef<HTMLButtonElement>(null);
  const claimHeading = useRef<HTMLHeadingElement>(null);
  const lanURL = access.isFetching ? null : verifiedLANAccessURL(access.data);
  const routeExpired = !access.isFetching && access.isSuccess && access.data?.availability === "verified" && !lanAccessObservationFresh(access.data);
  const accessRefreshWithheld = access.isFetching && access.data?.availability === "verified";
  useLocalRouteExpiry(access.data?.observedAt);
  const grantState = useQuery({
    queryKey: ["lan-app-grant", appId, claim?.attemptId],
    queryFn: () => api.lanGrant(appId, claim!.attemptId),
    enabled: Boolean(claim?.attemptId) && enabled && canManage,
    retry: false,
    refetchOnWindowFocus: false,
  });
  const reserve = useMutation({
    mutationFn: (request: ReservationRequest) =>
      api.reserveLANAccess(appId, {
        operationId: request.operationId,
        expectedRevisionNumber: request.expectedRevisionNumber,
        gatewayProfileRevisionId: request.profileRevisionId,
        gatewayProfileRevisionNumber: request.profileRevisionNumber,
      }),
    onSuccess: (reservation, request) => {
      if (reservation.allocation.ownerOperationId !== request.operationId || !/^[a-f0-9]{64}$/.test(reservation.approvalDigest)) {
        setUnresolvedReservation(request);
        setMessage("Rig could not verify the returned LAN reservation. Refresh access before taking another action.");
        return;
      }
      setUnresolvedReservation(null);
      setMessage("");
      const review = { operationId: request.operationId, expectedRevisionNumber: request.expectedRevisionNumber, reservation };
      setReserved(review);
      setReservationReview(review);
    },
    onError: (_error, request) => {
      setUnresolvedReservation(request);
      setMessage(`The LAN reservation result for operation ${request.operationId} is uncertain. Rig will only replay that exact request or reconcile the current access state.`);
    },
  });
  const approve = useMutation({
    mutationFn: (review: ReservationReview) => api.approveLANAccess(appId, {
      operationId: review.operationId,
      allocationId: review.reservation.allocation.id,
      expectedRevisionNumber: review.expectedRevisionNumber,
      approvalDigest: review.reservation.approvalDigest,
    }),
    onSuccess: async (result) => {
      setReservationReview(null);
      setReserved(null);
      setApproved(result.revision);
      setFocusAfterApproval(true);
      setMessage(`LAN access revision ${result.revision.revisionNumber} was approved. Review activation before any address can be used.`);
      await queryClient.invalidateQueries({ queryKey: ["lan-app-access", appId] });
    },
  });
  const grant = useMutation({
    mutationFn: ({ revision, attemptId }: { revision: LANAppAccessRevision; attemptId: string }) => {
      return api.grantLANAccess(appId, {
        attemptId,
        accessRevisionId: revision.id,
        accessRevisionNumber: revision.revisionNumber,
        approvalDigest: revision.specDigest,
      }).then((result) => ({ result, attemptId }));
    },
    onSuccess: async ({ result, attemptId }) => {
      setGrantReview(null);
      setClaim({ attemptId, state: result.claim.state, updatedAt: result.claim.updatedAt });
      setFocusAfterGrant(true);
      setMessage(`LAN activation claim ${result.claim.attemptId} is ${shortState(result.claim.state)}. Check its observation before using an address.`);
      await queryClient.invalidateQueries({ queryKey: ["lan-app-access", appId] });
    },
    onError: (error, request) => {
      setClaim({ attemptId: request.attemptId, state: "unresolved", updatedAt: "" });
      setFocusAfterGrant(true);
      setMessage(`LAN activation claim ${request.attemptId} needs recovery. Rig will not start a replacement grant without that exact claim identity.`);
    },
  });

  const refresh = async () => {
    setMessage("Checking the latest LAN access observation.");
    const [accessResult, profileResult, claimResult] = await Promise.all([access.refetch(), profile.refetch(), claim?.attemptId ? grantState.refetch() : Promise.resolve(undefined)]);
    if (unresolvedReservation && accessResult.data?.desiredAccess?.operationId === unresolvedReservation.operationId) {
      setUnresolvedReservation(null);
      reserve.reset();
      setMessage(`The current access state confirms reservation operation ${unresolvedReservation.operationId} progressed. Rig will not create a replacement reservation.`);
      return;
    }
    setMessage(accessResult.isError || profileResult.isError || claimResult?.isError
      ? "Rig could not refresh the complete LAN access observation. Review the reported error before using an address."
      : "LAN access observation refreshed.");
  };

  useEffect(() => {
    if (!focusAfterReservationReview || !reserved) return;
    const timer = window.setTimeout(() => {
      reservationReviewButton.current?.focus();
      setFocusAfterReservationReview(false);
    }, 0);
    return () => window.clearTimeout(timer);
  }, [focusAfterReservationReview, reserved]);

  useEffect(() => {
    if (!focusAfterApproval || !approved || claim) return;
    const timer = window.setTimeout(() => {
      activationReviewButton.current?.focus();
      setFocusAfterApproval(false);
    }, 0);
    return () => window.clearTimeout(timer);
  }, [approved, claim, focusAfterApproval]);

  useEffect(() => {
    if (!focusAfterGrant || !claim) return;
    const timer = window.setTimeout(() => {
      claimHeading.current?.focus();
      setFocusAfterGrant(false);
    }, 0);
    return () => window.clearTimeout(timer);
  }, [claim, focusAfterGrant]);

  useEffect(() => {
    if (access.isFetching || access.isError || !access.data) return;
    if (lanURL) {
      setMessage(`LAN route verified at ${access.data.observedAt}.`);
    } else if (routeExpired) {
      setMessage("LAN route verification expired. Check LAN access again before opening or copying an address.");
    }
  }, [access.data, access.isError, access.isFetching, lanURL, routeExpired]);

  const currentRevision = access.data?.desiredAccess ?? approved;
  const currentProfile = profile.data?.desiredProfile;
  const canReserve = Boolean(access.data && currentProfile && !currentRevision && !reserved && !unresolvedReservation && !access.isFetching && !reserve.isPending);
  const pendingReservation = reserved?.reservation;
  const closeReservationReview = () => {
    setReservationReview(null);
    setFocusAfterReservationReview(true);
  };

  if (!enabled) return <section className="lan-access-panel" aria-labelledby="lan-access-title"><div className="lan-heading"><div><h2 id="lan-access-title">LAN access</h2><p>LAN sharing requires the generated runtime on this controller.</p></div></div></section>;
  if (!canManage) return <section className="lan-access-panel" aria-labelledby="lan-access-title"><div className="lan-heading"><div><h2 id="lan-access-title">LAN access</h2><p>An administrator can inspect and manage LAN sharing for this application.</p></div></div></section>;

  const accessBusy = access.isFetching || profile.isFetching;
  return <section className="lan-access-panel" aria-labelledby="lan-access-title">
    <div className="lan-heading"><div><h2 id="lan-access-title">LAN access</h2><p>Keep this application local until an administrator approves and attests a separate LAN route.</p></div><button className="button small" type="button" disabled={accessBusy || reserve.isPending || approve.isPending || grant.isPending} onClick={() => void refresh()}>Check LAN access</button></div>
    <span className="sr-only" role="status" aria-live="polite" aria-atomic="true">{reserve.isPending ? "Reserving a LAN port for review." : approve.isPending ? "Approving LAN access." : grant.isPending ? "Applying the LAN route." : access.isFetching || profile.isFetching ? "Checking the latest LAN access observation." : message}</span>
    {message && <p className="lan-message">{message}</p>}
    {access.isLoading ? <p role="status">Loading LAN access state…</p> : access.isError ? <div className="callout danger" role="alert"><strong>LAN access state could not be loaded.</strong><span>{access.error.message}</span><button className="button small" type="button" onClick={() => void access.refetch()}>Retry LAN access check</button></div> : <>
      {access.data?.availability === "local_only" ? <div className="callout info"><strong>Local only</strong><span>No approved LAN access exists for this application.</span></div> : accessRefreshWithheld ? <div className="callout info" role="status"><strong>Checking LAN route</strong><span>The previous LAN address is hidden until this controller returns a fresh attestation.</span></div> : lanURL ? <div className="callout info lan-address"><strong>LAN route verified</strong><span>HTTP is reachable from devices that can reach this controller address. Verified at {access.data?.observedAt}.</span><code>{lanURL}</code><div className="lan-actions"><a className="button small" href={lanURL} target="_blank" rel="noopener noreferrer">Open LAN address</a><button className="button small" type="button" onClick={() => { if (!navigator.clipboard) { setMessage("LAN address could not be copied."); return; } void navigator.clipboard.writeText(lanURL).then(() => setMessage("LAN address copied."), () => setMessage("LAN address could not be copied.")); }}>Copy address</button></div></div> : <div className="callout warning"><strong>{routeExpired ? "LAN route verification expired" : access.data?.availability === "verified" ? "LAN address withheld" : "LAN route is not verified"}</strong><span>{routeExpired ? "The LAN route observation is too old to open or copy an address. Check LAN access again." : access.data?.availability === "verified" ? "The controller did not return a current safe LAN address. Check LAN access again before opening or copying an address." : "An approved profile or port allocation is not proof that the gateway is serving this application. No LAN address is shown."}</span></div>}
      {currentRevision && <dl className="lan-details"><div><dt>Approved access revision</dt><dd>{currentRevision.revisionNumber}</dd></div><div><dt>Allocated port</dt><dd>{currentRevision.allocation.port}</dd></div><div><dt>Allocation state</dt><dd>{shortState(currentRevision.allocation.state)}</dd></div><div><dt>Approval digest</dt><dd className="mono">{currentRevision.specDigest}</dd></div></dl>}
      {pendingReservation && <aside className="lan-pending" aria-labelledby="lan-reservation-title"><h3 id="lan-reservation-title">Reserved port awaiting approval</h3><p>Port {pendingReservation.allocation.port} is reserved for this exact review. No address is available.</p><dl className="lan-details"><div><dt>Allocation identifier</dt><dd className="mono">{pendingReservation.allocation.id}</dd></div><div><dt>Operation identifier</dt><dd className="mono">{reserved?.operationId}</dd></div></dl><button ref={reservationReviewButton} className="button small" type="button" onClick={() => setReservationReview(reserved)}>Review reserved port</button></aside>}
      {claim && <aside className="lan-pending" aria-labelledby="lan-claim-title"><h3 ref={claimHeading} id="lan-claim-title" tabIndex={-1}>Activation claim</h3><p>Claim <span className="mono">{claim.attemptId}</span>{claim.state ? ` is ${shortState(claim.state)}.` : "."}</p>{grantState.isError ? <div className="callout danger" role="alert"><strong>Claim observation could not be loaded.</strong><span>{grantState.error.message}</span><button className="button small" type="button" onClick={() => void grantState.refetch()}>Retry claim check</button></div> : grantState.data && <dl className="lan-details"><div><dt>Durable state</dt><dd>{shortState(grantState.data.claim.state)}</dd></div><div><dt>Gateway observation</dt><dd>{shortState(grantState.data.observed.availability)}</dd></div>{grantState.data.observed.observedAt && <div><dt>Observed at</dt><dd>{grantState.data.observed.observedAt}</dd></div>}</dl>}</aside>}
      {unresolvedReservation ? <aside className="lan-pending" aria-labelledby="lan-reservation-recovery-title"><h3 id="lan-reservation-recovery-title">Reservation result needs reconciliation</h3><p>Rig did not receive a verified result for this reservation. It will not create another reservation while the original operation is unresolved.</p><dl className="lan-details"><div><dt>Original operation</dt><dd className="mono">{unresolvedReservation.operationId}</dd></div><div><dt>Expected access revision</dt><dd>{unresolvedReservation.expectedRevisionNumber}</dd></div><div><dt>Gateway profile revision</dt><dd>{unresolvedReservation.profileRevisionNumber}</dd></div></dl><div className="lan-actions"><button className="button small" type="button" disabled={reserve.isPending} onClick={() => reserve.mutate(unresolvedReservation)}>Replay exact reservation</button><button className="button small" type="button" disabled={accessBusy || reserve.isPending} onClick={() => void refresh()}>Check current access state</button></div></aside> : !currentRevision && !pendingReservation && <>{profile.isLoading ? <p role="status">Checking the approved gateway profile…</p> : profile.isError ? <div className="callout danger" role="alert"><strong>Gateway profile could not be checked.</strong><span>{profile.error.message}</span><button className="button small" type="button" onClick={() => void profile.refetch()}>Retry profile check</button></div> : !currentProfile ? <div className="callout warning"><strong>LAN gateway profile required</strong><span>Approve a desired LAN gateway profile from Machines before reserving a port for this application.</span></div> : <button className="button" type="button" disabled={!canReserve} onClick={() => { setMessage(""); reserve.mutate({ operationId: crypto.randomUUID(), expectedRevisionNumber: access.data!.expectedRevisionNumber, profileRevisionId: currentProfile.id, profileRevisionNumber: currentProfile.revisionNumber }); }}>{reserve.isPending ? "Reserving port…" : "Reserve LAN port for review"}</button>}</>}
      {currentRevision && !lanURL && approved?.id === currentRevision.id && !claim && <button ref={activationReviewButton} className="button" type="button" disabled={grant.isPending || access.isFetching} onClick={() => setGrantReview(currentRevision)}>Review LAN activation</button>}
      {currentRevision && !lanURL && approved?.id !== currentRevision.id && !claim && <div className="callout warning"><strong>Activation claim identity is unavailable.</strong><span>The controller read model does not return the current grant attempt after refresh. Rig will not create a new grant without that exact identity. Use the controller’s recovery flow, then check LAN access again.</span></div>}
      {currentRevision && <div className="callout warning"><strong>Disable LAN sharing is unavailable.</strong><span>This controller API has no disable approval digest or removal claim endpoint, so Rig cannot safely release this allocation from the dashboard.</span></div>}
      {reserve.isError && !unresolvedReservation && <div className="callout danger" role="alert"><strong>Port reservation was not completed.</strong><span>{reserve.error.message}</span><button className="button small" type="button" onClick={() => void refresh()}>Refresh access summary</button></div>}
    </>}
    {reservationReview && <Dialog title="Review LAN application access" description="Confirm the exact reserved allocation and approval digest. This does not create a usable address." pending={approve.isPending} close={closeReservationReview}>
      <dl className="lan-review-details"><div><dt>Application</dt><dd className="mono">{appId}</dd></div><div><dt>Access revision</dt><dd>New revision {reservationReview.expectedRevisionNumber + 1}</dd></div><div><dt>Reserved port</dt><dd>{reservationReview.reservation.allocation.port}</dd></div><div><dt>Allocation identifier</dt><dd className="mono">{reservationReview.reservation.allocation.id}</dd></div><div><dt>Approval digest</dt><dd className="mono">{reservationReview.reservation.approvalDigest}</dd></div></dl>
      {approve.isError ? <div className="callout danger" role="alert"><strong>LAN access approval was not completed.</strong><span>{approve.error.message}</span><button className="button small" type="button" onClick={() => { closeReservationReview(); void refresh(); }}>Refresh access summary</button></div> : <div className="deployment-dialog-actions"><button className="button" type="button" disabled={approve.isPending} onClick={closeReservationReview}>Cancel</button><button className="button primary" type="button" disabled={approve.isPending} onClick={() => approve.mutate(reservationReview)}>{approve.isPending ? "Approving…" : "Approve LAN access"}</button></div>}
    </Dialog>}
      {grantReview && <Dialog title="Review LAN activation" description="Confirm the exact approved access revision. Rig will then request an attested gateway route and still withhold the address until verified." pending={grant.isPending} close={() => setGrantReview(null)}>
      <dl className="lan-review-details"><div><dt>Access revision</dt><dd>{grantReview.revisionNumber}</dd></div><div><dt>Access identifier</dt><dd className="mono">{grantReview.id}</dd></div><div><dt>Allocated port</dt><dd>{grantReview.allocation.port}</dd></div><div><dt>Approval digest</dt><dd className="mono">{grantReview.specDigest}</dd></div></dl>
      {grant.isError ? <div className="callout danger" role="alert"><strong>LAN activation was not completed.</strong><span>{grant.error.message}</span><button className="button small" type="button" onClick={() => { setGrantReview(null); void refresh(); }}>Refresh access summary</button></div> : <div className="deployment-dialog-actions"><button className="button" type="button" disabled={grant.isPending} onClick={() => setGrantReview(null)}>Cancel</button><button className="button primary" type="button" disabled={grant.isPending} onClick={() => grant.mutate({ revision: grantReview, attemptId: crypto.randomUUID() })}>{grant.isPending ? "Activating…" : "Grant and attest LAN route"}</button></div>}
    </Dialog>}
  </section>;
}
