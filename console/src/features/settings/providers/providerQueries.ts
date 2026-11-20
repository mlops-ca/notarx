import { useQuery } from "@tanstack/react-query";
import {
  listAudits,
  listIdentityProviders,
  type IdentityProvider,
} from "../../../client";
import { unwrap } from "../../../lib/query/client";
import { settingsKeys } from "../../../lib/query/keys";

/** Every identity provider, OIDC and SAML. The registry page is the only writer. */
export function useIdentityProviders() {
  return useQuery({
    queryKey: settingsKeys.identityProviders(),
    queryFn: async ({ signal }) =>
      (await unwrap(listIdentityProviders({ signal })))?.items ?? [],
    staleTime: 0,
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

/** The audit operation that a login writes when Entra ID leaves out the groups of a user. */
export const GROUPS_OVERAGE_OPERATION = "auth.oidc.groups_overage";

/**
 * When each OIDC provider last had a groups overage, by slug. The audit API
 * filters on the operation; the resource (auth/oidc/<slug>) tells providers apart.
 */
export function useGroupsOverage() {
  return useQuery({
    queryKey: [...settingsKeys.all, "groups-overage"],
    queryFn: async ({ signal }) => {
      const page = await unwrap(
        listAudits({
          query: { operation: GROUPS_OVERAGE_OPERATION, limit: 500 },
          signal,
        }),
      );
      const latest = new Map<string, string>();
      for (const record of page ?? []) {
        const match = /^auth\/oidc\/(.+)$/.exec(record.resource ?? "");
        if (!match) continue;
        const seen = latest.get(match[1]);
        if (!seen || Date.parse(record.occurredAt) > Date.parse(seen))
          latest.set(match[1], record.occurredAt);
      }
      return latest;
    },
    staleTime: 0,
  });
}
