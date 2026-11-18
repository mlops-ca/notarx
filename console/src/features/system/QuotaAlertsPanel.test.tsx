import { TestQueryProvider } from "../../test/queryClient";
import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { CurrentIdentity, RepositoryQuotaAlertRule } from "../../client";
import { PreferencesProvider } from "../../lib/preferences";
import { QuotaAlertsPanel } from "./QuotaAlertsPanel";

const mocks = vi.hoisted(() => ({
  identity: {
    actor: "fixture-admin",
    kind: "local_session",
    administrator: true,
    role: "admin",
  } as CurrentIdentity,
  listRepositoryQuotaAlertRules: vi.fn(),
  listEmailTargets: vi.fn(),
  getEmailNotificationCapability: vi.fn(),
  listRepositories: vi.fn(),
  createRepositoryQuotaAlertRule: vi.fn(),
  updateRepositoryQuotaAlertRule: vi.fn(),
  deleteRepositoryQuotaAlertRule: vi.fn(),
  listRepositoryQuotaAlertEvents: vi.fn(),
  getEmailDelivery: vi.fn(),
}));
vi.mock("../../lib/auth", () => ({
  useAuth: () => ({ identity: mocks.identity }),
}));
vi.mock("../../client", () => mocks);

const policy = {
  warningBasisPoints: 8000,
  criticalBasisPoints: 9500,
  recoveryBelowBasisPoints: 7500,
  warningForSeconds: 120,
  criticalForSeconds: 30,
  recoveryForSeconds: 300,
  maxSampleAgeSeconds: 60,
};
const rule: RepositoryQuotaAlertRule = {
  id: "rule-a",
  repositoryId: "repo-a",
  targetId: "target-a",
  targetVersion: "target-v1",
  enabled: false,
  deleted: false,
  version: "rule-v1",
  stateVersion: "state-v2",
  policy,
  sequence: 1,
  state: {
    severity: "critical",
    phase: "firing",
    dataState: "stale",
    usedBytes: 0,
    quotaBytes: 10000,
    lastSampleAt: "2026-10-04T12:00:00Z",
  },
  createdAt: "2026-10-04T11:00:00Z",
  updatedAt: "2026-10-04T12:00:00Z",
};
const target = {
  id: "target-a",
  name: "Synthetic operations",
  locale: "zh-CN",
  enabled: true,
  recipientConfigured: true,
  version: "target-v1",
  createdAt: rule.createdAt,
  updatedAt: rule.updatedAt,
};

function mount() {
  return render(
    <TestQueryProvider>
      <PreferencesProvider>
        <QuotaAlertsPanel />
      </PreferencesProvider>
    </TestQueryProvider>,
  );
}
beforeEach(() => {
  vi.resetAllMocks();
  localStorage.clear();
  mocks.identity = {
    actor: "fixture-admin",
    kind: "local_session",
    administrator: true,
    role: "admin",
  };
  mocks.listRepositoryQuotaAlertRules.mockResolvedValue({ data: [rule] });
  mocks.listEmailTargets.mockResolvedValue({ data: [target] });
  mocks.getEmailNotificationCapability.mockResolvedValue({
    data: { enabled: false, reason: "email_disabled" },
  });
  mocks.listRepositories.mockResolvedValue({
    data: {
      items: [
        {
          id: "repo-a",
          name: "Synthetic Raw",
          format: "raw",
          state: "active",
          type: "hosted",
        },
      ],
    },
  });
  mocks.listRepositoryQuotaAlertEvents.mockResolvedValue({ data: [] });
});
afterEach(cleanup);

it("keeps a password-change denial across cancel/back and clears it only after an explicit successful refresh", async () => {
  mocks.getEmailNotificationCapability.mockResolvedValue({
    data: { enabled: true, reason: "ready" },
  });
  mocks.updateRepositoryQuotaAlertRule.mockResolvedValue({
    error: { code: "password_change_required" },
  });
  mount();
  await userEvent.click(
    await screen.findByRole("button", { name: "View Synthetic Raw" }),
  );
  await userEvent.click(screen.getByRole("button", { name: "Enable rule" }));
  await userEvent.click(screen.getByRole("button", { name: "Confirm enable" }));
  expect(
    await screen.findByText(
      /Change your password before returning to this page\./,
    ),
  ).toBeInTheDocument();
  await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
  expect(screen.getByRole("button", { name: "Edit rule" })).toBeDisabled();
  await userEvent.click(screen.getByRole("button", { name: "Back to rules" }));
  expect(screen.getByRole("button", { name: "Create rule" })).toBeDisabled();
  await userEvent.click(screen.getByRole("button", { name: "Refresh" }));
  await waitFor(() =>
    expect(screen.getByRole("button", { name: "Create rule" })).toBeEnabled(),
  );
  expect(mocks.updateRepositoryQuotaAlertRule).toHaveBeenCalledTimes(1);
});

