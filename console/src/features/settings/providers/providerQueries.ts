import { useQuery } from "@tanstack/react-query";
import {
  listAudits,
  listIdentityProviders,
  type IdentityProvider,
} from "../../../client";
import { unwrap } from "../../../lib/query/client";
import { settingsKeys } from "../../../lib/query/keys";

/** Every identity provider, OIDC and SAML. The registry page is the only writer. */
export function useIdentityProviders({ enabled = true } = {}) {
  return useQuery({
    queryKey: settingsKeys.identityProviders(),
    queryFn: async ({ signal }) =>
      (await unwrap(listIdentityProviders({ signal })))?.items ?? [],
    staleTime: 0,
    enabled,
  });
}

/**
 * The OIDC provider that identity links are made against: the provider with
 * slug "default", otherwise the first OIDC provider.
 */
export function pickOidcProvider(
  providers: IdentityProvider[],
): IdentityProvider | null {
  const oidc = providers.filter((provider) => provider.kind === "oidc");
  return (
    oidc.find((provider) => provider.slug === "default") ?? oidc[0] ?? null
  );
}

/** What a login writes when the provider leaves out the groups of a user (too many to carry). */
export const GROUPS_OVERAGE_OPERATIONS = [
  "auth.oidc.groups_overage",
  "auth.saml.groups_overage",
] as const;

/** An overage older than this no longer warns: the cause may be fixed since. */
export const GROUPS_OVERAGE_WINDOW_DAYS = 30;
const DAY_MS = 24 * 60 * 60 * 1000;

/** The key of a provider in the overage map: its resource in the audit record. */
export function overageKey(kind: string, slug: string): string {
  return `auth/${kind}/${slug}`;
}

/**
 * When each provider last had a groups overage within the window, by audit
 * resource (auth/<kind>/<slug>). The audit API filters on the operation; the
 * resource tells providers apart.
 */
export function useGroupsOverage(now: () => number = Date.now) {
  return useQuery({
    queryKey: [...settingsKeys.all, "groups-overage"],
    queryFn: async ({ signal }) => {
      const since = now() - GROUPS_OVERAGE_WINDOW_DAYS * DAY_MS;
      const pages = await Promise.all(
        GROUPS_OVERAGE_OPERATIONS.map((operation) =>
          unwrap(listAudits({ query: { operation, limit: 500 }, signal })),
        ),
      );
      const latest = new Map<string, string>();
      for (const record of pages.flatMap((page) => page ?? [])) {
        const at = Date.parse(record.occurredAt);
        if (!record.resource || Number.isNaN(at) || at < since) continue;
        const seen = latest.get(record.resource);
        if (!seen || at > Date.parse(seen))
          latest.set(record.resource, record.occurredAt);
      }
      return latest;
    },
    staleTime: 0,
  });
}
