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
        // Returning to an earlier page keeps the cursors already learned for the
        // pages after it, unless this page now points somewhere else.
        const tokens = previous.tokens.slice();
        const index = previous.page - 1;
        if (next && tokens[index] === next) return previous;
        tokens.length = Math.min(tokens.length, index);
        if (next) tokens[index] = next;
        return tokens.length === previous.tokens.length &&
          tokens.every((token, i) => token === previous.tokens[i])
          ? previous
          : { ...previous, tokens };
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

/** The {value, relation} total of a listing (ADR 0023 section 8). */
export interface ListTotalValue {
  value: number;
  relation: "eq" | "gte" | "estimate";
}

/**
 * The total the pager should show. An exact total is used as it is; a lower
 * bound or an estimate never hides a page the server still offers, and never
 * claims fewer items than the pages already read.
 */
export function pagerTotal(
  total: ListTotalValue | undefined,
  page: number,
  pageSize: number,
  hasNext: boolean,
): number {
  if (!total) return 0;
  if (total.relation === "eq") return total.value;
  return Math.max(total.value, page * pageSize + (hasNext ? 1 : 0));
}
