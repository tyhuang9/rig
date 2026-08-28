import { useEffect, useId, useRef } from "react";
import { createPortal } from "react-dom";

export function Dialog({
  title,
  description,
  close,
  pending = false,
  children,
}: {
  title: string;
  description?: string;
  close: () => void;
  pending?: boolean;
  children: React.ReactNode;
}) {
  const ref = useRef<HTMLDivElement>(null);
  const titleId = useId();
  const descriptionId = useId();
  const restore = useRef<HTMLElement | null>(
    document.activeElement instanceof HTMLElement ? document.activeElement : null,
  );
  const closeRef = useRef(close);
  closeRef.current = close;
  const pendingRef = useRef(pending);
  pendingRef.current = pending;

  useEffect(() => {
    const element = ref.current;
    const root = document.getElementById("root");
    const previousAriaHidden = root?.getAttribute("aria-hidden");
    const previousInert = root?.inert;
    root?.setAttribute("aria-hidden", "true");
    if (root) root.inert = true;

    const visiblyFocusable = (target: HTMLElement) => {
      for (let current: HTMLElement | null = target; current && element?.contains(current); current = current.parentElement) {
        if (current.matches("[hidden], [inert], [aria-hidden='true']")) return false;
        const style = window.getComputedStyle(current);
        if (style.display === "none" || style.visibility === "hidden" || style.visibility === "collapse") return false;
        if (current === element) break;
      }
      return true;
    };
    const focusable = () => [
      ...(element?.querySelectorAll<HTMLElement>(
        "a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex='-1'])",
      ) ?? []),
    ].filter(visiblyFocusable);

    (focusable()[0] ?? element)?.focus();
    const keydown = (event: KeyboardEvent) => {
      if (event.key === "Escape" && !pendingRef.current) {
        event.preventDefault();
        closeRef.current();
        return;
      }
      if (event.key !== "Tab") return;
      const targets = focusable();
      if (!targets.length) {
        event.preventDefault();
        element?.focus();
        return;
      }
      const first = targets[0];
      const last = targets.at(-1)!;
      const activeIndex = targets.indexOf(document.activeElement as HTMLElement);
      if (activeIndex === -1) {
        event.preventDefault();
        (event.shiftKey ? last : first).focus();
      } else if (event.shiftKey && activeIndex === 0) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && activeIndex === targets.length - 1) {
        event.preventDefault();
        first.focus();
      }
    };
    document.addEventListener("keydown", keydown);
    return () => {
      document.removeEventListener("keydown", keydown);
      if (root) {
        if (previousAriaHidden === null) root.removeAttribute("aria-hidden");
        else root.setAttribute("aria-hidden", previousAriaHidden ?? "");
        root.inert = previousInert ?? false;
      }
      restore.current?.focus();
    };
  }, []);

  return createPortal(
    <div className="deployment-dialog-backdrop" role="presentation">
      <div
        ref={ref}
        className="deployment-dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        aria-describedby={description ? descriptionId : undefined}
        tabIndex={-1}
      >
        <h2 id={titleId}>{title}</h2>
        {description && <p id={descriptionId} className="deployment-dialog-description">{description}</p>}
        <div className="deployment-dialog-content">{children}</div>
      </div>
    </div>,
    document.body,
  );
}
