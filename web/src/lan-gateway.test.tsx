import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { api } from "./api";
import { LANGatewayPanel } from "./lan-gateway";

const candidate = { interfaceId: "nic-1", name: "Ethernet", selectedIpv4: "192.168.50.4", prefix: "24" };
const profile = { expectedRevisionNumber: 4, candidates: [candidate] };
const upgrade = { observed: { availability: "unknown" } };

function renderPanel(role = "administrator") {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  const view = render(<QueryClientProvider client={client}><LANGatewayPanel role={role}/></QueryClientProvider>);
  return { client, view };
}

describe("LANGatewayPanel", () => {
  afterEach(() => { cleanup(); vi.restoreAllMocks(); });

  it("reviews the server-issued digest before saving a desired profile", async () => {
    const digest = "a".repeat(64);
    const profileRead = vi.spyOn(api, "lanGatewayProfile")
      .mockResolvedValueOnce(profile as never)
      .mockResolvedValueOnce({ ...profile, proposal: { spec: { interfaceId: candidate.interfaceId, selectedIpv4: candidate.selectedIpv4, portStart: 8100, portEnd: 8119 }, approvalDigest: digest } } as never)
      .mockResolvedValue(profile as never);
    vi.spyOn(api, "lanGatewayUpgrade").mockResolvedValue(upgrade as never);
    const configure = vi.spyOn(api, "configureLANGatewayProfile").mockResolvedValue({ created: true, profile: { revisionNumber: 5 } } as never);

    renderPanel();
    await screen.findByLabelText("Interface and IPv4 address");
    fireEvent.click(screen.getByRole("button", { name: "Review LAN gateway profile" }));

    const dialog = await screen.findByRole("dialog", { name: "Review LAN gateway profile" });
    expect(dialog.textContent).toContain("New revision 5");
    expect(dialog.textContent).toContain("nic-1");
    expect(dialog.textContent).toContain("192.168.50.4");
    expect(dialog.textContent).toContain("8100–8119");
    expect(dialog.textContent).toContain(digest);
    expect(screen.queryByRole("link", { name: /open/i })).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: "Approve desired profile" }));
    await waitFor(() => expect(configure).toHaveBeenCalledWith(expect.objectContaining({
      expectedRevisionNumber: 4,
      approvalDigest: digest,
      spec: { interfaceId: "nic-1", selectedIpv4: "192.168.50.4", portStart: 8100, portEnd: 8119 },
      operationId: expect.any(String),
    })));
    expect(profileRead).toHaveBeenNthCalledWith(2, { interfaceId: "nic-1", selectedIpv4: "192.168.50.4", portStart: 8100, portEnd: 8119 });
  });

  it("focuses a failed profile approval inside its exact review dialog", async () => {
    const spec = { interfaceId: candidate.interfaceId, selectedIpv4: candidate.selectedIpv4, portStart: 8100, portEnd: 8119 };
    vi.spyOn(api, "lanGatewayProfile").mockResolvedValueOnce(profile as never).mockResolvedValue({ ...profile, proposal: { spec, approvalDigest: "a".repeat(64) } } as never);
    vi.spyOn(api, "lanGatewayUpgrade").mockResolvedValue(upgrade as never);
    vi.spyOn(api, "configureLANGatewayProfile").mockRejectedValue(new Error("profile changed"));

    renderPanel();
    fireEvent.click(await screen.findByRole("button", { name: "Review LAN gateway profile" }));
    await screen.findByRole("dialog", { name: "Review LAN gateway profile" });
    expect(document.activeElement).toBe(screen.getByRole("heading", { name: "Review LAN gateway profile" }));
    fireEvent.click(screen.getByRole("button", { name: "Approve desired profile" }));
    const error = await screen.findByRole("alert");
    await waitFor(() => expect(document.activeElement).toBe(error));
  });

  it("does not issue administrator-only reads for viewers", () => {
    const profileRead = vi.spyOn(api, "lanGatewayProfile");
    const upgradeRead = vi.spyOn(api, "lanGatewayUpgrade");

    renderPanel("viewer");

    expect(screen.getByText("LAN gateway details and controls require an administrator.")).not.toBeNull();
    expect(profileRead).not.toHaveBeenCalled();
    expect(upgradeRead).not.toHaveBeenCalled();
  });

  it("associates the port-pool error with both invalid port fields", async () => {
    vi.spyOn(api, "lanGatewayProfile").mockResolvedValue(profile as never);
    vi.spyOn(api, "lanGatewayUpgrade").mockResolvedValue(upgrade as never);

    renderPanel();
    const firstPort = await screen.findByLabelText("First port");
    const lastPort = screen.getByLabelText("Last port");
    fireEvent.change(firstPort, { target: { value: "8119" } });
    fireEvent.change(lastPort, { target: { value: "8100" } });

    const error = screen.getByRole("alert");
    expect(firstPort.getAttribute("aria-invalid")).toBe("true");
    expect(lastPort.getAttribute("aria-invalid")).toBe("true");
    expect(firstPort.getAttribute("aria-describedby")).toBe(error.id);
    expect(lastPort.getAttribute("aria-describedby")).toBe(error.id);
  });

  it("announces gateway refresh completion", async () => {
    vi.spyOn(api, "lanGatewayProfile").mockResolvedValue(profile as never);
    vi.spyOn(api, "lanGatewayUpgrade").mockResolvedValue(upgrade as never);

    renderPanel();
    await screen.findByLabelText("Interface and IPv4 address");
    fireEvent.click(screen.getByRole("button", { name: "Check gateway state" }));

    await waitFor(() => expect(screen.getByRole("status").textContent).toBe("LAN gateway state refreshed."));
  });

  it.each([
    ["committed", "serving", "Gateway upgrade committed and observed serving"],
    ["committed", "unknown", "Gateway upgrade committed; live state needs attention"],
    ["prepared", "unknown", "Gateway upgrade is in progress"],
    ["serving", "unknown", "Gateway upgrade is in progress"],
    ["unresolved", "unknown", "Gateway upgrade recovery required"],
  ])("shows the %s gateway claim and suppresses a competing review", async (state, availability, guidance) => {
    const configured = {
      ...profile,
      desiredProfile: { id: "11111111-1111-4111-8111-111111111111", revisionNumber: 4, spec: { interfaceId: candidate.interfaceId, selectedIpv4: candidate.selectedIpv4, portStart: 8100, portEnd: 8119 } },
    };
    const operationId = "22222222-2222-4222-8222-222222222222";
    vi.spyOn(api, "lanGatewayProfile").mockResolvedValue(configured as never);
    vi.spyOn(api, "lanGatewayUpgrade").mockResolvedValue({
      observed: { availability, operationId },
      desiredClaim: { operationId, profileRevisionId: configured.desiredProfile.id, profileRevisionNumber: 4, profileSpecDigest: "a".repeat(64), state, updatedAt: "2026-10-01T00:00:00Z" },
      proposal: { profileRevisionId: configured.desiredProfile.id, profileRevisionNumber: 4, actionDigest: "b".repeat(64) },
    } as never);

    renderPanel();

    expect(await screen.findByText(guidance)).not.toBeNull();
    expect(screen.getByText("Desired gateway state").nextElementSibling?.textContent).toBe(state);
    expect(screen.getByText("Observed gateway state").nextElementSibling?.textContent).toBe(availability);
    expect(screen.getByText("Desired operation").nextElementSibling?.textContent).toBe(operationId);
    expect(screen.queryByRole("button", { name: "Review gateway upgrade" })).toBeNull();
  });

  it("withholds a stale proposal that does not match the current desired profile", async () => {
    const configured = {
      ...profile,
      desiredProfile: { id: "11111111-1111-4111-8111-111111111111", revisionNumber: 5, spec: { interfaceId: candidate.interfaceId, selectedIpv4: candidate.selectedIpv4, portStart: 8100, portEnd: 8119 } },
    };
    vi.spyOn(api, "lanGatewayProfile").mockResolvedValue(configured as never);
    vi.spyOn(api, "lanGatewayUpgrade").mockResolvedValue({
      observed: { availability: "unknown" },
      proposal: { profileRevisionId: configured.desiredProfile.id, profileRevisionNumber: 4, actionDigest: "a".repeat(64) },
    } as never);

    renderPanel();

    expect(await screen.findByText(/does not match the current desired profile/i)).not.toBeNull();
    expect(screen.queryByRole("button", { name: "Review gateway upgrade" })).toBeNull();
  });

  it("expires an open gateway review when the desired profile changes", async () => {
    const initialProfile = {
      ...profile,
      desiredProfile: { id: "11111111-1111-4111-8111-111111111111", revisionNumber: 4, spec: { interfaceId: candidate.interfaceId, selectedIpv4: candidate.selectedIpv4, portStart: 8100, portEnd: 8119 } },
    };
    const proposal = { profileRevisionId: initialProfile.desiredProfile.id, profileRevisionNumber: 4, actionDigest: "a".repeat(64) };
    vi.spyOn(api, "lanGatewayProfile").mockResolvedValue(initialProfile as never);
    vi.spyOn(api, "lanGatewayUpgrade").mockResolvedValue({ observed: { availability: "unknown" }, proposal } as never);
    const upgrade = vi.spyOn(api, "upgradeLANGateway");

    const { client } = renderPanel();
    fireEvent.click(await screen.findByRole("button", { name: "Review gateway upgrade" }));
    await screen.findByRole("dialog", { name: "Review LAN gateway upgrade" });
    act(() => client.setQueryData(["lan-gateway-profile"], {
      ...initialProfile,
      desiredProfile: { ...initialProfile.desiredProfile, revisionNumber: 5 },
    }));

    expect(await screen.findByText("Gateway review expired")).not.toBeNull();
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole("alert")));
    expect(screen.queryByRole("button", { name: "Approve gateway upgrade" })).toBeNull();
    expect(upgrade).not.toHaveBeenCalled();
  });

  it("focuses a failed gateway upgrade inside its exact review dialog", async () => {
    const configured = { ...profile, desiredProfile: { id: "11111111-1111-4111-8111-111111111111", revisionNumber: 4, spec: { interfaceId: candidate.interfaceId, selectedIpv4: candidate.selectedIpv4, portStart: 8100, portEnd: 8119 } } };
    const proposal = { profileRevisionId: configured.desiredProfile.id, profileRevisionNumber: 4, actionDigest: "a".repeat(64) };
    vi.spyOn(api, "lanGatewayProfile").mockResolvedValue(configured as never);
    vi.spyOn(api, "lanGatewayUpgrade").mockResolvedValue({ observed: { availability: "unknown" }, proposal } as never);
    vi.spyOn(api, "upgradeLANGateway").mockRejectedValue(new Error("gateway unavailable"));

    renderPanel();
    fireEvent.click(await screen.findByRole("button", { name: "Review gateway upgrade" }));
    await screen.findByRole("dialog", { name: "Review LAN gateway upgrade" });
    expect(document.activeElement).toBe(screen.getByRole("heading", { name: "Review LAN gateway upgrade" }));
    fireEvent.click(screen.getByRole("button", { name: "Approve gateway upgrade" }));
    const error = await screen.findByRole("alert");
    await waitFor(() => expect(document.activeElement).toBe(error));
  });

  it("moves focus to the resulting gateway guidance after a successful upgrade", async () => {
    const configured = {
      ...profile,
      desiredProfile: { id: "11111111-1111-4111-8111-111111111111", revisionNumber: 4, spec: { interfaceId: candidate.interfaceId, selectedIpv4: candidate.selectedIpv4, portStart: 8100, portEnd: 8119 } },
    };
    const proposal = { profileRevisionId: configured.desiredProfile.id, profileRevisionNumber: 4, actionDigest: "a".repeat(64) };
    const operationId = "22222222-2222-4222-8222-222222222222";
    vi.spyOn(api, "lanGatewayProfile").mockResolvedValue(configured as never);
    vi.spyOn(api, "lanGatewayUpgrade")
      .mockResolvedValueOnce({ observed: { availability: "unknown" }, proposal } as never)
      .mockResolvedValue({
        observed: { availability: "serving", operationId },
        desiredClaim: { operationId, profileRevisionId: proposal.profileRevisionId, profileRevisionNumber: proposal.profileRevisionNumber, profileSpecDigest: "b".repeat(64), state: "committed", updatedAt: "2026-10-01T00:00:00Z" },
        proposal,
      } as never);
    vi.spyOn(api, "upgradeLANGateway").mockResolvedValue({
      created: true,
      claim: { operationId, profileRevisionId: proposal.profileRevisionId, profileRevisionNumber: proposal.profileRevisionNumber, profileSpecDigest: "b".repeat(64), state: "committed", updatedAt: "2026-10-01T00:00:00Z" },
    } as never);

    renderPanel();
    fireEvent.click(await screen.findByRole("button", { name: "Review gateway upgrade" }));
    fireEvent.click(screen.getByRole("button", { name: "Approve gateway upgrade" }));

    const guidance = await screen.findByText("Gateway upgrade committed and observed serving");
    const guidanceTarget = guidance.closest('[role="status"]');
    await waitFor(() => expect(document.activeElement).toBe(guidanceTarget));
  });
});
