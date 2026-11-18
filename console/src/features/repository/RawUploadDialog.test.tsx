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
import type { Repository } from "../../client";
import { PreferencesProvider } from "../../lib/preferences";
import { RawUploadDialog } from "./RawUploadDialog";

vi.mock("../../lib/auth", () => ({
  useAuth: () => ({ token: "test-token" }),
}));

const repository: Repository = {
  id: "11111111-1111-4111-8111-111111111111",
  name: "raw-releases",
  format: "raw",
  type: "hosted",
  allowedHosts: [],
  anonymousRead: false,
  mavenStrictPublication: false,
  state: "active",
  version: "1",
};

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

// The mask is the dismissal route the upload dialog's `busy` flag controls,
// and it only closes on a click that both starts and lands on it.
function clickMask() {
  const mask = document.querySelector(".ant-modal-wrap");
  expect(mask).not.toBeNull();
  fireEvent.mouseDown(mask as HTMLElement);
  fireEvent.click(mask as HTMLElement);
}

describe("RawUploadDialog", () => {
  it("uploads Unicode file names using the server's canonical Raw path encoding", async () => {
    const user = userEvent.setup();
    const fetchMock = vi
      .fn()
      .mockResolvedValue(new Response(null, { status: 201 }));
    vi.stubGlobal("fetch", fetchMock);
    render(
      <PreferencesProvider>
        <RawUploadDialog repo={repository} onUploaded={vi.fn()} />
      </PreferencesProvider>,
    );

    await user.click(screen.getByRole("button", { name: /Upload$/ }));
    const dialog = screen.getByRole("dialog");
    const fileInput =
      document.querySelector<HTMLInputElement>('input[type="file"]');
    expect(fileInput).not.toBeNull();
    const fileName = "Écran capturé 2026-08-19 13_56_07 (1).png";
    const file = new File(["image"], fileName, { type: "image/png" });
    await user.upload(fileInput!, file);
    await user.click(within(dialog).getByRole("button", { name: /Upload/ }));

    await waitFor(() => expect(fetchMock).toHaveBeenCalledOnce());
    expect(fetchMock).toHaveBeenCalledWith(
      "/raw/raw-releases/%C3%89cran%20captur%C3%A9%202026-08-19%2013_56_07%20%281%29.png",
      expect.objectContaining({ method: "PUT", body: file }),
    );
  });

  it("explains invalid target paths before sending the upload", async () => {
    const user = userEvent.setup();
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    render(
      <PreferencesProvider>
        <RawUploadDialog repo={repository} onUploaded={vi.fn()} />
      </PreferencesProvider>,
    );

    await user.click(screen.getByRole("button", { name: /Upload$/ }));
    const dialog = screen.getByRole("dialog");
    const fileInput =
      document.querySelector<HTMLInputElement>('input[type="file"]');
    expect(fileInput).not.toBeNull();
    await user.upload(fileInput!, new File(["image"], "image.png"));
    await user.clear(
      within(dialog).getByRole("textbox", { name: "Target path" }),
    );
    await user.type(
      within(dialog).getByRole("textbox", { name: "Target path" }),
      "releases/../image.png",
    );
    await user.click(within(dialog).getByRole("button", { name: /Upload/ }));

    expect(fetchMock).not.toHaveBeenCalled();
    expect(
      await within(dialog).findByText(/Invalid target path.*dot-dot/),
    ).toBeInTheDocument();
  });

  it("accepts one leading slash as an explicit repository-root path", async () => {
    const user = userEvent.setup();
    const fetchMock = vi
      .fn()
      .mockResolvedValue(new Response(null, { status: 201 }));
    vi.stubGlobal("fetch", fetchMock);
    render(
      <PreferencesProvider>
        <RawUploadDialog repo={repository} onUploaded={vi.fn()} />
      </PreferencesProvider>,
    );

    await user.click(screen.getByRole("button", { name: /Upload$/ }));
    const dialog = screen.getByRole("dialog");
    const fileInput =
      document.querySelector<HTMLInputElement>('input[type="file"]');
    await user.upload(fileInput!, new File(["image"], "image.png"));
    const pathInput = within(dialog).getByRole("textbox", {
      name: "Target path",
    });
    await user.clear(pathInput);
    await user.type(pathInput, "/rust/omz-tui-source-v0.1.0-dev.tar.gz");
    await user.click(within(dialog).getByRole("button", { name: /Upload/ }));

    await waitFor(() => expect(fetchMock).toHaveBeenCalledOnce());
    expect(fetchMock).toHaveBeenCalledWith(
      "/raw/raw-releases/rust/omz-tui-source-v0.1.0-dev.tar.gz",
      expect.objectContaining({ method: "PUT" }),
    );
  });

  it("preserves intentional leading and trailing spaces in a relative path", async () => {
    const user = userEvent.setup();
    const fetchMock = vi
      .fn()
      .mockResolvedValue(new Response(null, { status: 201 }));
    vi.stubGlobal("fetch", fetchMock);
    render(
      <PreferencesProvider>
        <RawUploadDialog repo={repository} onUploaded={vi.fn()} />
      </PreferencesProvider>,
    );

    await user.click(screen.getByRole("button", { name: /Upload$/ }));
    const dialog = screen.getByRole("dialog");
    const fileInput =
      document.querySelector<HTMLInputElement>('input[type="file"]');
    const file = new File(["image"], "image.png");
    await user.upload(fileInput!, file);
    const pathInput = within(dialog).getByRole("textbox", {
      name: "Target path",
    });
    await user.clear(pathInput);
    await user.type(pathInput, " release/image.png ");
    await user.click(within(dialog).getByRole("button", { name: /Upload/ }));

    await waitFor(() => expect(fetchMock).toHaveBeenCalledOnce());
    expect(fetchMock).toHaveBeenCalledWith(
      "/raw/raw-releases/%20release/image.png%20",
      expect.objectContaining({ method: "PUT", body: file }),
    );
  });

  it("translates a server Raw path rejection into an actionable message", async () => {
    const user = userEvent.setup();
    const fetchMock = vi
      .fn()
      .mockResolvedValue(new Response("invalid raw path\n", { status: 400 }));
    vi.stubGlobal("fetch", fetchMock);
    render(
      <PreferencesProvider>
        <RawUploadDialog repo={repository} onUploaded={vi.fn()} />
      </PreferencesProvider>,
    );

    await user.click(screen.getByRole("button", { name: /Upload$/ }));
    const dialog = screen.getByRole("dialog");
    const fileInput =
      document.querySelector<HTMLInputElement>('input[type="file"]');
    expect(fileInput).not.toBeNull();
    await user.upload(fileInput!, new File(["image"], "image.png"));
    await user.click(within(dialog).getByRole("button", { name: /Upload/ }));

    expect(
      await within(dialog).findByText(
        /Upload failed.*does not follow the Raw path rules.*Unicode, spaces, and parentheses/,
      ),
    ).toBeInTheDocument();
    expect(
      within(dialog).getByText("Raw file upload failed"),
    ).toBeInTheDocument();
    expect(
      within(dialog).queryByText(/invalid raw path/),
    ).not.toBeInTheDocument();
  });

  it("holds the dialog while the upload is in flight and frees it on failure", async () => {
    const user = userEvent.setup();
    let settle: (response: Response) => void = () => {};
    const fetchMock = vi.fn(
      () =>
        new Promise<Response>((resolve) => {
          settle = resolve;
        }),
    );
    vi.stubGlobal("fetch", fetchMock);
    render(
      <PreferencesProvider>
        <RawUploadDialog repo={repository} onUploaded={vi.fn()} />
      </PreferencesProvider>,
    );

    await user.click(screen.getByRole("button", { name: /Upload$/ }));
    const dialog = screen.getByRole("dialog");
    const fileInput =
      document.querySelector<HTMLInputElement>('input[type="file"]');
    expect(fileInput).not.toBeNull();
    await user.upload(fileInput!, new File(["image"], "release.png"));
    await user.click(within(dialog).getByRole("button", { name: /Upload/ }));

    clickMask();
    expect(screen.getByRole("dialog")).toBeInTheDocument();

    settle(new Response("storage offline", { status: 503 }));
    expect(
      await within(screen.getByRole("dialog")).findByText(
        /Upload failed \(503\): storage offline/,
      ),
    ).toBeInTheDocument();

    clickMask();
    await waitFor(() =>
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
    );
  });
});
