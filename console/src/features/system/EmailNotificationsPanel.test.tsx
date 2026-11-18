import { TestQueryProvider } from "../../test/queryClient";
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import type { CurrentIdentity, EmailTarget } from "../../client";
import { PreferencesProvider } from "../../lib/preferences";
import { EmailNotificationsPanel } from "./EmailNotificationsPanel";

const mocks = vi.hoisted(() => ({
  identity: {
    actor: "synthetic-admin",
    kind: "local_session",
    administrator: true,
    role: "admin",
  } as CurrentIdentity,
  listEmailTargets: vi.fn(),
  getEmailNotificationCapability: vi.fn(),
  listEmailDeliveries: vi.fn(),
  getEmailTarget: vi.fn(),
  createEmailTarget: vi.fn(),
  updateEmailTarget: vi.fn(),
  previewEmailNotification: vi.fn(),
  testEmailNotification: vi.fn(),
}));
vi.mock("../../lib/auth", () => ({
  useAuth: () => ({ identity: mocks.identity }),
}));
vi.mock("../../client", () => mocks);
const target: EmailTarget = {
  id: "target-a",
  name: "Synthetic operations",
  locale: "zh-CN",
  enabled: true,
  recipientConfigured: true,
  version: "v1",
  createdAt: "2026-01-01T12:00:00Z",
  updatedAt: "2026-01-01T12:00:00Z",
};
function mount() {
  return render(
    <TestQueryProvider>
      <PreferencesProvider>
        <EmailNotificationsPanel />
      </PreferencesProvider>
    </TestQueryProvider>,
  );
}
beforeEach(() => {
  vi.resetAllMocks();
  localStorage.clear();
  mocks.identity.administrator = true;
  mocks.listEmailTargets.mockResolvedValue({ data: [target] });
  mocks.getEmailNotificationCapability.mockResolvedValue({
    data: { enabled: true, reason: "ready" },
  });
  mocks.listEmailDeliveries.mockResolvedValue({ data: [] });
  mocks.previewEmailNotification.mockResolvedValue({
    data: {
      subject: "Synthetic preview",
      html: "<p>Synthetic</p>",
      text: "Synthetic",
      templateVersion: "2",
    },
  });
});
afterEach(cleanup);
it("rejects non-administrators before reading email resources", () => {
  mocks.identity.administrator = false;
  mount();
  expect(screen.getByText(/Only platform administrators/)).toBeInTheDocument();
  expect(mocks.listEmailTargets).not.toHaveBeenCalled();
});
it("creates disabled without sending and clears the sensitive value on cancel", async () => {
  mocks.createEmailTarget.mockResolvedValue({
    data: { ...target, enabled: false },
  });
  mount();
  await userEvent.click(
    await screen.findByRole("button", { name: "New email target" }),
  );
  await userEvent.type(screen.getByLabelText("Target name"), "Synthetic");
  await userEvent.type(
    screen.getByLabelText("Recipient address"),
    "a@example.test",
  );
  await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
  await userEvent.click(
    screen.getByRole("button", { name: "New email target" }),
  );
  expect(screen.getByLabelText("Recipient address")).toHaveValue("");
  await userEvent.type(screen.getByLabelText("Target name"), "Synthetic");
  await userEvent.type(
    screen.getByLabelText("Recipient address"),
    "a@example.test",
  );
  await userEvent.click(
    screen.getByRole("button", { name: "Save disabled target" }),
  );
  await waitFor(() => expect(mocks.createEmailTarget).toHaveBeenCalledTimes(1));
  expect(mocks.createEmailTarget.mock.calls[0][0].body).toEqual({
    name: "Synthetic",
    recipient: "a@example.test",
    locale: "en-CA",
    enabled: false,
  });
  expect(mocks.testEmailNotification).not.toHaveBeenCalled();
  expect(JSON.stringify(localStorage)).not.toContain("a@example.test");
});
it("omits the write-only recipient on edit and uses the target version", async () => {
  mocks.updateEmailTarget.mockResolvedValue({
    data: { ...target, version: "v2" },
  });
  mount();
  await userEvent.click(
    await screen.findByRole("button", { name: "Edit Synthetic operations" }),
  );
  expect(screen.getByLabelText("Recipient address")).toHaveValue("");
  await userEvent.click(screen.getByRole("button", { name: "Save target" }));
  await waitFor(() => expect(mocks.updateEmailTarget).toHaveBeenCalledTimes(1));
  expect(mocks.updateEmailTarget.mock.calls[0][0]).toMatchObject({
    headers: { "If-Match": "v1" },
    body: { name: target.name, locale: "zh-CN", enabled: true },
  });
  expect(mocks.updateEmailTarget.mock.calls[0][0].body).not.toHaveProperty(
    "recipient",
  );
});
it("preserves only the safe conflict draft and requires refresh to re-edit", async () => {
  mocks.updateEmailTarget.mockResolvedValue({
    error: { code: "version_conflict", message: "private-raw" },
  });
  mount();
  await userEvent.click(
    await screen.findByRole("button", { name: "Edit Synthetic operations" }),
  );
  await userEvent.type(
    screen.getByLabelText("Recipient address"),
    "private@example.test",
  );
  await userEvent.click(screen.getByRole("button", { name: "Save target" }));
  expect(
    await screen.findByText(/Target settings changed/),
  ).toBeInTheDocument();
  expect(screen.getByLabelText("Recipient address")).toHaveValue("");
  expect(screen.getByRole("button", { name: "Save target" })).toBeDisabled();
  expect(screen.queryByText("private-raw")).not.toBeInTheDocument();
  mocks.getEmailTarget.mockResolvedValue({
    data: { ...target, version: "v2" },
  });
  await userEvent.click(
    screen.getByRole("button", {
      name: "Refresh latest target and edit again",
    }),
  );
  await waitFor(() =>
    expect(screen.getByRole("button", { name: "Save target" })).toBeEnabled(),
  );
  expect(screen.getByLabelText("Target name")).toHaveValue(target.name);
});
it("blocks the whole workspace after a password-change error", async () => {
  mocks.updateEmailTarget.mockResolvedValue({
    error: { code: "password_change_required" },
  });
  mount();
  await userEvent.click(
    await screen.findByRole("button", { name: "Edit Synthetic operations" }),
  );
  await userEvent.type(
    screen.getByLabelText("Recipient address"),
    "private@example.test",
  );
  await userEvent.click(screen.getByRole("button", { name: "Save target" }));
  expect(
    await screen.findByText(
      /Change your password before returning to this page\./,
    ),
  ).toBeInTheDocument();
  expect(screen.queryByLabelText("Recipient address")).not.toBeInTheDocument();
  expect(
    screen.queryByRole("button", { name: "Preview template" }),
  ).not.toBeInTheDocument();
});
it.each(["test", "enable", "disable"])(
  "keeps %s confirmation consequences visible until cancel or submission",
  async (kind) => {
    mocks.listEmailTargets.mockResolvedValue({
      data: [{ ...target, enabled: kind !== "enable" }],
    });
    mount();
    const action =
      kind === "test" ? "Test" : kind === "enable" ? "Enable" : "Disable";
    await userEvent.click(
      await screen.findByRole("button", {
        name: `${action} Synthetic operations`,
      }),
    );
    expect(screen.queryByRole("button", { name: /close/i })).toBeNull();
    expect(
      screen.getByText(
        kind === "test"
          ? /The data is synthetic\. The request enters the real delivery queue and is rate limited\. Cancelling cannot recall queued or in-flight mail\. SMTP acceptance does not mean inbox delivery\./
          : /The target version changes\. Existing quota rules bind the old version and require route confirmation on the rules page; queued old-version mail will stop\./,
      ),
    ).toBeInTheDocument();
  },
);
it("confirms the target summary and suppresses repeated send clicks", async () => {
  let finish!: (v: unknown) => void;
  mocks.testEmailNotification.mockImplementation(
    () =>
      new Promise((r) => {
        finish = r;
      }),
  );
  mount();
  await userEvent.click(
    await screen.findByRole("button", { name: "Test Synthetic operations" }),
  );
  expect(mocks.testEmailNotification).not.toHaveBeenCalled();
  expect(screen.getByText(/v1/)).toBeInTheDocument();
  const confirm = screen.getByRole("button", {
    name: "Confirm synthetic test send",
  });
  fireEvent.click(confirm);
  fireEvent.click(confirm);
  expect(mocks.testEmailNotification).toHaveBeenCalledTimes(1);
  expect(mocks.testEmailNotification.mock.calls[0][0]).toMatchObject({
    body: { targetId: "target-a", scenario: "warning" },
    headers: { "If-Match": "v1" },
  });
  expect(
    mocks.testEmailNotification.mock.calls[0][0].headers["Idempotency-Key"],
  ).toMatch(/^[\da-f-]{36}$/);
  finish({ data: { id: "delivery-a", state: "pending" } });
  expect(
    await screen.findByText(/Synthetic test queued; check delivery results/),
  ).toBeInTheDocument();
});

