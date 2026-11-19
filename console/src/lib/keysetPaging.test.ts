import { act, renderHook } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { pagerTotal, useKeysetPages } from "./keysetPaging";

describe("useKeysetPages", () => {
  it("reaches a page only after the page before it reported a cursor", () => {
    const { result } = renderHook(() => useKeysetPages("a"));
    expect(result.current.page).toBe(1);
    expect(result.current.pageToken).toBeUndefined();

    act(() => result.current.setPage(3));
    expect(result.current.page).toBe(1);

    act(() => result.current.remember("t2"));
    act(() => result.current.setPage(3));
    expect(result.current.page).toBe(2);
    expect(result.current.pageToken).toBe("t2");

    act(() => result.current.remember("t3"));
    act(() => result.current.setPage((page) => page + 1));
    expect(result.current.page).toBe(3);
    expect(result.current.pageToken).toBe("t3");
  });

  it("forgets cursors beyond a page that turned out to be the last", () => {
    const { result } = renderHook(() => useKeysetPages("a"));
    act(() => result.current.remember("t2"));
    act(() => result.current.setPage(2));
    act(() => result.current.remember(undefined));
    act(() => result.current.setPage(3));
    expect(result.current.page).toBe(2);
    act(() => result.current.setPage(0));
    expect(result.current.page).toBe(1);
  });

  it("starts over when the filters or page size change", () => {
    const { result, rerender } = renderHook(
      ({ resetKey }) => useKeysetPages(resetKey),
      { initialProps: { resetKey: "a" } },
    );
    act(() => result.current.remember("t2"));
    act(() => result.current.setPage(2));
    expect(result.current.pageToken).toBe("t2");

    rerender({ resetKey: "b" });
    expect(result.current.page).toBe(1);
    expect(result.current.pageToken).toBeUndefined();
    act(() => result.current.setPage(2));
    expect(result.current.page).toBe(1);
  });
});

describe("useKeysetPages going back", () => {
  it("keeps the cursors of later pages when an earlier page is shown again", () => {
    const { result } = renderHook(() => useKeysetPages("a"));
    act(() => result.current.remember("t2"));
    act(() => result.current.setPage(2));
    act(() => result.current.remember("t3"));
    act(() => result.current.setPage(3));
    act(() => result.current.setPage(1));
    act(() => result.current.remember("t2"));
    act(() => result.current.setPage(3));
    expect(result.current.page).toBe(3);
    expect(result.current.pageToken).toBe("t3");
  });

  it("drops later cursors when a page now points elsewhere", () => {
    const { result } = renderHook(() => useKeysetPages("a"));
    act(() => result.current.remember("t2"));
    act(() => result.current.setPage(2));
    act(() => result.current.remember("t3"));
    act(() => result.current.setPage(1));
    act(() => result.current.remember("t2b"));
    act(() => result.current.setPage(3));
    expect(result.current.page).toBe(2);
    expect(result.current.pageToken).toBe("t2b");
  });
});

describe("pagerTotal", () => {
  it("uses an exact total as it is", () => {
    expect(pagerTotal({ value: 42, relation: "eq" }, 1, 20, true)).toBe(42);
  });

  it("never hides a page that the server still offers", () => {
    expect(pagerTotal({ value: 10001, relation: "gte" }, 600, 20, true)).toBe(
      12001,
    );
    expect(pagerTotal({ value: 900, relation: "estimate" }, 1, 20, false)).toBe(
      900,
    );
  });

  it("is zero before the first answer", () => {
    expect(pagerTotal(undefined, 1, 20, false)).toBe(0);
  });
});