it("keeps a loaded repository page and selection while refreshing alert rules", async () => {
  mocks.listRepositoryQuotaAlertRules.mockResolvedValue({ data: [] });
  mocks.listRepositories.mockImplementation(({ query }) =>
    Promise.resolve({
      data: query.pageToken
        ? {
            items: [
              {
                id: "repo-b",
                name: "Second page repository",
                state: "active",
                type: "hosted",
                format: "raw",
              },
            ],
          }
        : {
            items: [
              {
                id: "repo-a",
                name: "Synthetic Raw",
                state: "active",
                type: "hosted",
                format: "raw",
              },
            ],
            nextPageToken: "page-two",
          },
    }),
  );
  mount();
  await userEvent.click(
    await screen.findByRole("button", { name: "Create rule" }),
  );
  await userEvent.click(
    screen.getByRole("button", { name: "Load more repositories" }),
  );
  await userEvent.click(screen.getByRole("combobox", { name: "Repository" }));
  await userEvent.click(
    await screen.findByRole("option", { name: "Second page repository" }),
  );
  await userEvent.click(screen.getByRole("button", { name: "Refresh" }));
  await waitFor(() =>
    expect(mocks.listRepositoryQuotaAlertRules).toHaveBeenCalledTimes(2),
  );
  await waitFor(() =>
    expect(screen.getByRole("button", { name: "Refresh" })).not.toHaveClass(
      "ant-btn-loading",
    ),
  );
  await userEvent.click(screen.getByRole("combobox", { name: "Repository" }));
  expect(
    await screen.findByRole("option", { name: "Second page repository" }),
  ).toBeInTheDocument();
  expect(
    screen.getByRole("combobox", { name: "Repository" }).closest(".ant-select"),
  ).toHaveTextContent("Second page repository");
});

it("ignores an older read that resolves after a newer refresh", async () => {
  mount();
  await screen.findByText("Synthetic Raw");
  let finish:
    ((value: { data: RepositoryQuotaAlertRule[] }) => void) | undefined;
  mocks.listRepositoryQuotaAlertRules.mockImplementationOnce(
    () =>
      new Promise((resolve) => {
        finish = resolve;
      }),
  );
  await userEvent.click(screen.getByRole("button", { name: "Refresh" }));
  mocks.listRepositoryQuotaAlertRules.mockResolvedValue({ data: [] });
  await act(async () => {
    document.dispatchEvent(new Event("visibilitychange"));
  });
  expect(await screen.findByText("No quota alert rules")).toBeInTheDocument();
  await act(async () => {
    finish?.({ data: [rule] });
  });
  expect(screen.queryByText("Synthetic Raw")).not.toBeInTheDocument();
});

it("holds a root read authorization denial through automatic recovery until an explicit refresh", async () => {
  mount();
  await screen.findByText("Synthetic Raw");
  mocks.listRepositoryQuotaAlertRules.mockResolvedValue({
    error: { code: "access_denied" },
  });
  await userEvent.click(screen.getByRole("button", { name: "Refresh" }));
  await screen.findByText(/previously read data/);
  mocks.listRepositoryQuotaAlertRules.mockResolvedValue({ data: [rule] });
  await act(async () => {
    document.dispatchEvent(new Event("visibilitychange"));
  });
  await waitFor(() =>
    expect(screen.queryByText(/previously read data/)).not.toBeInTheDocument(),
  );
  expect(screen.getByRole("button", { name: "Create rule" })).toBeDisabled();
  await userEvent.click(screen.getByRole("button", { name: "Refresh" }));
  await waitFor(() =>
    expect(screen.getByRole("button", { name: "Create rule" })).toBeEnabled(),
  );
});