it("clears the whole workspace immediately on refresh denial while a sibling request hangs", async () => {
  mount();
  await userEvent.click(
    await screen.findByRole("button", { name: "Edit Synthetic operations" }),
  );
  await userEvent.type(
    screen.getByLabelText("Recipient address"),
    "private@example.test",
  );
  mocks.listEmailDeliveries.mockImplementation(() => new Promise(() => {}));
  mocks.listEmailTargets.mockResolvedValue({
    error: { code: "permission_denied" },
  });
  await userEvent.click(screen.getByRole("button", { name: "Refresh" }));
  expect(
    await screen.findByText(/Email access denied|cannot access email settings/),
  ).toBeInTheDocument();
  expect(screen.queryByLabelText("Recipient address")).not.toBeInTheDocument();
  expect(
    screen.queryByRole("button", { name: "Save target" }),
  ).not.toBeInTheDocument();
});

it("retries an uncertain test using the same immutable idempotency key", async () => {
  mocks.testEmailNotification.mockRejectedValueOnce(
    new Error("private-response"),
  );
  mocks.testEmailNotification.mockResolvedValueOnce({
    data: { id: "d", state: "pending" },
  });
  mount();
  await userEvent.click(
    await screen.findByRole("button", { name: "Test Synthetic operations" }),
  );
  await userEvent.click(
    screen.getByRole("button", { name: "Confirm synthetic test send" }),
  );
  expect(
    await screen.findByText(
      /The request failed and its result may be unknown\. Test retries use the same key; refresh to check delivery results\./,
    ),
  ).toBeInTheDocument();
  expect(screen.queryByText("private-response")).not.toBeInTheDocument();
  await userEvent.click(
    screen.getByRole("button", { name: "Confirm synthetic test send" }),
  );
  await waitFor(() =>
    expect(mocks.testEmailNotification).toHaveBeenCalledTimes(2),
  );
  const [first, second] = mocks.testEmailNotification.mock.calls.map(
    ([options]) => options,
  );
  expect(second.body).toEqual(first.body);
  expect(second.headers).toEqual(first.headers);
});

