import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { createMemoryRouter, Link, Outlet, RouterProvider } from "react-router-dom";
import { UnsavedChangesGuard, useUnsavedChanges } from "./unsaved-changes";

function EditPage() {
  useUnsavedChanges(true);
  return <><h1>Edit application A</h1><Link to="/apps/b">Open application B</Link></>;
}

function routerAtEdit(initialEntries = ["/apps/a"], initialIndex = 0) {
  return createMemoryRouter([{
    element: <UnsavedChangesGuard><Outlet/></UnsavedChangesGuard>,
    children: [
      { path: "/apps/a", element: <EditPage/> },
      { path: "/apps/b", element: <h1>Application B</h1> },
      { path: "/previous", element: <h1>Previous page</h1> },
    ],
  }], { initialEntries, initialIndex });
}

describe("UnsavedChangesGuard", () => {
  afterEach(() => { cleanup(); vi.restoreAllMocks(); });

  it("confirms internal navigation and preserves the dirty page when declined", async () => {
    const confirm = vi.spyOn(window, "confirm").mockReturnValueOnce(false).mockReturnValueOnce(true);
    const router = routerAtEdit();
    render(<RouterProvider router={router}/>);
    await screen.findByRole("heading", { name: "Edit application A" });
    fireEvent.click(screen.getByRole("link", { name: "Open application B" }));
    await waitFor(() => expect(confirm).toHaveBeenCalledTimes(1));
    expect(screen.getByRole("heading", { name: "Edit application A" })).not.toBeNull();
    fireEvent.click(screen.getByRole("link", { name: "Open application B" }));
    expect(await screen.findByRole("heading", { name: "Application B" })).not.toBeNull();
    expect(confirm).toHaveBeenCalledTimes(2);
  });

  it("guards browser back navigation and beforeunload without storing a draft", async () => {
    const confirm = vi.spyOn(window, "confirm").mockReturnValueOnce(false).mockReturnValueOnce(true);
    const localWrite = vi.spyOn(Storage.prototype, "setItem");
    const router = routerAtEdit(["/previous", "/apps/a"], 1);
    render(<RouterProvider router={router}/>);
    await screen.findByRole("heading", { name: "Edit application A" });

    const unload = new Event("beforeunload", { cancelable: true });
    window.dispatchEvent(unload);
    expect(unload.defaultPrevented).toBe(true);
    expect(localWrite).not.toHaveBeenCalled();

    await act(async () => { await router.navigate(-1); });
    await waitFor(() => expect(confirm).toHaveBeenCalledTimes(1));
    expect(screen.getByRole("heading", { name: "Edit application A" })).not.toBeNull();
    await act(async () => { await router.navigate(-1); });
    expect(await screen.findByRole("heading", { name: "Previous page" })).not.toBeNull();
  });
});
