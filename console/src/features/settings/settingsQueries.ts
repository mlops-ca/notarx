import { useQuery } from "@tanstack/react-query";
import { listIdentityProviders } from "../../client";
import type { IdentityProvider } from "../../client";
import { unwrap } from "../../lib/query/client";
import { settingsKeys } from "../../lib/query/keys";

/**
 * The provider this page edits: the one with slug "default", otherwise the
 * first OIDC provider. The multi-provider management UI is a later slice.
 */
export function pickOidcProvider(
  providers: IdentityProvider[],
): IdentityProvider | null {
  const oidc = providers.filter((provider) => provider.kind === "oidc");
  return (
    oidc.find((provider) => provider.slug === "default") ?? oidc[0] ?? null
  );
}

/** The OIDC provider settings; the Authentication page writes the answer back. */
export function useOidcProvider(emptyMessage: string) {
  return useQuery({
    queryKey: settingsKeys.identityProviders(),
    queryFn: async ({ signal }) => {
      const list = await unwrap(listIdentityProviders({ signal }));
      if (!list) throw new Error(emptyMessage);
      return pickOidcProvider(list.items);
    },
    staleTime: 0,
  });
}