it("cancels a pending save and ignores its later success after a new editor opens", async () => {
  let finish!: (v: unknown) => void;
  mocks.updateEmailTarget.mockImplementation(
    () =>
      new Promise((r) => {
        finish = r;
      }),
  );
  mount();
  await userEvent.click(
    await screen.findByRole("button", { name: "Edit Synthetic operations" }),
  );
  await userEvent.type(
    screen.getByLabelText("Recipient address"),
    "private@example.test",
  );
  await userEvent.click(screen.getByRole("button", { name: "Save target" }));
  await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
  await userEvent.click(
    screen.getByRole("button", { name: "New email target" }),
  );
  expect(mocks.updateEmailTarget.mock.calls[0][0].signal.aborted).toBe(true);
  finish({ data: target });
  await waitFor(() =>
    expect(
      screen.getByRole("button", { name: "Save disabled target" }),
    ).toBeInTheDocument(),
  );
  expect(screen.getByLabelText("Recipient address")).toHaveValue("");
  expect(
    screen.queryByText(/Email target saved; no test sent/),
  ).not.toBeInTheDocument();
});

it.each(["en-CA"])(
  "associates field errors and focuses the first invalid field in %s",
  async (locale) => {
    localStorage.setItem("ag.console.locale", locale);
    mount();
    await userEvent.click(
      await screen.findByRole("button", {
        name: "New email target",
      }),
    );
    await userEvent.click(
      screen.getByRole("button", {
        name: "Save disabled target",
      }),
    );
    const name = screen.getByLabelText("Target name");
    const recipient = screen.getByLabelText("Recipient address");
    expect(name).toHaveFocus();
    expect(name).toHaveAttribute("aria-invalid", "true");
    expect(name).toHaveAccessibleDescription(/Enter a target name/);
    expect(recipient).toHaveAttribute("aria-invalid", "true");
    expect(recipient).toHaveAccessibleDescription(
      /Enter one recipient email address/,
    );
    await userEvent.type(name, "Synthetic");
    expect(name).not.toHaveAttribute("aria-invalid", "true");
    await userEvent.click(
      screen.getByRole("button", {
        name: "Save disabled target",
      }),
    );
    expect(recipient).toHaveFocus();
    await userEvent.type(recipient, "private-sensitive-marker");
    expect(recipient).toHaveAccessibleDescription(/Enter one valid mailbox/);
    expect(
      screen.queryByText("private-sensitive-marker"),
    ).not.toBeInTheDocument();
    expect(mocks.createEmailTarget).not.toHaveBeenCalled();
    expect(mocks.testEmailNotification).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
    await userEvent.click(
      screen.getByRole("button", {
        name: "New email target",
      }),
    );
    expect(screen.getByLabelText("Recipient address")).toHaveValue("");
    expect(screen.getByLabelText("Recipient address")).not.toHaveAttribute(
      "aria-invalid",
      "true",
    );
  },
);

