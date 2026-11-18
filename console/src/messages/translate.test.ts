import { afterEach, describe, expect, it } from "vitest";
import { enCA, frCA, zhCN, type MessageKey } from ".";
import {
  LOCALE_FALLBACK,
  LocalizedError,
  createTranslator,
  errorText,
} from "./translate";

describe("LOCALE_FALLBACK", () => {
  it("is the single source of the chain: zh-CN falls back to en-CA, fr-CA to nothing", () => {
    expect(LOCALE_FALLBACK).toEqual({
      "en-CA": [],
      "fr-CA": [],
      "zh-CN": ["en-CA"],
    });
  });
});

describe("createTranslator fallback", () => {
  const retry: MessageKey = "ui.retry";
  const disable: MessageKey = "system.disableTarget";
  const saved = { retry: zhCN[retry], disable: zhCN[disable] };

  afterEach(() => {
    zhCN[retry] = saved.retry;
    zhCN[disable] = saved.disable;
  });

  it("reads zh-CN where it has the key", () => {
    expect(saved.retry).toBeDefined();
    expect(createTranslator("zh-CN")(retry)).toBe(saved.retry);
  });

  it("falls back to en-CA for a key the zh-CN overlay lacks", () => {
    delete zhCN[retry];
    expect(createTranslator("zh-CN")(retry)).toBe("Retry");
  });

  it("fills placeholders in the fallback text", () => {
    delete zhCN[disable];
    expect(createTranslator("zh-CN")(disable, { name: "Ops" })).toBe(
      "Disable Ops",
    );
  });

  it("serves en-CA directly and fr-CA from its own total table", () => {
    expect(createTranslator("en-CA")(retry)).toBe(enCA[retry]);
    expect(createTranslator("fr-CA")(retry)).toBe(frCA[retry]);
    expect(createTranslator("fr-CA")(retry)).not.toBe(enCA[retry]);
    expect(createTranslator("fr-CA")(disable, { name: "Ops" })).toContain(
      "Ops",
    );
  });
});

describe("LocalizedError", () => {
  it("builds its message from the en-CA table and is translated by key", () => {
    const error = new LocalizedError("publicBrowse.failedToReadOciTagsStatus", {
      status: 503,
    });
    expect(error.message).toBe("Failed to read OCI tags (503)");
    expect(
      errorText(error, createTranslator("en-CA"), "ui.requestFailed"),
    ).toBe("Failed to read OCI tags (503)");
    expect(
      errorText(error, createTranslator("zh-CN"), "ui.requestFailed"),
    ).toBe(createTranslator("zh-CN")(error.key, error.variables));
  });
});