it("does not let a pending explicit refresh clear a newer event authorization denial", async () => {
  mount();
  await userEvent.click(
    await screen.findByRole("button", { name: "View Synthetic Raw" }),
  );
  await screen.findByText("No alert events yet");
  let finish:
    ((value: { data: RepositoryQuotaAlertRule[] }) => void) | undefined;
  mocks.listRepositoryQuotaAlertRules.mockImplementationOnce(
    () =>
      new Promise((resolve) => {
        finish = resolve;
      }),
  );
  await userEvent.click(screen.getByRole("button", { name: "Refresh" }));
  mocks.listRepositoryQuotaAlertEvents.mockResolvedValue({
    error: { code: "access_denied" },
  });
  await userEvent.click(screen.getByRole("button", { name: "Refresh events" }));
  await waitFor(() =>
    expect(screen.getByRole("button", { name: "Edit rule" })).toBeDisabled(),
  );
  await act(async () => {
    finish?.({ data: [rule] });
  });
  await waitFor(() =>
    expect(screen.getByRole("button", { name: "Refresh" })).not.toHaveClass(
      "ant-btn-loading",
    ),
  );
  expect(screen.getByRole("button", { name: "Edit rule" })).toBeDisabled();
  await userEvent.click(screen.getByRole("button", { name: "Refresh" }));
  await waitFor(() =>
    expect(screen.getByRole("button", { name: "Edit rule" })).toBeEnabled(),
  );
});

it("locks duplicate submissions and ignores a late mutation after leaving the panel", async () => {
  let finish: ((value: { data: RepositoryQuotaAlertRule }) => void) | undefined;
  mocks.updateRepositoryQuotaAlertRule.mockImplementation(
    () =>
      new Promise((resolve) => {
        finish = resolve;
      }),
  );
  const view = mount();
  await userEvent.click(
    await screen.findByRole("button", { name: "View Synthetic Raw" }),
  );
  await userEvent.click(screen.getByRole("button", { name: "Edit rule" }));
  const button = screen.getByRole("button", { name: "Save rule" });
  act(() => {
    fireEvent.click(button);
    fireEvent.click(button);
  });
  expect(mocks.updateRepositoryQuotaAlertRule).toHaveBeenCalledTimes(1);
  view.unmount();
  mocks.listRepositoryQuotaAlertRules.mockResolvedValue({ data: [] });
  mount();
  await screen.findByText("No quota alert rules");
  await act(async () => {
    finish?.({ data: rule });
  });
  expect(screen.queryByText("Rule saved")).not.toBeInTheDocument();
  expect(screen.getByText("No quota alert rules")).toBeInTheDocument();
});

describe("quota alerts read boundary", () => {
  it("shows retained severity separately from stale evidence and mail prerequisites", async () => {
    mount();
    expect(screen.getByRole("status")).toHaveAttribute("aria-busy", "true");
    expect(await screen.findByText("Synthetic Raw")).toBeInTheDocument();
    expect(screen.getByText("Critical")).toBeInTheDocument();
    expect(screen.getByText("Sample stale")).toBeInTheDocument();
    expect(screen.getByText(/Email channel disabled/)).toBeInTheDocument();
    expect(
      screen.getByText(
        /Observes logical repository usage, not the physical capacity of S3, NAS, or local mounts\./,
      ),
    ).toBeInTheDocument();
    expect(mocks.createRepositoryQuotaAlertRule).not.toHaveBeenCalled();
  });
  it("has one initial error state and never echoes unsafe error detail", async () => {
    mocks.listRepositoryQuotaAlertRules.mockRejectedValue(
      new Error("smtp-password-sensitive-marker"),
    );
    mount();
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Request failed",
    );
    expect(screen.queryByRole("status")).not.toBeInTheDocument();
    expect(screen.queryByText(/sensitive-marker/)).not.toBeInTheDocument();
  });
  it("retains last rows on refresh failure and marks them old", async () => {
    mount();
    await screen.findByText("Synthetic Raw");
    mocks.listRepositoryQuotaAlertRules.mockResolvedValue({
      error: { code: "internal_error", message: "unsafe" },
    });
    await userEvent.click(screen.getByRole("button", { name: "Refresh" }));
    expect(await screen.findByText(/previously read data/)).toBeInTheDocument();
    expect(screen.getByText("Synthetic Raw")).toBeInTheDocument();
  });
  it("does not request administrator data for a member", async () => {
    mocks.identity = {
      ...mocks.identity,
      administrator: false,
      role: "member",
    };
    mount();
    expect(
      screen.getByText("Only platform administrators can manage quota alerts"),
    ).toBeInTheDocument();
    await waitFor(() =>
      expect(mocks.listRepositoryQuotaAlertRules).not.toHaveBeenCalled(),
    );
  });
  it("shows empty rules independently from missing targets", async () => {
    mocks.listRepositoryQuotaAlertRules.mockResolvedValue({ data: [] });
    mocks.listEmailTargets.mockResolvedValue({ data: [] });
    mount();
    expect(await screen.findByText("No quota alert rules")).toBeInTheDocument();
    expect(
      screen.getByText(
        /Create a target in the System runtime Email notifications tab, then return here to configure a rule\./,
      ),
    ).toBeInTheDocument();
  });
});

