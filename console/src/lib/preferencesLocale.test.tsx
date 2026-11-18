import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

// A message chunk that cannot be fetched (offline, stale deploy) must leave the
// Console in its current language, never half in French (ADR 0014).
vi.mock("../messages/frCA.bundle", () => {
  throw new Error("chunk fetch failed");
});

import {
  PreferencesProvider,
  preloadLocale,
  usePreferences,
} from "./preferences";

function Probe() {
  const { locale, setLocale, t } = usePreferences();
  return (
    <>
      <span data-testid="locale">{locale}</span>
      <span data-testid="retry">{t("ui.retry")}</span>
      <button onClick={() => setLocale("fr-CA")}>French</button>
      <button onClick={() => setLocale("zh-CN")}>Chinese</button>
    </>
  );
}

afterEach(() => {
  cleanup();
  localStorage.clear();
  vi.restoreAllMocks();
});

describe("PreferencesProvider message loading", () => {
  it("stays in the current language, and stores nothing, when a chunk fails", async () => {
    const error = vi
      .spyOn(console, "error")
      .mockImplementation(() => undefined);
    const user = userEvent.setup();
    render(
      <PreferencesProvider>
        <Probe />
      </PreferencesProvider>,
    );
    await user.click(screen.getByRole("button", { name: "French" }));
    await waitFor(() => expect(error).toHaveBeenCalled());
    expect(screen.getByTestId("locale")).toHaveTextContent("en-CA");
    expect(screen.getByTestId("retry")).toHaveTextContent("Retry");
    expect(document.documentElement.lang).toBe("en-CA");
    expect(localStorage.getItem("ag.console.locale")).toBeNull();
    // The reader is told, in the language still on screen.
    expect(
      await screen.findByText(
        "Could not load the selected language (French). Check your connection and try again.",
      ),
    ).toBeInTheDocument();
  });

  it("preloads the stored language and swallows a failed fetch", async () => {
    localStorage.setItem("ag.console.locale", "fr-CA");
    await expect(preloadLocale()).resolves.toBeUndefined();
    localStorage.setItem("ag.console.locale", "zh-CN");
    await expect(preloadLocale()).resolves.toBeUndefined();
  });

  it("falls back to en-CA at start when the stored language cannot load", async () => {
    const error = vi
      .spyOn(console, "error")
      .mockImplementation(() => undefined);
    localStorage.setItem("ag.console.locale", "fr-CA");
    render(
      <PreferencesProvider>
        <Probe />
      </PreferencesProvider>,
    );
    await waitFor(() => expect(error).toHaveBeenCalled());
    expect(screen.getByTestId("locale")).toHaveTextContent("en-CA");
  });

  it("switches to a language whose chunk loads, and then to the one it was before", async () => {
    const user = userEvent.setup();
    render(
      <PreferencesProvider>
        <Probe />
      </PreferencesProvider>,
    );
    await user.click(screen.getByRole("button", { name: "Chinese" }));
    await waitFor(() =>
      expect(screen.getByTestId("locale")).toHaveTextContent("zh-CN"),
    );
    expect(localStorage.getItem("ag.console.locale")).toBe("zh-CN");
    expect(document.documentElement.lang).toBe("zh-CN");
  });
});
