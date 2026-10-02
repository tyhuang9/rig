import { useEffect, useRef, useState } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import {
  APIError,
  api,
  isGatewayReconciliationRequired,
  lanAccessObservationFresh,
  subscribeGatewayReconciliationRequired,
  validNormalGeneratedRuntimeStatus,
  verifiedLANAccessURL,
  type DisableLANAppAccessRequest,
  type LANAppAccessRead,
  type LANAppAccessReservationMutation,
  type LANAppAccessRevision,
  type LANAppDisableClaim,
  type LANAppDisableReview,
  type LANAppGrantClaim,
} from "./api";
import { Dialog } from "./dialog";
import { useLocalRouteExpiry } from "./use-local-route-expiry";

function administrator(role: string) {
  return role.trim().toLowerCase() === "administrator";
}

function shortState(value: string) {
  return value.replaceAll("_", " ");
}

function validIdentifier(value: unknown): value is string {
  return typeof value === "string" && value.length > 0;
}

function validDigest(value: unknown): value is string {
  return typeof value === "string" && /^[a-f0-9]{64}$/.test(value);
}

function validRevision(value: unknown, minimum = 1): value is number {
  return typeof value === "number" && Number.isSafeInteger(value) && value >= minimum;
}

type ReservationRequest = {
  operationId: string;
  expectedRevisionNumber: number;
  profileRevisionId: string;
  profileRevisionNumber: number;
};

type ReservationReview = {
  operationId: string;
  expectedRevisionNumber: number;
  reservation: Pick<LANAppAccessReservationMutation, "allocation" | "approvalDigest">;
};

type GrantRequest = {
  attemptId: string;
  accessRevisionId: string;
  accessRevisionNumber: number;
  approvalDigest: string;
};

type StoredAttempt<T> = {
  version: 1;
  kind: "reservation" | "grant" | "disable";
  appId: string;
  userId: string;
  request: T;
};

function storageKey(appId: string, kind: StoredAttempt<unknown>["kind"]) {
  return `rig-lan-access-${kind}:${appId}`;
}

function validReservationRequest(value: unknown): value is ReservationRequest {
  if (!value || typeof value !== "object") return false;
  const request = value as Record<string, unknown>;
  return validIdentifier(request.operationId) && validRevision(request.expectedRevisionNumber, 0) &&
    validIdentifier(request.profileRevisionId) && validRevision(request.profileRevisionNumber);
}

function validGrantRequest(value: unknown): value is GrantRequest {
  if (!value || typeof value !== "object") return false;
  const request = value as Record<string, unknown>;
  return validIdentifier(request.attemptId) && validIdentifier(request.accessRevisionId) &&
    validRevision(request.accessRevisionNumber) && validDigest(request.approvalDigest);
}

function validDisableRequest(value: unknown): value is DisableLANAppAccessRequest {
  if (!value || typeof value !== "object") return false;
  const request = value as Record<string, unknown>;
  return validIdentifier(request.operationId) && validIdentifier(request.accessRevisionId) &&
    validRevision(request.accessRevisionNumber) && validIdentifier(request.allocationId) &&
    validDigest(request.approvalDigest);
}

function readAttempt<T>(appId: string, userId: string, kind: StoredAttempt<T>["kind"], valid: (value: unknown) => value is T): T | null {
  try {
    const value = JSON.parse(window.sessionStorage.getItem(storageKey(appId, kind)) || "null") as StoredAttempt<unknown> | null;
    if (!value || value.version !== 1 || value.kind !== kind || value.appId !== appId || value.userId !== userId || !valid(value.request)) {
      if (value) window.sessionStorage.removeItem(storageKey(appId, kind));
      return null;
    }
    return value.request;
  } catch {
    return null;
  }
}

function saveAttempt<T>(appId: string, userId: string, kind: StoredAttempt<T>["kind"], request: T) {
  window.sessionStorage.setItem(storageKey(appId, kind), JSON.stringify({ version: 1, kind, appId, userId, request } satisfies StoredAttempt<T>));
}

function clearAttempt(appId: string, kind: StoredAttempt<unknown>["kind"]) {
  try {
    window.sessionStorage.removeItem(storageKey(appId, kind));
  } catch {
    // A storage cleanup failure cannot make a completed operation unsafe.
  }
}

function sameRevision(left: LANAppAccessRevision, right: LANAppAccessRevision) {
  return left.id === right.id && left.revisionNumber === right.revisionNumber &&
    left.operationId === right.operationId && left.specDigest === right.specDigest &&
    left.allocation.id === right.allocation.id && left.allocation.ownerOperationId === right.allocation.ownerOperationId &&
    left.allocation.port === right.allocation.port &&
    left.allocation.gatewayProfileRevisionId === right.allocation.gatewayProfileRevisionId &&
    left.allocation.gatewayProfileRevisionNumber === right.allocation.gatewayProfileRevisionNumber;
}

function sameLANActionRevision(appId: string, left: LANAppAccessRevision | undefined, right: LANAppAccessRevision | undefined) {
  return Boolean(left && right && left.appId === appId && right.appId === appId &&
    left.allocation.appId === appId && right.allocation.appId === appId &&
    left.id === right.id && left.revisionNumber === right.revisionNumber &&
    left.operationId === right.operationId && left.specDigest === right.specDigest &&
    left.approvedBy === right.approvedBy && left.approvedAt === right.approvedAt &&
    left.allocation.id === right.allocation.id && left.allocation.ownerOperationId === right.allocation.ownerOperationId &&
    left.allocation.port === right.allocation.port && left.allocation.state === right.allocation.state &&
    left.allocation.gatewayProfileRevisionId === right.allocation.gatewayProfileRevisionId &&
    left.allocation.gatewayProfileRevisionNumber === right.allocation.gatewayProfileRevisionNumber);
}

function exactLANActionURL(appId: string, displayed: LANAppAccessRead | undefined, fresh: LANAppAccessRead | undefined) {
  const displayedURL = verifiedLANAccessURL(displayed);
  const freshURL = verifiedLANAccessURL(fresh);
  return displayed && fresh && displayedURL && freshURL && displayed.url === fresh.url && displayedURL === freshURL &&
    displayed.expectedRevisionNumber === fresh.expectedRevisionNumber &&
    sameLANActionRevision(appId, displayed.desiredAccess, fresh.desiredAccess) ? freshURL : null;
}

type LANAddressAction = "open" | "copy";
export const LAN_ADDRESS_ACTION_DEADLINE_MS = 10_000;
type PendingLANWindow = {
  popup: Window;
  document: Document;
  origin: string;
};
type LANAddressActionState =
  | { phase: "idle" }
  | { phase: "checking"; action: LANAddressAction }
  | { phase: "success"; action: LANAddressAction; message: string }
  | { phase: "failure"; action: LANAddressAction; message: string };