async function fillPolicy() {
  for (const [label, value] of [
    ["Warning threshold (%)", "80.01"],
    ["Critical threshold (%)", "95"],
    ["Recovery threshold (%)", "75"],
    ["Warning hold duration (seconds)", "120"],
    ["Critical hold duration (seconds)", "30"],
    ["Recovery hold duration (seconds)", "300"],
    ["Maximum sample age (seconds)", "60"],
  ])
    await userEvent.type(screen.getByRole("textbox", { name: label }), value);
}
describe("quota alerts explicit writes", () => {
  it("starts with blank policy and creates an explicitly disabled rule with exact basis points", async () => {
    mocks.listRepositoryQuotaAlertRules.mockResolvedValue({ data: [] });
    mocks.createRepositoryQuotaAlertRule.mockResolvedValue({ data: rule });
    mount();
    await userEvent.click(
      await screen.findByRole("button", { name: "Create rule" }),
    );
    expect(
      screen.getByRole("textbox", { name: "Warning threshold (%)" }),
    ).toHaveValue("");
    await userEvent.click(screen.getByRole("combobox", { name: "Repository" }));
    await userEvent.click(
      await screen.findByRole("option", { name: "Synthetic Raw" }),
    );
    await userEvent.click(
      screen.getByRole("combobox", { name: "Email target" }),
    );
    await userEvent.click(
      await screen.findByRole("option", { name: /Synthetic operations/ }),
    );
    await fillPolicy();
    await userEvent.click(
      screen.getByRole("button", { name: "Create disabled rule" }),
    );
    await waitFor(() =>
      expect(mocks.createRepositoryQuotaAlertRule).toHaveBeenCalledTimes(1),
    );
    expect(mocks.createRepositoryQuotaAlertRule.mock.calls[0][0].body).toEqual({
      repositoryId: "repo-a",
      targetId: "target-a",
      enabled: false,
      policy: { ...policy, warningBasisPoints: 8001 },
    });
    expect(await screen.findByText("Rule saved")).toBeInTheDocument();
  });
  it("validates threshold ordering without silently rounding invalid precision", async () => {
    mocks.listRepositoryQuotaAlertRules.mockResolvedValue({ data: [] });
    mount();
    await userEvent.click(
      await screen.findByRole("button", { name: "Create rule" }),
    );
    await fillPolicy();
    await userEvent.type(
      screen.getByRole("textbox", { name: "Warning threshold (%)" }),
      "1",
    );
    await userEvent.click(
      screen.getByRole("button", { name: "Create disabled rule" }),
    );
    expect(
      screen.getByRole("textbox", { name: "Warning threshold (%)" }),
    ).toHaveAccessibleDescription(
      /Enter a percentage above 0 and at most 100, with up to two decimals\./,
    );
    expect(mocks.createRepositoryQuotaAlertRule).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(screen.queryByRole("textbox")).not.toBeInTheDocument();
  });
  it("preserves edit input after a CAS conflict and does not claim success", async () => {
    mocks.updateRepositoryQuotaAlertRule.mockResolvedValue({
      error: { code: "version_conflict", message: "smtp-sensitive-marker" },
    });
    mount();
    await userEvent.click(
      await screen.findByRole("button", { name: "View Synthetic Raw" }),
    );
    await userEvent.click(screen.getByRole("button", { name: "Edit rule" }));
    const field = screen.getByRole("textbox", {
      name: "Warning threshold (%)",
    });
    await userEvent.clear(field);
    await userEvent.type(field, "81.25");
    await userEvent.click(screen.getByRole("button", { name: "Save rule" }));
    expect(
      await screen.findByText(
        /Another administrator changed this rule\. Go back, refresh, and edit again; the newer configuration was not overwritten\./,
      ),
    ).toBeInTheDocument();
    expect(field).toHaveValue("81.25");
    expect(
      mocks.updateRepositoryQuotaAlertRule.mock.calls[0][0].headers,
    ).toEqual({ "If-Match": "rule-v1" });
    expect(screen.queryByText("Rule saved")).not.toBeInTheDocument();
    expect(screen.queryByText(/sensitive-marker/)).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Save rule" })).toBeDisabled();
  });
});