it.each(["en-CA"])(
  "gives a safe read fallback rather than test retry instructions in %s",
  async (locale) => {
    localStorage.setItem("ag.console.locale", locale);
    mocks.listEmailTargets.mockResolvedValue({
      error: { code: "private-unknown", message: "private-server-marker" },
    });
    mount();
    expect(
      await screen.findByText(
        "Could not read email notification data. Retry the read.",
      ),
    ).toBeInTheDocument();
    expect(
      screen.queryByText(/private-server-marker|same key/),
    ).not.toBeInTheDocument();
    expect(mocks.testEmailNotification).not.toHaveBeenCalled();
  },
);

it("keeps an unknown save outcome separate from test retries without revealing its response", async () => {
  mocks.updateEmailTarget.mockResolvedValue({
    error: { code: "private-unknown", message: "private-server-marker" },
  });
  mount();
  await userEvent.click(
    await screen.findByRole("button", { name: "Edit Synthetic operations" }),
  );
  await userEvent.click(screen.getByRole("button", { name: "Save target" }));
  expect(
    await screen.findByText(
      "Could not confirm the save. Refresh and check target settings before retrying.",
    ),
  ).toBeInTheDocument();
  expect(
    screen.queryByText(/private-server-marker|same key/),
  ).not.toBeInTheDocument();
  expect(mocks.testEmailNotification).not.toHaveBeenCalled();
});

it("locks duplicate saves and discards a cancelled form's late result and errors", async () => {
  let finish!: (value: unknown) => void;
  mocks.createEmailTarget.mockImplementation(
    () =>
      new Promise((resolve) => {
        finish = resolve;
      }),
  );
  mount();
  await userEvent.click(
    await screen.findByRole("button", { name: "New email target" }),
  );
  await userEvent.type(
    screen.getByLabelText("Target name"),
    "Synthetic pending",
  );
  await userEvent.type(
    screen.getByLabelText("Recipient address"),
    "private@example.test",
  );
  const save = screen.getByRole("button", { name: "Save disabled target" });
  fireEvent.click(save);
  fireEvent.click(save);
  expect(mocks.createEmailTarget).toHaveBeenCalledTimes(1);
  const signal = mocks.createEmailTarget.mock.calls[0][0].signal as AbortSignal;
  await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
  expect(signal.aborted).toBe(true);
  await userEvent.click(
    screen.getByRole("button", { name: "New email target" }),
  );
  finish({ data: { ...target, enabled: false } });
  await waitFor(() =>
    expect(screen.getByLabelText("Recipient address")).toHaveValue(""),
  );
  expect(screen.getByLabelText("Target name")).toHaveValue("");
  expect(
    screen.queryByText(/Email target saved; no test sent/),
  ).not.toBeInTheDocument();
  expect(JSON.stringify(localStorage)).not.toContain("private@example.test");
  expect(mocks.testEmailNotification).not.toHaveBeenCalled();
});

it("blocks the workspace when reloading a conflicted target is refused", async () => {
  mocks.updateEmailTarget.mockResolvedValue({
    error: { code: "version_conflict", message: "private-raw" },
  });
  mount();
  await userEvent.click(
    await screen.findByRole("button", { name: "Edit Synthetic operations" }),
  );
  await userEvent.type(
    screen.getByLabelText("Recipient address"),
    "private@example.test",
  );
  await userEvent.click(screen.getByRole("button", { name: "Save target" }));
  expect(
    await screen.findByText(/Target settings changed/),
  ).toBeInTheDocument();
  mocks.getEmailTarget.mockResolvedValue({
    error: { code: "password_change_required" },
  });
  await userEvent.click(
    screen.getByRole("button", {
      name: "Refresh latest target and edit again",
    }),
  );
  expect(
    await screen.findByText(
      /Change your password before returning to this page\./,
    ),
  ).toBeInTheDocument();
  expect(
    screen.queryByRole("button", { name: "Save target" }),
  ).not.toBeInTheDocument();
});
