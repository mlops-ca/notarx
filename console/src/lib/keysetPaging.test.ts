import { act, renderHook } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { useKeysetPages } from "./keysetPaging";

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