describe("quota alert evidence and control", () => {
  it("reads events and accepted delivery details without offering send or replay", async () => {
    mocks.listRepositoryQuotaAlertEvents.mockResolvedValue({
      data: [
        {
          id: "event-a",
          ruleId: "rule-a",
          ruleVersion: "rule-v1",
          repositoryId: "repo-a",
          repositoryName: "Synthetic Raw",
          episodeId: "episode-a",
          sequence: 1,
          scenario: "critical",
          usedBytes: 9900,
          quotaBytes: 10000,
          policy,
          occurredAt: rule.updatedAt,
          sampleAt: rule.updatedAt,
          evidenceSince: rule.createdAt,
          targetId: "target-a",
          targetVersion: "target-v1",
          notificationCode: "queued",
          templateVersion: "quota-1",
          deliveryId: "delivery-a",
          deliveryState: "accepted",
        },
      ],
    });
    mocks.getEmailDelivery.mockResolvedValue({
      data: {
        id: "delivery-a",
        eventId: "event-a",
        targetId: "target-a",
        targetVersion: "target-v1",
        scenario: "critical",
        locale: "zh-CN",
        templateVersion: "quota-1",
        kind: "repository_quota",
        state: "accepted",
        attempts: 2,
        possibleDuplicate: true,
        version: "delivery-v2",
        acceptedAt: rule.updatedAt,
        createdAt: rule.createdAt,
        updatedAt: rule.updatedAt,
      },
    });
    mount();
    await userEvent.click(
      await screen.findByRole("button", { name: "View Synthetic Raw" }),
    );
    expect(
      await screen.findByText("Accepted by SMTP server"),
    ).toBeInTheDocument();
    await userEvent.click(
      screen.getByRole("button", { name: "View delivery details" }),
    );
    expect(await screen.findByText(/Possible duplicate/)).toBeInTheDocument();
    expect(
      screen.getByText(
        /SMTP accepted only means the server accepted the message; it does not confirm inbox delivery\./,
      ),
    ).toBeInTheDocument();
    expect(screen.getByText(/Historical sample/)).toHaveTextContent("0 B");
    expect(
      screen.queryByRole("button", { name: /Send|Replay/ }),
    ).not.toBeInTheDocument();
  });
  it("cannot enable when email encryption is unavailable", async () => {
    mocks.getEmailNotificationCapability.mockResolvedValue({
      data: { enabled: false, reason: "encryption_key_unavailable" },
    });
    mount();
    await userEvent.click(
      await screen.findByRole("button", { name: "View Synthetic Raw" }),
    );
    expect(screen.getByRole("button", { name: "Enable rule" })).toBeDisabled();
    expect(mocks.updateRepositoryQuotaAlertRule).not.toHaveBeenCalled();
  });
  it("requires purpose and target confirmation before enabling and uses CAS", async () => {
    mocks.getEmailNotificationCapability.mockResolvedValue({
      data: { enabled: true, reason: "ready" },
    });
    mocks.updateRepositoryQuotaAlertRule.mockResolvedValue({
      data: { ...rule, enabled: true, version: "rule-v2" },
    });
    mount();
    await userEvent.click(
      await screen.findByRole("button", { name: "View Synthetic Raw" }),
    );
    await userEvent.click(screen.getByRole("button", { name: "Enable rule" }));
    expect(
      screen.getByText(
        /Enabling may send email to this target for future real quota events\. Previously unqueued events are not caught up, and no test email is sent\./,
      ),
    ).toBeInTheDocument();
    expect(
      screen.getAllByText(/Synthetic operations/, { selector: "p" }),
    ).toHaveLength(2);
    expect(mocks.updateRepositoryQuotaAlertRule).not.toHaveBeenCalled();
    await userEvent.click(
      screen.getByRole("button", { name: "Confirm enable" }),
    );
    await waitFor(() =>
      expect(mocks.updateRepositoryQuotaAlertRule).toHaveBeenCalledTimes(1),
    );
    expect(mocks.updateRepositoryQuotaAlertRule.mock.calls[0][0]).toMatchObject(
      { headers: { "If-Match": "rule-v1" }, body: { enabled: true } },
    );
  });
  it("cancelled delete leaves the rule unchanged and a confirmed delete retains history", async () => {
    mocks.deleteRepositoryQuotaAlertRule.mockResolvedValue({
      response: { status: 204 },
    });
    mount();
    await userEvent.click(
      await screen.findByRole("button", { name: "View Synthetic Raw" }),
    );
    await userEvent.click(screen.getByRole("button", { name: "Delete rule" }));
    expect(
      screen.getByText(
        /Deletion stops new evaluations and retains history without generating a recovery event\. Unclaimed automatic deliveries are cancelled; a claimed delivery may finish once\./,
      ),
    ).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(mocks.deleteRepositoryQuotaAlertRule).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("button", { name: "Delete rule" }));
    await userEvent.click(
      screen.getByRole("button", { name: "Confirm delete" }),
    );
    await waitFor(() =>
      expect(mocks.deleteRepositoryQuotaAlertRule).toHaveBeenCalledTimes(1),
    );
    expect(
      mocks.deleteRepositoryQuotaAlertRule.mock.calls[0][0].headers,
    ).toEqual({ "If-Match": "rule-v1" });
  });
});

