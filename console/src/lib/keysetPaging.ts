import { useCallback, useState } from "react";

interface KeysetState {
  key: string;
  page: number;
  /** tokens[i] is the cursor that loads page i + 2, learned while page i + 1 was shown. */
  tokens: string[];
}

/**
 * Page numbers over a keyset listing (ADR 0023 section 8). The server pages by
 * cursor, so a page is reachable once the page before it was read: the hook
 * remembers each `nextPageToken` and clamps a jump to the furthest known page.
 * Everything resets when `resetKey` (filters, page size) changes, because a
 * cursor is bound to the query that issued it.
 */
export function useKeysetPages(resetKey: string) {
  const [state, setState] = useState<KeysetState>({
    key: resetKey,
    page: 1,
    tokens: [],
  });
  const current: KeysetState =
    state.key === resetKey ? state : { key: resetKey, page: 1, tokens: [] };
  if (state.key !== resetKey) setState(current);

  const remember = useCallback(
    (next: string | undefined) =>
      setState((previous) => {
        if (previous.key !== resetKey) return previous;
        const tokens = previous.tokens.slice(0, previous.page - 1);
        if (next) tokens[previous.page - 1] = next;
        const unchanged =
          tokens.length === previous.tokens.length &&
          tokens.every((token, index) => token === previous.tokens[index]);
        return unchanged ? previous : { ...previous, tokens };
      }),
    [resetKey],
  );

  const setPage = useCallback(
    (target: number | ((page: number) => number)) =>
      setState((previous) => {
        if (previous.key !== resetKey) return previous;
        const wanted =
          typeof target === "function" ? target(previous.page) : target;
        const page = Math.min(Math.max(1, wanted), previous.tokens.length + 1);
        return page === previous.page ? previous : { ...previous, page };
      }),
    [resetKey],
  );

  return {
    page: current.page,
    pageToken: current.page > 1 ? current.tokens[current.page - 2] : undefined,
    setPage,
    remember,
  };
}
