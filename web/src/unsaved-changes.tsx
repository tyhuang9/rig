import { createContext, useCallback, useContext, useEffect, useMemo, useState } from "react";
import { useBlocker } from "react-router-dom";

const leaveWarning = "Discard unsaved configuration changes and leave this page?";

type UnsavedChangesState = {
  dirty: boolean;
  setDirty: (dirty: boolean) => void;
  confirmDiscard: () => boolean;
};

const UnsavedChangesContext = createContext<UnsavedChangesState>({
  dirty: false,
  setDirty: () => undefined,
  confirmDiscard: () => true,
});

export function UnsavedChangesGuard({ children }: { children: React.ReactNode }) {
  const [dirty, setDirty] = useState(false);
  const blocker = useBlocker(dirty);
  const confirmDiscard = useCallback(() => !dirty || window.confirm(leaveWarning), [dirty]);

  useEffect(() => {
    if (blocker.state !== "blocked") return;
    if (window.confirm(leaveWarning)) blocker.proceed();
    else blocker.reset();
  }, [blocker]);

  useEffect(() => {
    if (!dirty) return;
    const warnBeforeUnload = (event: BeforeUnloadEvent) => {
      event.preventDefault();
      event.returnValue = "";
    };
    window.addEventListener("beforeunload", warnBeforeUnload);
    return () => window.removeEventListener("beforeunload", warnBeforeUnload);
  }, [dirty]);

  const value = useMemo(() => ({ dirty, setDirty, confirmDiscard }), [confirmDiscard, dirty]);
  return <UnsavedChangesContext.Provider value={value}>{children}</UnsavedChangesContext.Provider>;
}

export function useUnsavedChanges(dirty: boolean) {
  const setGuardDirty = useContext(UnsavedChangesContext).setDirty;
  useEffect(() => setGuardDirty(dirty), [dirty, setGuardDirty]);
  useEffect(() => () => setGuardDirty(false), [setGuardDirty]);
}

export function useConfirmDiscard() {
  return useContext(UnsavedChangesContext).confirmDiscard;
}