it.each(["en-CA"])(
  "associates quota errors with controls and focuses the first selection in %s",
  async (locale) => {
    localStorage.setItem("ag.console.locale", locale);
    mount();
    await userEvent.click(
      await screen.findByRole("button", {
        name: "Create rule",
      }),
    );
    await userEvent.click(
      screen.getByRole("button", {
        name: "Create disabled rule",
      }),
    );
    const repository = screen.getByRole("combobox", {
      name: "Repository",
    });
    expect(repository).toHaveFocus();
    expect(repository).toHaveAttribute("aria-invalid", "true");
    expect(repository).toHaveAccessibleDescription(/Choose a repository/);
    const duration = screen.getByRole("textbox", {
      name: "Warning hold duration (seconds)",
    });
    expect(duration).toHaveAttribute("aria-invalid", "true");
    expect(duration).toHaveAccessibleDescription(/1–86400/);
    await userEvent.type(duration, "120");
    expect(duration).not.toHaveAttribute("aria-invalid", "true");
    expect(mocks.createRepositoryQuotaAlertRule).not.toHaveBeenCalled();
  },
);

it.each([
  [
    "Warning threshold (%)",
    "80.001",
    /Enter a percentage above 0 and at most 100, with up to two decimals\./,
  ],
  [
    "Critical threshold (%)",
    "79",
    /Warning must be below the critical threshold\./,
  ],
  [
    "Recovery threshold (%)",
    "80",
    /Recovery must be below the warning threshold\./,
  ],
  ["Warning hold duration (seconds)", "0", /1–86400/],
  ["Maximum sample age (seconds)", "29", /30–3600/],
])(
  "reports the %s constraint at the related field without saving",
  async (label, value, description) => {
    mount();
    await userEvent.click(
      await screen.findByRole("button", { name: "View Synthetic Raw" }),
    );
    await userEvent.click(screen.getByRole("button", { name: "Edit rule" }));
    const field = screen.getByRole("textbox", { name: label });
    await userEvent.clear(field);
    await userEvent.type(field, value);
    await userEvent.click(screen.getByRole("button", { name: "Save rule" }));
    expect(field).toHaveAttribute("aria-invalid", "true");
    expect(field).toHaveAccessibleDescription(description);
    expect(mocks.updateRepositoryQuotaAlertRule).not.toHaveBeenCalled();
  },
);

