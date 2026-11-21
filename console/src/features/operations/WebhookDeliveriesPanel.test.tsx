import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  createWebhookSubscription,
  listWebhookDeliveries,
  listWebhookSubscriptions,
  replayWebhookDelivery,
} from "../../client";
import type { WebhookDelivery, WebhookSubscription } from "../../client";
import { PreferencesProvider } from "../../lib/preferences";
import { TestQueryProvider } from "../../test/queryClient";
import { WebhookDeliveriesPanel } from "./WebhookDeliveriesPanel";

vi.mock("../../client", () => ({
  createWebhookSubscription: vi.fn(),
  listWebhookDeliveries: vi.fn(),
  listWebhookSubscriptions: vi.fn(),
  replayWebhookDelivery: vi.fn(),
  updateWebhookSubscription: vi.fn(),
}));

const mockCreate = vi.mocked(createWebhookSubscription);
const mockListDeliveries = vi.mocked(listWebhookDeliveries);
const mockListSubscriptions = vi.mocked(listWebhookSubscriptions);
const mockReplay = vi.mocked(replayWebhookDelivery);

const subscription: WebhookSubscription = {
  id: "11111111-1111-4111-8111-111111111111",
  name: "Security automation",
  endpointUrl: "https://hooks.example.test/artifacts",
  eventTypes: ["artifact.quarantined", "artifact.released"],
  enabled: true,
  version: "1",
  secretConfigured: true,
  createdAt: "2026-08-12T01:00:00Z",
  updatedAt: "2026-08-12T01:00:00Z",
};

const deadDelivery: WebhookDelivery = {
  id: "22222222-2222-4222-8222-222222222222",
  eventId: "33333333-3333-4333-8333-333333333333",
  eventType: "artifact.quarantined",
  subscriptionId: subscription.id,
  subscriptionName: subscription.name,
  state: "dead",
  attempts: 8,
  lastStatus: 503,
  lastError: "receiver unavailable",
  createdAt: "2026-08-12T01:01:00Z",
  updatedAt: "2026-08-12T01:02:00Z",
};

const renderPanel = () =>
  render(
    <TestQueryProvider>
      <PreferencesProvider>
        <WebhookDeliveriesPanel />
      </PreferencesProvider>
    </TestQueryProvider>,
  );