function lanAddressActionFailure(error: unknown) {
  if (isGatewayReconciliationRequired(error)) {
    return "Rig entered LAN recovery before it could prove this address. Complete recovery before trying again.";
  }
  if (error instanceof Error && error.message === "lan_address_changed") {
    return "The LAN address or its exact access revision changed during the check. Check LAN access before trying again.";
  }
  if (error instanceof Error && error.message === "popup_closed") {
    return "The new window closed before Rig finished checking the LAN address. Try again when you are ready.";
  }
  if (error instanceof Error && error.message === "popup_changed") {
    return "The new window changed before Rig finished checking the LAN address. Rig closed it without opening the LAN address.";
  }
  if (error instanceof Error && error.message === "clipboard_failed") {
    return "The freshly proved LAN address could not be copied. Check clipboard permission before trying again.";
  }
  if (error instanceof Error && error.message === "lan_action_authority_changed") {
    return "The LAN address action stopped because operator access changed. Check LAN access after signing in with administrator access.";
  }
  if (error instanceof Error && error.message === "lan_action_timed_out") {
    return "Rig could not prove the LAN address within 10 seconds. Check LAN access before trying again.";
  }
  return "Rig could not prove that this LAN address is safe to use. Check LAN access before trying again.";
}

function closeLANWindow(popup: Window | null) {
  if (!popup || popup.closed) return;
  try {
    popup.close();
  } catch {
    // The browser may already have torn down an isolated popup.
  }
}

function preparePendingLANWindow(popup: Window): PendingLANWindow | null {
  try {
    if (popup.closed || popup.location.href !== "about:blank") return null;
    const popupDocument = popup.document;
    popupDocument.title = "Checking LAN address — Rig";
    popupDocument.documentElement.lang = "en";
    const main = popupDocument.createElement("main");
    const heading = popupDocument.createElement("h1");
    const status = popupDocument.createElement("p");
    heading.textContent = "Checking LAN address";
    status.textContent = "Rig is proving the current LAN address before opening it.";
    status.setAttribute("role", "status");
    status.setAttribute("aria-live", "polite");
    status.setAttribute("aria-atomic", "true");
    main.append(heading, status);
    popupDocument.body.replaceChildren(main);
    return { popup, document: popupDocument, origin: popup.location.origin };
  } catch {
    return null;
  }
}

function pendingLANWindowUnchanged(pending: PendingLANWindow): boolean {
  try {
    return !pending.popup.closed && pending.popup.document === pending.document &&
      pending.popup.location.href === "about:blank" && pending.popup.location.origin === pending.origin;
  } catch {
    return false;
  }
}

function reservationFromSnapshot(appId: string, value: LANAppAccessRead["pendingReservation"]): ReservationReview | null {
  if (!value || value.allocation.appId !== appId || !validIdentifier(value.allocation.ownerOperationId) ||
      !validRevision(value.expectedRevisionNumber, 0) || !validDigest(value.approvalDigest) ||
      !validIdentifier(value.allocation.id) || !validIdentifier(value.allocation.gatewayProfileRevisionId) ||
      !validRevision(value.allocation.gatewayProfileRevisionNumber)) return null;
  return {
    operationId: value.allocation.ownerOperationId,
    expectedRevisionNumber: value.expectedRevisionNumber,
    reservation: { allocation: value.allocation, approvalDigest: value.approvalDigest },
  };
}

function grantFromSnapshot(appId: string, revision: LANAppAccessRevision | undefined, claim: LANAppAccessRead["grantClaim"]): LANAppGrantClaim | null {
  if (!revision || !claim || claim.appId !== appId || claim.accessRevisionId !== revision.id ||
      claim.accessRevisionNumber !== revision.revisionNumber || claim.allocationId !== revision.allocation.id ||
      claim.ownerOperationId !== revision.operationId || claim.port !== revision.allocation.port ||
      !validIdentifier(claim.attemptId) || !validIdentifier(claim.state)) return null;
  return claim;
}

function disableClaimFromSnapshot(appId: string, claim: LANAppAccessRead["disableClaim"]): LANAppDisableClaim | null {
  if (!claim || claim.appId !== appId || !validIdentifier(claim.operationId) || !validIdentifier(claim.accessRevisionId) ||
      !validRevision(claim.accessRevisionNumber) || !validIdentifier(claim.allocationId) ||
      !validDigest(claim.approvalDigest) || !validIdentifier(claim.state)) return null;
  return claim;
}

function disableReviewFromSnapshot(appId: string, revision: LANAppAccessRevision | undefined, review: LANAppAccessRead["disableReview"]): LANAppDisableReview | null {
  if (!revision || !review || review.appId !== appId || review.accessRevisionId !== revision.id ||
      review.accessRevisionNumber !== revision.revisionNumber || review.allocationId !== revision.allocation.id ||
      review.ownerOperationId !== revision.operationId || review.port !== revision.allocation.port ||
      review.gatewayProfileRevisionId !== revision.allocation.gatewayProfileRevisionId ||
      review.gatewayProfileRevisionNumber !== revision.allocation.gatewayProfileRevisionNumber ||
      !validDigest(review.approvalDigest)) return null;
  return review;
}

function reservationMatchesRequest(review: ReservationReview | null, request: ReservationRequest) {
  return Boolean(review && review.operationId === request.operationId &&
    review.expectedRevisionNumber === request.expectedRevisionNumber &&
    review.reservation.allocation.gatewayProfileRevisionId === request.profileRevisionId &&
    review.reservation.allocation.gatewayProfileRevisionNumber === request.profileRevisionNumber);
}

function grantMatchesRequest(claim: LANAppGrantClaim | null, revision: LANAppAccessRevision | undefined, request: GrantRequest) {
  return Boolean(claim && revision && revision.specDigest === request.approvalDigest && claim.attemptId === request.attemptId &&
    claim.accessRevisionId === request.accessRevisionId && claim.accessRevisionNumber === request.accessRevisionNumber &&
    validDigest(request.approvalDigest));
}

function disableMatchesRequest(claim: LANAppDisableClaim | null, request: DisableLANAppAccessRequest) {
  return Boolean(claim && claim.operationId === request.operationId &&
    claim.accessRevisionId === request.accessRevisionId && claim.accessRevisionNumber === request.accessRevisionNumber &&
    claim.allocationId === request.allocationId && claim.approvalDigest === request.approvalDigest);
}

function sameDisableReview(left: LANAppDisableReview, right: LANAppDisableReview) {
  return left.appId === right.appId && left.accessRevisionId === right.accessRevisionId &&
    left.accessRevisionNumber === right.accessRevisionNumber && left.allocationId === right.allocationId &&
    left.ownerOperationId === right.ownerOperationId && left.port === right.port &&
    left.gatewayProfileRevisionId === right.gatewayProfileRevisionId &&
    left.gatewayProfileRevisionNumber === right.gatewayProfileRevisionNumber &&
    left.approvalDigest === right.approvalDigest;
}

function grantUnresolved(claim: LANAppGrantClaim | null) {
  return Boolean(claim && claim.state !== "committed" && claim.state !== "rolled_back");
}

function staleDisableError(error: unknown) {
  return error instanceof APIError && [409, 422, 503].includes(error.status);
}