it("reports a failed repository page in the editor and reads it again on request", async () => {
  mocks.listRepositoryQuotaAlertRules.mockResolvedValue({ data: [] });
  let failPage = true;
  mocks.listRepositories.mockImplementation(({ query }) =>
    Promise.resolve(
      query.pageToken
        ? failPage
          ? { error: { status: 503, code: "unavailable" } }
          : {
              data: {
                items: [
                  {
                    id: "repo-b",
                    name: "Second page repository",
                    state: "active",
                    type: "hosted",
                    format: "raw",
                  },
                ],
              },
            }
        : {
            data: {
              items: [
                {
                  id: "repo-a",
                  name: "Synthetic Raw",
                  state: "active",
                  type: "hosted",
                  format: "raw",
                },
              ],
              nextPageToken: "page-two",
            },
          },
    ),
  );
  mount();
  await userEvent.click(
    await screen.findByRole("button", { name: "Create rule" }),
  );
  await userEvent.click(
    screen.getByRole("button", { name: "Load more repositories" }),
  );
  // The failure is local to the editor; the panel stays writable.
  await waitFor(() =>
    expect(
      mocks.listRepositories.mock.calls.filter(
        ([options]) => options.query.pageToken === "page-two",
      ),
    ).toHaveLength(1),
  );
  expect(
    await screen.findByText(
      "Request failed. Check the connection and refresh before retrying.",
    ),
  ).toBeInTheDocument();
  failPage = false;
  await userEvent.click(
    screen.getByRole("button", { name: "Load more repositories" }),
  );
  await userEvent.click(screen.getByRole("combobox", { name: "Repository" }));
  expect(
    await screen.findByRole("option", { name: "Second page repository" }),
  ).toBeInTheDocument();
});

it("keeps the editor's loaded catalogue, selection and draft across a panel refresh", async () => {
  mocks.listRepositoryQuotaAlertRules.mockResolvedValue({ data: [] });
  const repository = (id: string, name: string) => ({
    id,
    name,
    state: "active",
    type: "hosted",
    format: "raw",
  });
  let refreshed = false;
  mocks.listRepositories.mockImplementation(({ query }) =>
    Promise.resolve({
      data: query.pageToken
        ? {
            items: [repository("repo-b", "Second page repository")],
            nextPageToken: "page-three",
          }
        : {
            items: [repository("repo-a", "Synthetic Raw")],
            nextPageToken: refreshed ? "new-page-two" : "page-two",
          },
    }),
  );
  mocks.createRepositoryQuotaAlertRule.mockResolvedValue({ data: rule });
  mount();
  await userEvent.click(
    await screen.findByRole("button", { name: "Create rule" }),
  );
  await userEvent.click(
    screen.getByRole("button", { name: "Load more repositories" }),
  );
  const picker = screen.getByRole("combobox", { name: "Repository" });
  await userEvent.click(picker);
  await userEvent.click(
    await screen.findByRole("option", { name: "Second page repository" }),
  );
  await userEvent.click(screen.getByRole("combobox", { name: "Email target" }));
  await userEvent.click(
    await screen.findByRole("option", { name: /Synthetic operations/ }),
  );
  await fillPolicy();
  refreshed = true;
  await userEvent.click(screen.getByRole("button", { name: "Refresh" }));
  await waitFor(() =>
    expect(
      screen.getByRole("button", { name: "Create disabled rule" }),
    ).toBeEnabled(),
  );
  expect(picker.closest(".ant-select")).toHaveTextContent(
    "Second page repository",
  );
  expect(
    screen.getByRole("textbox", { name: "Warning threshold (%)" }),
  ).toHaveValue("80.01");
  await userEvent.click(
    screen.getByRole("button", { name: "Load more repositories" }),
  );
  await waitFor(() =>
    expect(mocks.listRepositories).toHaveBeenLastCalledWith(
      expect.objectContaining({
        query: { pageSize: 200, pageToken: "page-three" },
      }),
    ),
  );
  await userEvent.click(
    screen.getByRole("button", { name: "Create disabled rule" }),
  );
  await waitFor(() =>
    expect(mocks.createRepositoryQuotaAlertRule).toHaveBeenCalledTimes(1),
  );
  expect(mocks.createRepositoryQuotaAlertRule.mock.calls[0][0].body).toEqual({
    repositoryId: "repo-b",
    targetId: "target-a",
    enabled: false,
    policy: { ...policy, warningBasisPoints: 8001 },
  });
});
