import { useQuery } from "@tanstack/react-query";

/** One enabled identity provider of the login chooser (GET /auth/idp). */
export interface LoginIdentityProvider {
  slug: string;
  displayName: string;
  kind: "oidc" | "saml";
}

/**
 * The enabled identity providers. Discovery is optional: when it is offline
 * or refused, local and token sign-in remain available.
 */
export function useLoginIdentityProviders() {
  return useQuery({
    queryKey: ["auth", "identity-providers"],
    queryFn: async ({ signal }): Promise<LoginIdentityProvider[]> => {
      try {
        const response = await fetch("/auth/idp", {
          credentials: "include",
          signal,
        });
        if (!response.ok) return [];
        const body = (await response.json()) as {
          items?: LoginIdentityProvider[];
        };
        return Array.isArray(body.items) ? body.items : [];
      } catch (error) {
        if (signal.aborted) throw error;
        return [];
      }
    },
    meta: { authFailure: "ignore" },
    staleTime: 0,
  });
}

/** The browser entry point of a provider's sign-in flow. */
export function loginPath(provider: LoginIdentityProvider, redirect: string) {
  return `/auth/${provider.kind}/${encodeURIComponent(provider.slug)}/login?redirect=${encodeURIComponent(redirect)}`;
}
