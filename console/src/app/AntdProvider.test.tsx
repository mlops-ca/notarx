import { DatePicker } from "antd";
import dayjs from "dayjs";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, it } from "vitest";
import { PreferencesProvider, usePreferences } from "../lib/preferences";
import { AntdProvider } from "./AntdProvider";

function Calendar() {
  const { setLocale } = usePreferences();
  return (
    <>
      <button onClick={() => setLocale("en-CA")}>English</button>
      <button onClick={() => setLocale("fr-CA")}>French</button>
      <button onClick={() => setLocale("zh-CN")}>Chinese</button>
      <DatePicker defaultValue={dayjs("2026-10-14")} />
    </>
  );
}

afterEach(() => {
  cleanup();
  localStorage.clear();
});

const weekdays = () =>
  [...document.querySelectorAll(".ant-picker-panel th")].map(
    (node) => node.textContent,
  );

// The antd and dayjs locales load on demand (ADR 0014), so wait for each one.
it.each([
  ["en-CA", ["Su", "Mo", "Tu", "We", "Th", "Fr", "Sa"], "Oct"],
  ["fr-CA", ["di", "lu", "ma", "me", "je", "ve", "sa"], undefined],
  ["zh-CN", ["一", "二", "三", "四", "五", "六", "日"], "10月"],
] as const)(
  "opens the calendar with the %s weekdays",
  async (locale, expected, month) => {
    localStorage.setItem("ag.console.locale", locale);
    const user = userEvent.setup();
    render(
      <PreferencesProvider>
        <AntdProvider>
          <Calendar />
        </AntdProvider>
      </PreferencesProvider>,
    );
    await waitFor(
      async () => {
        if (weekdays().length === 0) {
          await user.click(screen.getByRole("textbox"));
        }
        expect(weekdays()).toEqual(expected);
      },
      { timeout: 5000 },
    );
    if (month) {
      expect(
        document.querySelector(".ant-picker-header .ant-picker-month-btn"),
      ).toHaveTextContent(month);
    }
  },
  20000,
);

it("switches the picker language with the Console language", async () => {
  const user = userEvent.setup();
  render(
    <PreferencesProvider>
      <AntdProvider>
        <Calendar />
      </AntdProvider>
    </PreferencesProvider>,
  );
  const placeholder = () =>
    document
      .querySelector(".ant-picker-input input")
      ?.getAttribute("placeholder");
  await user.click(screen.getByRole("button", { name: "French" }));
  await waitFor(() => expect(placeholder()).toBe("Sélectionner une date"));
  await user.click(screen.getByRole("button", { name: "English" }));
  await waitFor(() => expect(placeholder()).toBe("Select date"));
});
