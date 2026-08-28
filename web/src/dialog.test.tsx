import { useState } from "react";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { Dialog } from "./dialog";

function rootContainer() {
  const root = document.createElement("div");
  root.id = "root";
  document.body.append(root);
  return root;
}

function DialogHarness({ pending = false }: { pending?: boolean }) {
  const [open, setOpen] = useState(false);
  return <>
    <button type="button" onClick={() => setOpen(true)}>Open dialog</button>
    {open && <Dialog title="Managed dialog" pending={pending} close={() => setOpen(false)}><button type="button">Confirm</button></Dialog>}
  </>;
}

describe("Dialog", () => {
  afterEach(() => {
    cleanup();
    document.getElementById("root")?.remove();
  });

  it("wraps around only visible targets when CSS-hidden controls occupy first, middle, and last positions", () => {
    render(<Dialog title="Accessible dialog" description="Only this concise consequence is described." close={() => undefined}><div style={{ display: "none" }}><button id="css-hidden-first" type="button">CSS-hidden first</button></div><a href="https://example.com">First visible link</a><div hidden><button id="hidden-dialog-button" type="button">Hidden button</button></div><div inert><button id="inert-dialog-button" type="button">Inert button</button></div><div style={{ visibility: "hidden" }}><button id="css-hidden-middle" type="button">CSS-hidden middle</button></div><div aria-hidden="true"><button id="aria-hidden-dialog-button" type="button">Aria-hidden button</button></div><select aria-label="Choice"><option>One</option></select><button type="button">Last visible button</button><div style={{ display: "none" }}><button id="css-hidden-last" type="button">CSS-hidden last</button></div></Dialog>);
    const dialog = screen.getByRole("dialog", { name: "Accessible dialog" });
    const description = document.getElementById(dialog.getAttribute("aria-describedby")!);
    expect(description?.textContent).toBe("Only this concise consequence is described.");
    expect(screen.getAllByText("Only this concise consequence is described.")).toHaveLength(1);
    const first = screen.getByRole("link", { name: "First visible link" });
    const last = screen.getByRole("button", { name: "Last visible button" });
    expect(document.activeElement).toBe(first);
    for (const id of ["css-hidden-first", "hidden-dialog-button", "inert-dialog-button", "css-hidden-middle", "aria-hidden-dialog-button", "css-hidden-last"]) {
      document.getElementById(id)?.focus();
      fireEvent.keyDown(document, { key: "Tab" });
      expect(document.activeElement).toBe(first);
      document.getElementById(id)?.focus();
      fireEvent.keyDown(document, { key: "Tab", shiftKey: true });
      expect(document.activeElement).toBe(last);
    }
    last.focus();
    fireEvent.keyDown(document, { key: "Tab" });
    expect(document.activeElement).toBe(first);
    first.focus();
    fireEvent.keyDown(document, { key: "Tab", shiftKey: true });
    expect(document.activeElement).toBe(last);
  });

  it("suppresses Escape while pending, then restores inert state and launcher focus", () => {
    const root = rootContainer();
    root.setAttribute("aria-hidden", "false");
    root.inert = false;
    const view = render(<DialogHarness pending/>, { container: root });
    const launcher = screen.getByRole("button", { name: "Open dialog" });
    launcher.focus();
    fireEvent.click(launcher);
    expect(screen.getByRole("dialog", { name: "Managed dialog" })).not.toBeNull();
    expect(root.getAttribute("aria-hidden")).toBe("true");
    expect(root.inert).toBe(true);

    fireEvent.keyDown(document, { key: "Escape" });
    expect(screen.getByRole("dialog", { name: "Managed dialog" })).not.toBeNull();

    view.rerender(<DialogHarness pending={false}/>);
    fireEvent.keyDown(document, { key: "Escape" });
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(root.getAttribute("aria-hidden")).toBe("false");
    expect(root.inert).toBe(false);
    expect(document.activeElement).toBe(launcher);
  });

  it("restores background attributes and removes keyboard handling when unmounted", () => {
    const root = rootContainer();
    root.setAttribute("aria-hidden", "background-state");
    root.inert = false;
    const close = vi.fn();
    const view = render(<Dialog title="Unmounted dialog" close={close}><button type="button">Confirm</button></Dialog>, { container: root });
    expect(screen.getByRole("dialog", { name: "Unmounted dialog" }).getAttribute("aria-describedby")).toBeNull();
    expect(root.getAttribute("aria-hidden")).toBe("true");
    expect(root.inert).toBe(true);
    view.unmount();
    expect(root.getAttribute("aria-hidden")).toBe("background-state");
    expect(root.inert).toBe(false);
    fireEvent.keyDown(document, { key: "Escape" });
    expect(close).not.toHaveBeenCalled();
  });
});