// The mask is the dismissal route this dialog's `busy` flag controls, and it
// only closes on a click that both starts and lands on it.
function clickMask() {
  const mask = document.querySelector(".ant-modal-wrap");
  expect(mask).not.toBeNull();
  fireEvent.mouseDown(mask as HTMLElement);
  fireEvent.click(mask as HTMLElement);
}

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe("WebhookDeliveriesPanel", () => {
  it("names a delivery's subscription even when it is on another subscription page", async () => {
    mockListSubscriptions.mockResolvedValue({
      data: { items: [subscription], nextPageToken: "sub-2" },
    } as never);
    mockListDeliveries.mockResolvedValue({
      data: {
        items: [
          {
            ...deadDelivery,
            subscriptionId: "55555555-5555-4555-8555-555555555555",
            subscriptionName: "Release notifier",
          },
        ],
      },
    } as never);

    renderPanel();
    expect(await screen.findByText("Release notifier")).toBeInTheDocument();
    expect(
      screen.queryByText("55555555-5555-4555-8555-555555555555"),
    ).not.toBeInTheDocument();
  });

  it("continues the delivery log and the subscription list with their cursors", async () => {
    const user = userEvent.setup();
    const secondDelivery = {
      ...deadDelivery,
      id: "44444444-4444-4444-8444-444444444444",
      lastError: "second page failure",
    };
    mockListSubscriptions.mockResolvedValue({
      data: { items: [subscription] },
    } as never);
    mockListDeliveries.mockImplementation((async (options?: {
      query?: { pageToken?: string };
    }) =>
      options?.query?.pageToken === "del-2"
        ? { data: { items: [secondDelivery] } }
        : {
            data: { items: [deadDelivery], nextPageToken: "del-2" },
          }) as never);

    renderPanel();
    expect(await screen.findByText("receiver unavailable")).toBeInTheDocument();
    expect(mockListDeliveries.mock.calls[0]?.[0]?.query).toMatchObject({
      limit: 20,
      pageToken: undefined,
    });

    // The first pager is the subscription list (no next cursor), the second
    // one belongs to the delivery log.
    const next = await screen.findAllByRole("button", { name: "Next Page" });
    await user.click(next[next.length - 1]);

    expect(await screen.findByText("second page failure")).toBeInTheDocument();
    expect(mockListDeliveries).toHaveBeenLastCalledWith(
      expect.objectContaining({
        query: expect.objectContaining({ pageToken: "del-2" }),
      }),
    );
  });

  it("pages the subscription list by cursor", async () => {
    const user = userEvent.setup();
    const other = {
      ...subscription,
      id: "55555555-5555-4555-8555-555555555555",
      name: "Second page automation",
    };
    mockListSubscriptions.mockImplementation((async (options?: {
      query?: { pageToken?: string };
    }) =>
      options?.query?.pageToken === "sub-2"
        ? { data: { items: [other] } }
        : {
            data: { items: [subscription], nextPageToken: "sub-2" },
          }) as never);
    mockListDeliveries.mockResolvedValue({
      data: { items: [] },
    } as never);

    renderPanel();
    expect(
      (await screen.findAllByText("Security automation"))[0],
    ).toBeInTheDocument();
    const next = await screen.findAllByRole("button", { name: "Next Page" });
    await user.click(next[0]);

    expect(
      (await screen.findAllByText("Second page automation"))[0],
    ).toBeInTheDocument();
    expect(mockListSubscriptions).toHaveBeenLastCalledWith(
      expect.objectContaining({
        query: { limit: 100, pageToken: "sub-2" },
      }),
    );
  });

  it("shows delivery failures and explicitly replays dead deliveries", async () => {
    const user = userEvent.setup();
    mockListSubscriptions.mockResolvedValue({
      data: { items: [subscription] },
    } as never);
    mockListDeliveries.mockResolvedValue({
      data: { items: [deadDelivery] },
    } as never);
    mockReplay.mockResolvedValue({
      data: { ...deadDelivery, state: "pending", attempts: 0 },
    } as never);

    renderPanel();

    expect(
      (await screen.findAllByText("Security automation"))[0],
    ).toBeInTheDocument();
    expect(screen.getByText("receiver unavailable")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Replay delivery" }));

    expect(mockReplay).toHaveBeenCalledWith({
      path: { deliveryId: deadDelivery.id },
    });
    expect(mockListDeliveries).toHaveBeenCalledTimes(2);
  });

  it("creates a signed subscription with the supported event set", async () => {
    const user = userEvent.setup();
    mockListSubscriptions.mockResolvedValue({
      data: { items: [] },
    } as never);
    mockListDeliveries.mockResolvedValue({
      data: { items: [] },
    } as never);
    mockCreate.mockResolvedValue({ data: subscription } as never);

    renderPanel();
    await screen.findByText("No webhook subscriptions yet");
    await user.click(
      screen.getAllByRole("button", { name: "New subscription" })[0],
    );
    await user.type(
      screen.getByPlaceholderText("e.g. Security automation"),
      "Security automation",
    );
    await user.type(
      screen.getByPlaceholderText("https://hooks.example.com/artifacts"),
      "https://hooks.example.test/artifacts",
    );
    await user.type(
      screen.getByPlaceholderText("At least 32 characters"),
      "webhook-signing-secret-at-least-32-characters",
    );
    await user.click(within(screen.getByRole("dialog")).getByRole("combobox"));
    await user.click(await screen.findByText("artifact.promoted"));
    await user.click(screen.getByRole("button", { name: "Create" }));

    expect(mockCreate).toHaveBeenCalledWith({
      body: {
        name: "Security automation",
        endpointUrl: "https://hooks.example.test/artifacts",
        secret: "webhook-signing-secret-at-least-32-characters",
        eventTypes: [
          "artifact.quarantined",
          "artifact.released",
          "artifact.promoted",
        ],
        enabled: true,
      },
    });
  });

  it("keeps one primary action when the subscription list is empty", async () => {
    mockListSubscriptions.mockResolvedValue({
      data: { items: [] },
    } as never);
    mockListDeliveries.mockResolvedValue({
      data: { items: [] },
    } as never);

    renderPanel();

    expect(
      await screen.findByText("No webhook subscriptions yet"),
    ).toBeInTheDocument();
    expect(
      screen.getAllByRole("button", { name: "New subscription" }),
    ).toHaveLength(1);
  });

  it("holds the editor shut while saving and reopens dismissal after a failure", async () => {
    const user = userEvent.setup();
    mockListSubscriptions.mockResolvedValue({
      data: { items: [] },
    } as never);
    mockListDeliveries.mockResolvedValue({
      data: { items: [] },
    } as never);
    let rejectSave: (error: unknown) => void = () => {};
    mockCreate.mockImplementationOnce(
      () =>
        new Promise((_, reject) => {
          rejectSave = reject;
        }) as never,
    );

    renderPanel();
    await screen.findByText("No webhook subscriptions yet");
    await user.click(
      screen.getAllByRole("button", { name: "New subscription" })[0],
    );
    await user.type(
      screen.getByPlaceholderText("e.g. Security automation"),
      "Security automation",
    );
    await user.type(
      screen.getByPlaceholderText("https://hooks.example.com/artifacts"),
      "https://hooks.example.test/artifacts",
    );
    await user.type(
      screen.getByPlaceholderText("At least 32 characters"),
      "webhook-signing-secret-at-least-32-characters",
    );
    await user.click(screen.getByRole("button", { name: "Create" }));

    clickMask();
    expect(screen.getByRole("dialog")).toBeInTheDocument();

    rejectSave(new Error("network down"));
    // The panel and the dialog share one error state, so assert the dialog's.
    expect(
      await within(screen.getByRole("dialog")).findByText("network down"),
    ).toBeInTheDocument();

    clickMask();
    await waitFor(() =>
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
    );
  });

  it("frees the replay action when the request rejects", async () => {
    const user = userEvent.setup();
    mockListSubscriptions.mockResolvedValue({
      data: { items: [subscription] },
    } as never);
    mockListDeliveries.mockResolvedValue({
      data: { items: [deadDelivery] },
    } as never);
    mockReplay.mockRejectedValueOnce(new Error("network down"));

    renderPanel();
    const replayButton = await screen.findByRole("button", {
      name: "Replay delivery",
    });
    await user.click(replayButton);

    expect(await screen.findByText("network down")).toBeInTheDocument();
    // The rejection released `busyId`, so the button stops spinning.
    await waitFor(() =>
      expect(replayButton).not.toHaveClass("ant-btn-loading"),
    );
  });
});