export function LANApplicationAccessPanel({ appId, role, userId, enabled }: { appId: string; role: string; userId: string; enabled: boolean }) {
  const canManage = administrator(role) && validIdentifier(userId);
  const lanAddressAuthorityKey = enabled && canManage ? `${appId}\u0000${role.trim().toLowerCase()}\u0000${userId}` : null;
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
  const [reservationReview, setReservationReview] = useState<ReservationReview | null>(null);
  const [grantReview, setGrantReview] = useState<LANAppAccessRevision | null>(null);
  const [disableReview, setDisableReview] = useState<LANAppDisableReview | null>(null);
  const [unresolvedReservation, setUnresolvedReservation] = useState<ReservationRequest | null>(() => readAttempt(appId, userId, "reservation", validReservationRequest));
  const [unresolvedGrantRequest, setUnresolvedGrantRequest] = useState<GrantRequest | null>(() => readAttempt(appId, userId, "grant", validGrantRequest));
  const [unresolvedDisableRequest, setUnresolvedDisableRequest] = useState<DisableLANAppAccessRequest | null>(() => readAttempt(appId, userId, "disable", validDisableRequest));
  const [message, setMessage] = useState("");
  const [focusAfterReservationReview, setFocusAfterReservationReview] = useState(false);
  const [focusAfterReservationRecovery, setFocusAfterReservationRecovery] = useState(false);
  const [focusAfterApproval, setFocusAfterApproval] = useState(false);
  const [focusAfterGrant, setFocusAfterGrant] = useState(false);
  const [focusAfterDisable, setFocusAfterDisable] = useState(false);
  const [lanAddressAction, setLANAddressAction] = useState<LANAddressActionState>({ phase: "idle" });
  const reservationReviewButton = useRef<HTMLButtonElement>(null);
  const activationReviewButton = useRef<HTMLButtonElement>(null);
  const disableReviewButton = useRef<HTMLButtonElement>(null);
  const grantHeading = useRef<HTMLHeadingElement>(null);
  const disableHeading = useRef<HTMLHeadingElement>(null);
  const recoveryHeading = useRef<HTMLHeadingElement>(null);
  const reservationRecoveryHeading = useRef<HTMLHeadingElement>(null);
  const grantRecoveryHeading = useRef<HTMLHeadingElement>(null);
  const approvalErrorTarget = useRef<HTMLDivElement>(null);
  const lanAddressActionStatus = useRef<HTMLDivElement>(null);
  const openLANAddressButton = useRef<HTMLButtonElement>(null);
  const copyLANAddressButton = useRef<HTMLButtonElement>(null);
  const mounted = useRef(true);
  const lanAddressActionInFlight = useRef(false);
  const lanAddressActionSequence = useRef(0);
  const lanAddressActionDeadline = useRef<number | null>(null);
  const currentLANAddressAction = useRef<LANAddressAction>("open");
  const pendingLANWindow = useRef<Window | null>(null);
  const actionAppId = useRef(appId);
  const currentLANAddressAuthority = useRef<string | null>(lanAddressAuthorityKey);
  const observedLANAddressAuthority = useRef<string | null>(lanAddressAuthorityKey);
  currentLANAddressAuthority.current = lanAddressAuthorityKey;

  const currentRevision = access.data?.desiredAccess;
  const pendingReservation = reservationFromSnapshot(appId, access.data?.pendingReservation);
  const grantClaim = grantFromSnapshot(appId, currentRevision, access.data?.grantClaim);
  const disableClaim = disableClaimFromSnapshot(appId, access.data?.disableClaim);
  const currentDisableReview = disableReviewFromSnapshot(appId, currentRevision, access.data?.disableReview);
  const accessReady = access.isSuccess && !access.isFetching && Boolean(access.data);
  const accessRefreshWithheld = access.isFetching && access.data?.availability === "verified";
  const routeExpired = !access.isFetching && access.isSuccess && access.data?.availability === "verified" && !lanAccessObservationFresh(access.data);
  const latestAccess = useRef<LANAppAccessRead | undefined>(access.data);
  latestAccess.current = access.data;
  useLocalRouteExpiry(access.data?.observedAt);

  const clearLANAddressActionDeadline = (deadline = lanAddressActionDeadline.current) => {
    if (deadline === null || lanAddressActionDeadline.current !== deadline) return;
    window.clearTimeout(deadline);
    lanAddressActionDeadline.current = null;
  };

  const startLANAddressActionDeadline = (sequence: number, action: LANAddressAction, popup: Window | null) => {
    clearLANAddressActionDeadline();
    const deadline = window.setTimeout(() => {
      if (!mounted.current || sequence !== lanAddressActionSequence.current) return;
      lanAddressActionDeadline.current = null;
      lanAddressActionSequence.current += 1;
      lanAddressActionInFlight.current = false;
      closeLANWindow(popup);
      if (pendingLANWindow.current === popup) pendingLANWindow.current = null;
      setLANAddressAction({ phase: "failure", action, message: lanAddressActionFailure(new Error("lan_action_timed_out")) });
    }, LAN_ADDRESS_ACTION_DEADLINE_MS);
    lanAddressActionDeadline.current = deadline;
    return deadline;
  };

  const grantState = useQuery({
    queryKey: ["lan-app-grant", appId, grantClaim?.attemptId],
    queryFn: () => api.lanGrant(appId, grantClaim!.attemptId),
    enabled: Boolean(grantClaim?.attemptId) && enabled && canManage,
    retry: false,
    refetchOnWindowFocus: false,
  });
  const disableState = useQuery({
    queryKey: ["lan-app-disable", appId, disableClaim?.operationId],
    queryFn: () => api.lanDisable(appId, disableClaim!.operationId),
    enabled: Boolean(disableClaim?.operationId) && enabled && canManage,
    retry: false,
    refetchOnWindowFocus: false,
  });
  const refreshAccess = async () => {
    const result = await access.refetch();
    return result.isError ? undefined : result.data;
  };

  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
      lanAddressActionSequence.current += 1;
      lanAddressActionInFlight.current = false;
      clearLANAddressActionDeadline();
      closeLANWindow(pendingLANWindow.current);
      pendingLANWindow.current = null;
    };
  }, []);

  useEffect(() => subscribeGatewayReconciliationRequired(() => {
    lanAddressActionSequence.current += 1;
    lanAddressActionInFlight.current = false;
    clearLANAddressActionDeadline();
    closeLANWindow(pendingLANWindow.current);
    pendingLANWindow.current = null;
    if (mounted.current) {
      setLANAddressAction({
        phase: "failure",
        action: currentLANAddressAction.current,
        message: "Rig entered LAN recovery before it could prove this address. Complete recovery before trying again.",
      });
    }
  }), []);

  useEffect(() => {
    if (actionAppId.current === appId) return;
    actionAppId.current = appId;
    lanAddressActionSequence.current += 1;
    lanAddressActionInFlight.current = false;
    clearLANAddressActionDeadline();
    closeLANWindow(pendingLANWindow.current);
    pendingLANWindow.current = null;
    setLANAddressAction({ phase: "idle" });
  }, [appId]);

  useEffect(() => {
    if (observedLANAddressAuthority.current === lanAddressAuthorityKey) return;
    observedLANAddressAuthority.current = lanAddressAuthorityKey;
    lanAddressActionSequence.current += 1;
    lanAddressActionInFlight.current = false;
    clearLANAddressActionDeadline();
    closeLANWindow(pendingLANWindow.current);
    pendingLANWindow.current = null;
    setLANAddressAction(lanAddressAuthorityKey
      ? { phase: "failure", action: currentLANAddressAction.current, message: lanAddressActionFailure(new Error("lan_action_authority_changed")) }
      : { phase: "idle" });
  }, [lanAddressAuthorityKey]);

  const proveLANAddress = async (displayed: LANAppAccessRead | undefined, sequence: number, authorityKey: string) => {
    const before = await api.status();
    if (!mounted.current || sequence !== lanAddressActionSequence.current) throw new Error("action_cancelled");
    if (currentLANAddressAuthority.current !== authorityKey) throw new Error("lan_action_authority_changed");
    if (!validNormalGeneratedRuntimeStatus(before)) throw new Error("invalid_system_status");
    const fresh = await api.lanAccess(appId);
    if (!mounted.current || sequence !== lanAddressActionSequence.current) throw new Error("action_cancelled");
    if (currentLANAddressAuthority.current !== authorityKey) throw new Error("lan_action_authority_changed");
    const after = await api.status();
    if (!mounted.current || sequence !== lanAddressActionSequence.current) throw new Error("action_cancelled");
    if (currentLANAddressAuthority.current !== authorityKey) throw new Error("lan_action_authority_changed");
    if (!validNormalGeneratedRuntimeStatus(after)) throw new Error("invalid_system_status");
    const freshURL = exactLANActionURL(appId, displayed, fresh);
    if (!freshURL || exactLANActionURL(appId, displayed, latestAccess.current) !== freshURL) {
      throw new Error("lan_address_changed");
    }
    return freshURL;
  };

  const finishLANAddressAction = async (action: LANAddressAction, displayed: LANAppAccessRead | undefined, sequence: number, authorityKey: string, pendingWindow: PendingLANWindow | null, deadline: number) => {
    const popup = pendingWindow?.popup ?? null;
    try {
      const freshURL = await proveLANAddress(displayed, sequence, authorityKey);
      if (!mounted.current || sequence !== lanAddressActionSequence.current || currentLANAddressAuthority.current !== authorityKey ||
          exactLANActionURL(appId, displayed, latestAccess.current) !== freshURL) {
        closeLANWindow(popup);
        return;
      }
      if (action === "open") {
        if (!pendingWindow) throw new Error("popup_closed");
        if (!pendingLANWindowUnchanged(pendingWindow)) throw new Error(pendingWindow.popup.closed ? "popup_closed" : "popup_changed");
        pendingWindow.popup.location.replace(freshURL);
        pendingLANWindow.current = null;
      } else {
        if (currentLANAddressAuthority.current !== authorityKey) throw new Error("lan_action_authority_changed");
        if (!navigator.clipboard) throw new Error("clipboard_failed");
        try {
          await navigator.clipboard.writeText(freshURL);
        } catch {
          throw new Error("clipboard_failed");
        }
        if (!mounted.current || sequence !== lanAddressActionSequence.current || currentLANAddressAuthority.current !== authorityKey) return;
      }
      if (mounted.current && sequence === lanAddressActionSequence.current) {
        setLANAddressAction({
          phase: "success",
          action,
          message: action === "open" ? "The freshly proved LAN address opened in a new window." : "The freshly proved LAN address was copied.",
        });
      }
    } catch (error) {
      closeLANWindow(popup);
      if (pendingLANWindow.current === popup) pendingLANWindow.current = null;
      if (mounted.current && sequence === lanAddressActionSequence.current) {
        setLANAddressAction({ phase: "failure", action, message: lanAddressActionFailure(error) });
      }
    } finally {
      clearLANAddressActionDeadline(deadline);
      if (sequence === lanAddressActionSequence.current) lanAddressActionInFlight.current = false;
    }
  };

  const openLANAddress = (displayed: LANAppAccessRead | undefined) => {
    const authorityKey = currentLANAddressAuthority.current;
    if (lanAddressActionInFlight.current || !authorityKey) return;
    lanAddressActionInFlight.current = true;
    currentLANAddressAction.current = "open";
    const sequence = ++lanAddressActionSequence.current;
    let popup: Window | null = null;
    let pendingWindow: PendingLANWindow | null = null;
    try {
      popup = window.open("about:blank", "_blank");
      if (!popup) throw new Error("popup_blocked");
      popup.opener = null;
      pendingWindow = preparePendingLANWindow(popup);
      if (!pendingWindow) throw new Error("popup_changed");
    } catch {
      closeLANWindow(popup);
      lanAddressActionInFlight.current = false;
      setLANAddressAction({ phase: "failure", action: "open", message: "The browser blocked an isolated new window. Allow pop-ups for Rig before trying again." });
      return;
    }
    pendingLANWindow.current = popup;
    setLANAddressAction({ phase: "checking", action: "open" });
    const deadline = startLANAddressActionDeadline(sequence, "open", popup);
    void finishLANAddressAction("open", displayed, sequence, authorityKey, pendingWindow, deadline);
  };

  const copyLANAddress = (displayed: LANAppAccessRead | undefined) => {
    const authorityKey = currentLANAddressAuthority.current;
    if (lanAddressActionInFlight.current || !authorityKey) return;
    lanAddressActionInFlight.current = true;
    currentLANAddressAction.current = "copy";
    const sequence = ++lanAddressActionSequence.current;
    setLANAddressAction({ phase: "checking", action: "copy" });
    const deadline = startLANAddressActionDeadline(sequence, "copy", null);
    void finishLANAddressAction("copy", displayed, sequence, authorityKey, null, deadline);
  };

  const reserve = useMutation({
    mutationFn: async (request: ReservationRequest) => {
      saveAttempt(appId, userId, "reservation", request);
      return api.reserveLANAccess(appId, {
        operationId: request.operationId,
        expectedRevisionNumber: request.expectedRevisionNumber,
        gatewayProfileRevisionId: request.profileRevisionId,
        gatewayProfileRevisionNumber: request.profileRevisionNumber,
      });
    },
    onSuccess: async (_result, request) => {
      const freshReservation = reservationFromSnapshot(appId, (await refreshAccess())?.pendingReservation);
      if (reservationMatchesRequest(freshReservation, request)) {
        clearAttempt(appId, "reservation");
        setUnresolvedReservation(null);
        setReservationReview(freshReservation);
        setMessage("The reserved port is ready for exact approval review.");
      } else {
        setUnresolvedReservation(request);
        setFocusAfterReservationRecovery(true);
        setMessage("Rig could not confirm the reservation from a fresh access snapshot. It will only replay this exact request.");
      }
    },
    onError: (error, request) => {
      setUnresolvedReservation(request);
      setFocusAfterReservationRecovery(true);
      setMessage(`The LAN reservation result for operation ${request.operationId} is uncertain. ${error instanceof Error ? error.message : "Check the current access state before replaying it."}`);
    },
  });

  const canApprove = Boolean(accessReady && reservationReview && pendingReservation &&
    reservationReview.operationId === pendingReservation.operationId &&
    reservationReview.expectedRevisionNumber === pendingReservation.expectedRevisionNumber &&
    reservationReview.reservation.allocation.id === pendingReservation.reservation.allocation.id &&
    reservationReview.reservation.approvalDigest === pendingReservation.reservation.approvalDigest);
  const approve = useMutation({
    mutationFn: async (review: ReservationReview) => {
      if (!canApprove) throw new Error("The reservation review is stale. Refresh the LAN access state before approving it.");
      return api.approveLANAccess(appId, {
        operationId: review.operationId,
        allocationId: review.reservation.allocation.id,
        expectedRevisionNumber: review.expectedRevisionNumber,
        approvalDigest: review.reservation.approvalDigest,
      });
    },
    onSuccess: async () => {
      setReservationReview(null);
      setFocusAfterApproval(true);
      const fresh = await refreshAccess();
      setMessage(fresh?.desiredAccess
        ? `LAN access revision ${fresh.desiredAccess.revisionNumber} is ready for activation review.`
        : "LAN approval was accepted, but a fresh access snapshot did not confirm its head. Check LAN access before continuing.");
    },
  });

  useEffect(() => {
    if (approve.isError && reservationReview) approvalErrorTarget.current?.focus();
  }, [approve.isError, reservationReview]);

  const canGrant = Boolean(accessReady && currentRevision && grantReview && sameRevision(currentRevision, grantReview) &&
    !grantClaim && !unresolvedGrantRequest && !disableClaim && !unresolvedDisableRequest);
  const grant = useMutation({
    mutationFn: async (request: GrantRequest) => {
      const replaying = unresolvedGrantRequest?.attemptId === request.attemptId &&
        unresolvedGrantRequest.accessRevisionId === request.accessRevisionId &&
        unresolvedGrantRequest.accessRevisionNumber === request.accessRevisionNumber &&
        unresolvedGrantRequest.approvalDigest === request.approvalDigest;
      if (!canGrant && !(accessReady && replaying)) throw new Error("The activation review is stale. Refresh the LAN access state before activating it.");
      saveAttempt(appId, userId, "grant", request);
      return api.grantLANAccess(appId, request);
    },
    onMutate: () => {
      setGrantReview(null);
      setFocusAfterGrant(true);
    },
    onSuccess: async (_result, request) => {
      const fresh = await refreshAccess();
      const freshClaim = grantFromSnapshot(appId, fresh?.desiredAccess, fresh?.grantClaim);
      if (grantMatchesRequest(freshClaim, fresh?.desiredAccess, request)) {
        clearAttempt(appId, "grant");
        setUnresolvedGrantRequest(null);
        setMessage(`LAN activation claim ${request.attemptId} is being observed.`);
      } else {
        setUnresolvedGrantRequest(request);
        setMessage("Rig could not confirm the activation claim from a fresh access snapshot. It will only replay this exact request.");
      }
    },
    onError: (error, request) => {
      setUnresolvedGrantRequest(request);
      setMessage(`The LAN activation result for claim ${request.attemptId} is uncertain. ${error instanceof Error ? error.message : "Replay only this exact request."}`);
    },
  });

  const canDisable = Boolean(accessReady && currentRevision && disableReview && currentDisableReview &&
    sameDisableReview(disableReview, currentDisableReview) && !disableClaim && !unresolvedDisableRequest && !unresolvedGrantRequest && !grantUnresolved(grantClaim));
  const disable = useMutation({
    mutationFn: async (request: DisableLANAppAccessRequest) => {
      const replaying = unresolvedDisableRequest?.operationId === request.operationId &&
        unresolvedDisableRequest.accessRevisionId === request.accessRevisionId &&
        unresolvedDisableRequest.accessRevisionNumber === request.accessRevisionNumber &&
        unresolvedDisableRequest.allocationId === request.allocationId &&
        unresolvedDisableRequest.approvalDigest === request.approvalDigest;
      if (!canDisable && !(accessReady && replaying)) throw new Error("The LAN sharing removal review is stale. Refresh the LAN access state before removing it.");
      saveAttempt(appId, userId, "disable", request);
      return api.disableLANAccess(appId, request);
    },
    onMutate: () => {
      setDisableReview(null);
      setFocusAfterDisable(true);
    },
    onSuccess: async (_result, request) => {
      const freshClaim = disableClaimFromSnapshot(appId, (await refreshAccess())?.disableClaim);
      if (disableMatchesRequest(freshClaim, request)) {
        clearAttempt(appId, "disable");
        setUnresolvedDisableRequest(null);
        setMessage(`LAN sharing removal claim ${request.operationId} is being observed.`);
      } else {
        setUnresolvedDisableRequest(request);
        setMessage("Rig could not confirm the LAN sharing removal from a fresh access snapshot. It will only replay this exact request.");
      }
    },
    onError: (error, request) => {
      setUnresolvedDisableRequest(request);
      if (staleDisableError(error)) {
        setDisableReview(null);
        setMessage("The LAN sharing removal consent changed or could not be confirmed. Rig refreshed the access snapshot and retained the exact operation ID for recovery.");
        void refreshAccess();
      } else {
        setMessage(`The LAN sharing removal result for operation ${request.operationId} is uncertain. ${error instanceof Error ? error.message : "Replay only this exact request."}`);
      }
    },
  });

  const lanAddressWithheld = lanAddressAction.phase === "checking" || lanAddressAction.phase === "failure";
  const lanURL = access.isFetching || disable.isPending || Boolean(disableClaim) || Boolean(unresolvedDisableRequest) || lanAddressWithheld
    ? null : verifiedLANAccessURL(access.data);

  useEffect(() => {
    setUnresolvedReservation(readAttempt(appId, userId, "reservation", validReservationRequest));
    setUnresolvedGrantRequest(readAttempt(appId, userId, "grant", validGrantRequest));
    setUnresolvedDisableRequest(readAttempt(appId, userId, "disable", validDisableRequest));
  }, [appId, userId]);

  useEffect(() => {
    if (!accessReady) return;
    if (unresolvedReservation && reservationMatchesRequest(pendingReservation, unresolvedReservation)) {
      clearAttempt(appId, "reservation");
      setUnresolvedReservation(null);
      setMessage(`The current access state confirms reservation operation ${unresolvedReservation.operationId}.`);
    }
    if (unresolvedGrantRequest && grantMatchesRequest(grantClaim, currentRevision, unresolvedGrantRequest)) {
      clearAttempt(appId, "grant");
      setUnresolvedGrantRequest(null);
      setMessage(`The current access state confirms activation claim ${unresolvedGrantRequest.attemptId}.`);
    }
    if (unresolvedDisableRequest && disableMatchesRequest(disableClaim, unresolvedDisableRequest)) {
      clearAttempt(appId, "disable");
      setUnresolvedDisableRequest(null);
      setMessage(`The current access state confirms LAN sharing removal operation ${unresolvedDisableRequest.operationId}.`);
    }
  }, [accessReady, appId, disableClaim, grantClaim, pendingReservation, unresolvedDisableRequest, unresolvedGrantRequest, unresolvedReservation]);

  useEffect(() => {
    if (!focusAfterReservationReview || !pendingReservation) return;
    const timer = window.setTimeout(() => {
      reservationReviewButton.current?.focus();
      setFocusAfterReservationReview(false);
    }, 0);
    return () => window.clearTimeout(timer);
  }, [focusAfterReservationReview, pendingReservation]);

  useEffect(() => {
    if (!focusAfterReservationRecovery || !unresolvedReservation) return;
    const timer = window.setTimeout(() => {
      if (!reservationRecoveryHeading.current) return;
      reservationRecoveryHeading.current.focus();
      setFocusAfterReservationRecovery(false);
    }, 0);
    return () => window.clearTimeout(timer);
  }, [focusAfterReservationRecovery, unresolvedReservation]);

  useEffect(() => {
    if (!focusAfterApproval || !currentRevision || grantClaim) return;
    const timer = window.setTimeout(() => {
      activationReviewButton.current?.focus();
      setFocusAfterApproval(false);
    }, 0);
    return () => window.clearTimeout(timer);
  }, [currentRevision, focusAfterApproval, grantClaim]);

  useEffect(() => {
    if (!focusAfterGrant || (!grantClaim && !unresolvedGrantRequest)) return;
    const timer = window.setTimeout(() => {
      const target = grantClaim ? grantHeading.current : grantRecoveryHeading.current;
      if (!target) return;
      target.focus();
      setFocusAfterGrant(false);
    }, 0);
    return () => window.clearTimeout(timer);
  }, [focusAfterGrant, grantClaim, unresolvedGrantRequest]);

  useEffect(() => {
    if (!focusAfterDisable) return;
    const timer = window.setTimeout(() => {
      const target = disableClaim ? disableHeading.current : recoveryHeading.current;
      if (!target) return;
      target.focus();
      setFocusAfterDisable(false);
    }, 0);
    return () => window.clearTimeout(timer);
  }, [disableClaim, focusAfterDisable, unresolvedDisableRequest]);

  useEffect(() => {
    if (lanAddressAction.phase === "checking" || lanAddressAction.phase === "failure") {
      const timer = window.setTimeout(() => lanAddressActionStatus.current?.focus(), 0);
      return () => window.clearTimeout(timer);
    }
    if (lanAddressAction.phase === "success") {
      const timer = window.setTimeout(() => {
        const target = lanAddressAction.action === "open" ? openLANAddressButton.current : copyLANAddressButton.current;
        target?.focus();
      }, 0);
      return () => window.clearTimeout(timer);
    }
  }, [lanAddressAction]);

  useEffect(() => {
    if (access.isFetching || access.isError || !access.data) return;
    if (lanURL) setMessage(`LAN route verified at ${access.data.observedAt}.`);
    else if (routeExpired) setMessage("LAN route verification expired. Check LAN access again before opening or copying an address.");
  }, [access.data, access.isError, access.isFetching, lanURL, routeExpired]);

  const refresh = async () => {
    setMessage("Checking the latest LAN access observation.");
    const checks = await Promise.all([
      access.refetch(),
      profile.refetch(),
      grantClaim ? grantState.refetch() : Promise.resolve(undefined),
      disableClaim ? disableState.refetch() : Promise.resolve(undefined),
    ]);
    const failed = checks.some((check) => check?.isError);
    if (!failed) setLANAddressAction({ phase: "idle" });
    setMessage(failed
      ? "Rig could not refresh the complete LAN access observation. Review the reported error before using an address."
      : "LAN access observation refreshed.");
  };

  const currentProfile = profile.data?.desiredProfile;
  const canReserve = Boolean(accessReady && currentProfile && !profile.isFetching && !currentRevision && !pendingReservation &&
    !unresolvedReservation && !unresolvedGrantRequest && !unresolvedDisableRequest && !grantUnresolved(grantClaim) && !reserve.isPending);
  const disableAllowed = Boolean(accessReady && currentDisableReview && !disableClaim && !unresolvedDisableRequest && !unresolvedGrantRequest && !grantUnresolved(grantClaim));
  const observedGrantMatchesSnapshot = grantState.data?.claim.attemptId === grantClaim?.attemptId;
  const observedDisableMatchesSnapshot = disableState.data?.claim.operationId === disableClaim?.operationId;
  const closeReservationReview = () => {
    setReservationReview(null);
    setFocusAfterReservationReview(true);
  };

  if (!enabled) return <section className="lan-access-panel" aria-labelledby="lan-access-title"><div className="lan-heading"><div><h2 id="lan-access-title">LAN access</h2><p>LAN sharing requires the generated runtime on this controller.</p></div></div></section>;
  if (!canManage) return <section className="lan-access-panel" aria-labelledby="lan-access-title"><div className="lan-heading"><div><h2 id="lan-access-title">LAN access</h2><p>An administrator can inspect and manage LAN sharing for this application.</p></div></div></section>;

  const accessBusy = access.isFetching || profile.isFetching;
  return <section className="lan-access-panel" aria-labelledby="lan-access-title">
    <div className="lan-heading"><div><h2 id="lan-access-title">LAN access</h2><p>Keep this application local until an administrator approves and attests a separate LAN route.</p></div><button className="button small" type="button" disabled={accessBusy || lanAddressAction.phase === "checking" || reserve.isPending || approve.isPending || grant.isPending || disable.isPending} onClick={() => void refresh()}>Check LAN access</button></div>
    <span className="sr-only" role="status" aria-live="polite" aria-atomic="true">{lanAddressAction.phase === "checking" ? `Checking the LAN address before ${lanAddressAction.action === "open" ? "opening" : "copying"}.` : lanAddressAction.phase === "success" ? lanAddressAction.message : reserve.isPending ? "Reserving a LAN port for review." : approve.isPending ? "Approving LAN access." : grant.isPending ? "Applying the LAN route." : disable.isPending ? "Removing LAN sharing." : accessBusy ? "Checking the latest LAN access observation." : message}</span>
    {message && <p className="lan-message">{message}</p>}
    {lanAddressAction.phase === "success" && <p className="lan-message">{lanAddressAction.message}</p>}
    {access.isLoading ? <p role="status">Loading LAN access state…</p> : access.isError ? <div className="callout danger" role="alert"><strong>LAN access state could not be loaded.</strong><span>{access.error.message}</span><button className="button small" type="button" onClick={() => void access.refetch()}>Retry LAN access check</button></div> : <>
      {lanAddressAction.phase === "checking" ? <div ref={lanAddressActionStatus} className="callout info" role="status" tabIndex={-1}><strong>Checking LAN address</strong><span>Rig is proving current system state and a fresh exact LAN access observation before using the address.</span></div> : lanAddressAction.phase === "failure" ? <div ref={lanAddressActionStatus} className="callout danger" role="alert" tabIndex={-1}><strong>LAN address action stopped</strong><span>{lanAddressAction.message}</span></div> : access.data?.availability === "local_only" && !disableClaim ? <div className="callout info"><strong>Local only</strong><span>No approved LAN access exists for this application.</span></div> : accessRefreshWithheld ? <div className="callout info" role="status"><strong>Checking LAN route</strong><span>The previous LAN address is hidden until this controller returns a fresh attestation.</span></div> : lanURL ? <div className="callout info lan-address"><strong>LAN route verified</strong><span>HTTP is reachable from devices that can reach this controller address. Verified at {access.data?.observedAt}.</span><code>{lanURL}</code><div className="lan-actions"><button ref={openLANAddressButton} className="button small" type="button" onClick={() => openLANAddress(access.data)}>Open LAN address</button><button ref={copyLANAddressButton} className="button small" type="button" onClick={() => copyLANAddress(access.data)}>Copy address</button></div></div> : <div className="callout warning"><strong>{disableClaim?.state === "committed" ? "LAN sharing removed" : disableClaim ? "LAN sharing is being removed" : routeExpired ? "LAN route verification expired" : access.data?.availability === "verified" ? "LAN address withheld" : "LAN route is not verified"}</strong><span>{disableClaim?.state === "committed" ? "The controller withdrew the LAN route and recorded release proof. No LAN address is available." : disableClaim ? "The controller is releasing this LAN route. Its address stays hidden until the removal claim resolves." : routeExpired ? "The LAN route observation is too old to open or copy an address. Check LAN access again." : access.data?.availability === "verified" ? "The controller did not return a current safe LAN address. Check LAN access again before opening or copying an address." : "An approved profile or port allocation is not proof that the gateway is serving this application. No LAN address is shown."}</span></div>}
      {currentRevision && <dl className="lan-details"><div><dt>Approved access revision</dt><dd>{currentRevision.revisionNumber}</dd></div><div><dt>Allocated port</dt><dd>{currentRevision.allocation.port}</dd></div><div><dt>Allocation state</dt><dd>{shortState(currentRevision.allocation.state)}</dd></div><div><dt>Approval digest</dt><dd className="mono">{currentRevision.specDigest}</dd></div></dl>}
      {pendingReservation && <aside className="lan-pending" aria-labelledby="lan-reservation-title"><h3 id="lan-reservation-title">Reserved port awaiting approval</h3><p>Port {pendingReservation.reservation.allocation.port} is reserved for this exact review. No address is available.</p><dl className="lan-details"><div><dt>Allocation identifier</dt><dd className="mono">{pendingReservation.reservation.allocation.id}</dd></div><div><dt>Operation identifier</dt><dd className="mono">{pendingReservation.operationId}</dd></div></dl><button ref={reservationReviewButton} className="button small" type="button" onClick={() => setReservationReview(pendingReservation)}>Review reserved port</button></aside>}
      {grantClaim && <aside className="lan-pending" aria-labelledby="lan-claim-title"><h3 ref={grantHeading} id="lan-claim-title" tabIndex={-1}>Activation claim</h3><p>Claim <span className="mono">{grantClaim.attemptId}</span> is {shortState(grantClaim.state)}.</p>{grantState.isError ? <div className="callout danger" role="alert"><strong>Claim observation could not be loaded.</strong><span>{grantState.error.message}</span><button className="button small" type="button" onClick={() => void grantState.refetch()}>Retry claim check</button></div> : observedGrantMatchesSnapshot && grantState.data && <dl className="lan-details"><div><dt>Durable state</dt><dd>{shortState(grantState.data.claim.state)}</dd></div><div><dt>Gateway observation</dt><dd>{shortState(grantState.data.observed.availability)}</dd></div>{grantState.data.observed.observedAt && <div><dt>Observed at</dt><dd>{grantState.data.observed.observedAt}</dd></div>}</dl>}</aside>}
      {disableClaim && <aside className="lan-pending" aria-labelledby="lan-disable-title"><h3 ref={disableHeading} id="lan-disable-title" tabIndex={-1}>LAN sharing removal claim</h3><p>Removal operation <span className="mono">{disableClaim.operationId}</span> is {shortState(disableClaim.state)}.</p>{disableClaim.state === "committed" && <p>Port {disableClaim.port} was released{disableClaim.releasedAt ? ` at ${disableClaim.releasedAt}` : ""}.</p>}{disableState.isError ? <div className="callout danger" role="alert"><strong>Removal observation could not be loaded.</strong><span>{disableState.error.message}</span><button className="button small" type="button" onClick={() => void disableState.refetch()}>Retry removal check</button></div> : observedDisableMatchesSnapshot && disableState.data && <dl className="lan-details"><div><dt>Durable state</dt><dd>{shortState(disableState.data.claim.state)}</dd></div><div><dt>Gateway observation</dt><dd>{shortState(disableState.data.observed.availability)}</dd></div>{disableState.data.observed.observedAt && <div><dt>Observed at</dt><dd>{disableState.data.observed.observedAt}</dd></div>}</dl>}</aside>}
      {unresolvedReservation && !reservationMatchesRequest(pendingReservation, unresolvedReservation) && <aside className="lan-pending" aria-labelledby="lan-reservation-recovery-title"><h3 ref={reservationRecoveryHeading} id="lan-reservation-recovery-title" tabIndex={-1}>Reservation result needs reconciliation</h3><p>Rig did not receive a verified result for this reservation. It will not create another reservation while the original operation is unresolved.</p><dl className="lan-details"><div><dt>Original operation</dt><dd className="mono">{unresolvedReservation.operationId}</dd></div><div><dt>Expected access revision</dt><dd>{unresolvedReservation.expectedRevisionNumber}</dd></div><div><dt>Gateway profile revision</dt><dd>{unresolvedReservation.profileRevisionNumber}</dd></div></dl><div className="lan-actions"><button className="button small" type="button" disabled={!accessReady || reserve.isPending} onClick={() => reserve.mutate(unresolvedReservation)}>Replay exact reservation</button><button className="button small" type="button" disabled={accessBusy || reserve.isPending} onClick={() => void refresh()}>Check current access state</button></div></aside>}
      {unresolvedGrantRequest && !grantMatchesRequest(grantClaim, currentRevision, unresolvedGrantRequest) && <aside className="lan-pending" aria-labelledby="lan-grant-recovery-title"><h3 ref={grantRecoveryHeading} id="lan-grant-recovery-title" tabIndex={-1}>Activation result needs reconciliation</h3><p>Rig retained the exact activation claim and will not create a replacement request while it is unresolved.</p><dl className="lan-details"><div><dt>Claim identifier</dt><dd className="mono">{unresolvedGrantRequest.attemptId}</dd></div><div><dt>Access revision</dt><dd>{unresolvedGrantRequest.accessRevisionNumber}</dd></div></dl><div className="lan-actions"><button className="button small" type="button" disabled={!accessReady || grant.isPending} onClick={() => grant.mutate(unresolvedGrantRequest)}>Replay exact activation</button><button className="button small" type="button" disabled={accessBusy || grant.isPending} onClick={() => void refresh()}>Check current access state</button></div></aside>}
      {unresolvedDisableRequest && !disableMatchesRequest(disableClaim, unresolvedDisableRequest) && <aside className="lan-pending" aria-labelledby="lan-disable-recovery-title"><h3 ref={recoveryHeading} id="lan-disable-recovery-title" tabIndex={-1}>LAN sharing removal needs reconciliation</h3><p>Rig retained the exact removal operation. The port remains held until the controller records a release proof, and Rig will not create a replacement operation.</p><dl className="lan-details"><div><dt>Removal operation</dt><dd className="mono">{unresolvedDisableRequest.operationId}</dd></div><div><dt>Access revision</dt><dd>{unresolvedDisableRequest.accessRevisionNumber}</dd></div><div><dt>Allocation identifier</dt><dd className="mono">{unresolvedDisableRequest.allocationId}</dd></div></dl><div className="lan-actions"><button className="button small" type="button" disabled={!accessReady || disable.isPending} onClick={() => disable.mutate(unresolvedDisableRequest)}>Replay exact removal</button><button className="button small" type="button" disabled={accessBusy || disable.isPending} onClick={() => void refresh()}>Check current access state</button></div></aside>}
      {!currentRevision && !pendingReservation && !unresolvedReservation && !unresolvedGrantRequest && !unresolvedDisableRequest && <>{profile.isLoading ? <p role="status">Checking the approved gateway profile…</p> : profile.isError ? <div className="callout danger" role="alert"><strong>Gateway profile could not be checked.</strong><span>{profile.error.message}</span><button className="button small" type="button" onClick={() => void profile.refetch()}>Retry profile check</button></div> : !currentProfile ? <div className="callout warning"><strong>LAN gateway profile required</strong><span>Approve a desired LAN gateway profile from Machines before reserving a port for this application.</span></div> : <button className="button" type="button" disabled={!canReserve} onClick={() => { setMessage(""); reserve.mutate({ operationId: crypto.randomUUID(), expectedRevisionNumber: access.data!.expectedRevisionNumber, profileRevisionId: currentProfile.id, profileRevisionNumber: currentProfile.revisionNumber }); }}>{reserve.isPending ? "Reserving port…" : "Reserve LAN port for review"}</button>}</>}
      {currentRevision && !lanURL && !lanAddressWithheld && !grantClaim && !unresolvedGrantRequest && !disableClaim && !unresolvedDisableRequest && <button ref={activationReviewButton} className="button" type="button" disabled={!accessReady || grant.isPending} onClick={() => setGrantReview(currentRevision)}>Review LAN activation</button>}
      {currentRevision && !lanURL && grantUnresolved(grantClaim) && <div className="callout warning"><strong>LAN activation is still resolving.</strong><span>Rig will not offer LAN sharing removal until the current activation claim reaches a terminal state.</span></div>}
      {currentRevision && disableAllowed && <button ref={disableReviewButton} className="button danger" type="button" disabled={disable.isPending || lanAddressAction.phase === "checking"} onClick={() => setDisableReview(currentDisableReview)}>Review LAN sharing removal</button>}
      {currentRevision && accessReady && access.data?.disableReview && !currentDisableReview && <div className="callout warning"><strong>LAN sharing removal review is stale.</strong><span>The controller’s removal consent no longer matches the active access revision. Refresh LAN access before taking action.</span></div>}
      {reserve.isError && !unresolvedReservation && <div className="callout danger" role="alert"><strong>Port reservation was not completed.</strong><span>{reserve.error.message}</span><button className="button small" type="button" onClick={() => void refresh()}>Refresh access summary</button></div>}
    </>}
    {reservationReview && <Dialog title="Review LAN application access" description="Confirm the exact reserved allocation and approval digest. This does not create a usable address." pending={approve.isPending} focusTitle close={closeReservationReview}>
      <dl className="lan-review-details"><div><dt>Application</dt><dd className="mono">{appId}</dd></div><div><dt>Access revision</dt><dd>New revision {reservationReview.expectedRevisionNumber + 1}</dd></div><div><dt>Reserved port</dt><dd>{reservationReview.reservation.allocation.port}</dd></div><div><dt>Allocation identifier</dt><dd className="mono">{reservationReview.reservation.allocation.id}</dd></div><div><dt>Approval digest</dt><dd className="mono">{reservationReview.reservation.approvalDigest}</dd></div></dl>
      {approve.isError ? <div ref={approvalErrorTarget} className="callout danger" role="alert" tabIndex={-1}><strong>LAN access approval was not completed.</strong><span>{approve.error.message}</span><button className="button small" type="button" onClick={() => { closeReservationReview(); void refresh(); }}>Refresh access summary</button></div> : <div className="deployment-dialog-actions"><button className="button" type="button" disabled={approve.isPending} onClick={closeReservationReview}>Cancel</button><button className="button primary" type="button" disabled={approve.isPending || !canApprove} onClick={() => approve.mutate(reservationReview)}>{approve.isPending ? "Approving…" : "Approve LAN access"}</button></div>}
    </Dialog>}
    {grantReview && <Dialog title="Review LAN activation" description="Confirm the exact approved access revision. Rig will then request an attested gateway route and still withhold the address until verified." pending={grant.isPending} focusTitle close={() => setGrantReview(null)}>
      <dl className="lan-review-details"><div><dt>Access revision</dt><dd>{grantReview.revisionNumber}</dd></div><div><dt>Access identifier</dt><dd className="mono">{grantReview.id}</dd></div><div><dt>Allocated port</dt><dd>{grantReview.allocation.port}</dd></div><div><dt>Approval digest</dt><dd className="mono">{grantReview.specDigest}</dd></div></dl>
      <div className="deployment-dialog-actions"><button className="button" type="button" disabled={grant.isPending} onClick={() => setGrantReview(null)}>Cancel</button><button className="button primary" type="button" disabled={grant.isPending || !canGrant} onClick={() => grant.mutate({ attemptId: crypto.randomUUID(), accessRevisionId: grantReview.id, accessRevisionNumber: grantReview.revisionNumber, approvalDigest: grantReview.specDigest })}>{grant.isPending ? "Activating…" : "Grant and attest LAN route"}</button></div>
    </Dialog>}
    {disableReview && <Dialog title="Review LAN sharing removal" description="Confirm the exact approved LAN allocation. Rig asks the controller to withdraw the route; the port stays held until release proof is recorded." pending={disable.isPending} focusTitle close={() => { setDisableReview(null); disableReviewButton.current?.focus(); }}>
      <dl className="lan-review-details"><div><dt>Application</dt><dd className="mono">{disableReview.appId}</dd></div><div><dt>Access revision</dt><dd>{disableReview.accessRevisionNumber}</dd></div><div><dt>Access identifier</dt><dd className="mono">{disableReview.accessRevisionId}</dd></div><div><dt>Allocation owner</dt><dd className="mono">{disableReview.ownerOperationId}</dd></div><div><dt>Allocation identifier</dt><dd className="mono">{disableReview.allocationId}</dd></div><div><dt>Allocated port</dt><dd>{disableReview.port}</dd></div><div><dt>Gateway profile revision</dt><dd>{disableReview.gatewayProfileRevisionNumber}</dd></div><div><dt>Gateway profile identifier</dt><dd className="mono">{disableReview.gatewayProfileRevisionId}</dd></div><div><dt>Removal approval digest</dt><dd className="mono">{disableReview.approvalDigest}</dd></div></dl>
      <div className="deployment-dialog-actions"><button className="button" type="button" disabled={disable.isPending} onClick={() => { setDisableReview(null); disableReviewButton.current?.focus(); }}>Cancel</button><button className="button danger" type="button" disabled={disable.isPending || !canDisable} onClick={() => disable.mutate({ operationId: crypto.randomUUID(), accessRevisionId: disableReview.accessRevisionId, accessRevisionNumber: disableReview.accessRevisionNumber, allocationId: disableReview.allocationId, approvalDigest: disableReview.approvalDigest })}>{disable.isPending ? "Removing…" : "Remove LAN sharing"}</button></div>
    </Dialog>}
  </section>;
}
