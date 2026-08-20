import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { APIError, api } from "./api";
import { ApplicationConfigurationPanel } from "./application-configuration";

const initial = {
  revisionId: "11111111-1111-1111-1111-111111111111",
  revisionNumber: 1,
  updatedAt: "2026-08-20T12:00:00Z",
  entries: [
    { key: "EMPTY", sensitive: false, value: "" },
    { key: "TOKEN", sensitive: true },
  ],
};

function renderPanel() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  render(<QueryClientProvider client={client}><ApplicationConfigurationPanel appId="app-1" /></QueryClientProvider>);
  return client;
}

describe("ApplicationConfigurationPanel", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
    vi.spyOn(api, "applicationConfiguration").mockResolvedValue(initial);
  });
  afterEach(cleanup);

  it("never hydrates secrets and preserves a blank stored replacement", async () => {
    const replace = vi.spyOn(api, "replaceApplicationConfiguration").mockResolvedValue({ ...initial, revisionNumber: 2 });
    renderPanel();
    expect(await screen.findByDisplayValue("EMPTY")).not.toBeNull();
    const replacement = screen.getByLabelText("Replacement value");
    expect((replacement as HTMLInputElement).value).toBe("");
    expect(replacement.getAttribute("type")).toBe("password");
    expect(screen.getByText("Stored")).not.toBeNull();
    expect(screen.queryByText("sentinel-stored-secret")).toBeNull();
    const visibleValue = screen.getByLabelText("Value");
    fireEvent.change(visibleValue, { target: { value: "temporary" } });
    fireEvent.change(visibleValue, { target: { value: "" } });
    fireEvent.click(screen.getByRole("button", { name: "Save configuration" }));
    await waitFor(() => expect(replace).toHaveBeenCalledWith("app-1", {
      expectedRevisionNumber: 1,
      variables: [{ key: "EMPTY", value: "" }],
      secrets: [],
      remove: [],
    }));
  });

  it("supports focused additions and explicit secret removal with undo", async () => {
    renderPanel();
    await screen.findByText("Stored");
    fireEvent.click(screen.getByRole("button", { name: "Add variable" }));
    const names = screen.getAllByLabelText("Variable name");
    expect(document.activeElement).toBe(names.at(-1));
    fireEvent.click(screen.getByRole("button", { name: "Remove secret TOKEN" }));
    expect(screen.getByRole("button", { name: "Undo remove" })).not.toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Undo remove" }));
    expect(screen.getByRole("button", { name: "Remove secret TOKEN" })).not.toBeNull();
  });

  it("preserves edits on conflict and provides an explicit reload recovery", async () => {
    const configuration = vi.mocked(api.applicationConfiguration);
    configuration.mockReset();
    configuration.mockResolvedValueOnce(initial).mockResolvedValue({ revisionId: "new", revisionNumber: 2, entries: [{ key: "LATEST", sensitive: false, value: "yes" }] });
    vi.spyOn(api, "replaceApplicationConfiguration").mockRejectedValue(new APIError({ status: 409, code: "configuration_conflict", detail: "Application configuration changed; reload and try again" }));
    renderPanel();
    const value = await screen.findByLabelText("Value");
    fireEvent.change(value, { target: { value: "local-edit" } });
    fireEvent.click(screen.getByRole("button", { name: "Save configuration" }));
    const reload = await screen.findByRole("button", { name: "Reload latest configuration" });
    expect((value as HTMLInputElement).value).toBe("local-edit");
    expect(document.activeElement).toBe(reload.closest("[role=alert]"));
    fireEvent.click(reload);
    expect(await screen.findByDisplayValue("LATEST")).not.toBeNull();
    expect(screen.queryByDisplayValue("local-edit")).toBeNull();
  });

  it("retains edits when conflict recovery cannot reload", async () => {
    const configuration = vi.mocked(api.applicationConfiguration);
    configuration.mockReset();
    configuration.mockResolvedValueOnce(initial).mockRejectedValue(new Error("offline"));
    vi.spyOn(api, "replaceApplicationConfiguration").mockRejectedValue(new APIError({ status: 409, code: "configuration_conflict", detail: "Configuration changed" }));
    renderPanel();
    const value = await screen.findByLabelText("Value");
    fireEvent.change(value, { target: { value: "keep-this-edit" } });
    fireEvent.click(screen.getByRole("button", { name: "Save configuration" }));
    fireEvent.click(await screen.findByRole("button", { name: "Reload latest configuration" }));
    expect(await screen.findByText(/could not reload the latest configuration/i)).not.toBeNull();
    expect((value as HTMLInputElement).value).toBe("keep-this-edit");
  });

  it("resets dirty configuration state when the application route changes", async () => {
    const configuration = vi.mocked(api.applicationConfiguration);
    configuration.mockReset();
    configuration.mockImplementation(async (appId) => appId === "app-a" ? {
      revisionId: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
      revisionNumber: 3,
      entries: [
        { key: "A_VARIABLE", sensitive: false, value: "a-value" },
        { key: "A_SECRET", sensitive: true },
      ],
    } : {
      revisionId: "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb",
      revisionNumber: 7,
      entries: [{ key: "B_VARIABLE", sensitive: false, value: "b-value" }],
    });
    const replace = vi.spyOn(api, "replaceApplicationConfiguration").mockResolvedValue({
      revisionId: "cccccccc-cccc-cccc-cccc-cccccccccccc",
      revisionNumber: 8,
      entries: [{ key: "B_VARIABLE", sensitive: false, value: "b-edited" }],
    });
    const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
    const view = render(<QueryClientProvider client={client}><ApplicationConfigurationPanel appId="app-a" /></QueryClientProvider>);

    fireEvent.change(await screen.findByDisplayValue("a-value"), { target: { value: "unsaved-a" } });
    fireEvent.change(screen.getByLabelText("Replacement value"), { target: { value: "a-secret-replacement" } });
    view.rerender(<QueryClientProvider client={client}><ApplicationConfigurationPanel appId="app-b" /></QueryClientProvider>);

    const bValue = await screen.findByDisplayValue("b-value");
    expect(screen.queryByDisplayValue("unsaved-a")).toBeNull();
    expect(screen.queryByDisplayValue("a-secret-replacement")).toBeNull();
    expect(screen.queryByDisplayValue("A_SECRET")).toBeNull();
    fireEvent.change(bValue, { target: { value: "b-edited" } });
    fireEvent.click(screen.getByRole("button", { name: "Save configuration" }));
    await waitFor(() => expect(replace).toHaveBeenCalledWith("app-b", {
      expectedRevisionNumber: 7,
      variables: [{ key: "B_VARIABLE", value: "b-edited" }],
      secrets: [],
      remove: [],
    }));
    expect(JSON.stringify(replace.mock.calls)).not.toContain("a-secret-replacement");
  });

  it("serializes saves and locks every edit until the submitted revision is applied", async () => {
    let resolveSave!: (configuration: typeof initial) => void;
    const pendingSave = new Promise<typeof initial>((resolve) => { resolveSave = resolve; });
    const replace = vi.spyOn(api, "replaceApplicationConfiguration").mockReturnValue(pendingSave);
    renderPanel();

    const value = await screen.findByLabelText("Value");
    fireEvent.change(value, { target: { value: "submitted-value" } });
    const save = screen.getByRole("button", { name: "Save configuration" });
    fireEvent.click(save);
    fireEvent.click(save);

    await waitFor(() => expect(replace).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(screen.getByRole("button", { name: /Saving/ }).hasAttribute("disabled")).toBe(true));
    for (const input of screen.getAllByRole("textbox")) expect(input.hasAttribute("disabled")).toBe(true);
    expect(screen.getByLabelText("Replacement value").hasAttribute("disabled")).toBe(true);
    for (const button of screen.getAllByRole("button")) expect(button.hasAttribute("disabled")).toBe(true);
    fireEvent.change(value, { target: { value: "must-not-apply" } });
    fireEvent.click(screen.getByRole("button", { name: "Add variable" }));
    expect((value as HTMLInputElement).value).toBe("submitted-value");
    expect(replace).toHaveBeenCalledTimes(1);

    await act(async () => resolveSave({
      ...initial,
      revisionNumber: 2,
      entries: [
        { key: "EMPTY", sensitive: false, value: "submitted-value" },
        { key: "TOKEN", sensitive: true },
      ],
    }));
    expect(await screen.findByText("Configuration revision 2 saved.")).not.toBeNull();
    expect((screen.getByLabelText("Value") as HTMLInputElement).value).toBe("submitted-value");
    expect(replace).toHaveBeenCalledTimes(1);
  });
});
