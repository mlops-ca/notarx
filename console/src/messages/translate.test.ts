import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { enCA, type MessageKey } from ".";
import { frCA } from "./frCA.bundle";
import {
  LOCALE_FALLBACK,
  LocalizedError,
  createTranslator,
  errorText,
  isMessagesLoaded,
  loadMessages,
} from "./translate";
import { zhCN } from "./zhCN.bundle";

beforeAll(async () => {
  await Promise.all([loadMessages("fr-CA"), loadMessages("zh-CN")]);
});

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

describe("loadMessages", () => {
  it("reads a locale that is not loaded as en-CA, then as its own table once loaded", async () => {
    vi.resetModules();
    const fresh = await import("./translate");
    const key: MessageKey = "ui.retry";
    expect(fresh.isMessagesLoaded("fr-CA")).toBe(false);
    expect(fresh.createTranslator("fr-CA")(key)).toBe(enCA[key]);
    const first = fresh.loadMessages("fr-CA");
    expect(fresh.loadMessages("fr-CA")).toBe(first);
    await first;
    expect(fresh.isMessagesLoaded("fr-CA")).toBe(true);
    expect(fresh.createTranslator("fr-CA")(key)).toBe(frCA[key]);
    await expect(fresh.loadMessages("fr-CA")).resolves.toBeUndefined();
  });

  it("keeps en-CA available and rejects, then retries, a failed chunk fetch", async () => {
    vi.resetModules();
    let broken = true;
    vi.doMock("./frCA.bundle", () => {
      if (broken) throw new Error("chunk fetch failed");
      return { frCA };
    });
    const fresh = await import("./translate");
    await expect(fresh.loadMessages("fr-CA")).rejects.toThrow();
    expect(fresh.isMessagesLoaded("fr-CA")).toBe(false);
    // Nothing is half-loaded: the translator still speaks the reference.
    expect(fresh.createTranslator("fr-CA")("ui.retry")).toBe(enCA["ui.retry"]);
    expect(fresh.isMessagesLoaded("en-CA")).toBe(true);
    broken = false;
    await fresh.loadMessages("fr-CA");
    expect(fresh.createTranslator("fr-CA")("ui.retry")).toBe(frCA["ui.retry"]);
    vi.doUnmock("./frCA.bundle");
  });

  it("marks en-CA loaded from the start", () => {
    expect(isMessagesLoaded("en-CA")).toBe(true);
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
