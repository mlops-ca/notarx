import type { IdentityProvider } from "../../../client";
import { Notice } from "../../../components/ui/Feedback";
import { formatDate } from "../../../lib/format";
import { usePreferences } from "../../../lib/preferences";
import { SAML_CERTIFICATE_WARNING_DAYS, daysUntil } from "./providerModel";
import { overageKey } from "./providerQueries";

/** Certificates of a SAML provider that expire within the warning window, or already have. */
function expiringCertificates(provider: IdentityProvider) {
  return (provider.saml?.certificates ?? []).filter(
    (certificate) =>
      daysUntil(certificate.notAfter) <= SAML_CERTIFICATE_WARNING_DAYS,
  );
}

/** How many warnings a provider carries; the registry table shows the count. */
export function providerWarningCount(
  provider: IdentityProvider,
  overage: ReadonlyMap<string, string>,
): number {
  return (
    expiringCertificates(provider).length +
    (overage.has(overageKey(provider.kind, provider.slug)) ? 1 : 0)
  );
}

/**
 * Warnings that need an administrator's attention: SAML signing certificates
 * 30 days before their notAfter, and Entra ID groups overage seen at sign-in
 * (the login handler writes an audit event, ADR 0018).
 */
export function ProviderWarnings({
  providers,
  overage,
}: {
  providers: IdentityProvider[];
  /** Latest overage time by audit resource (auth/<kind>/<slug>). */
  overage: ReadonlyMap<string, string>;
}) {
  const { locale, t } = usePreferences();
  return (
    <>
      {providers.flatMap((provider) => [
        ...expiringCertificates(provider).map((certificate) => {
          const days = daysUntil(certificate.notAfter);
          const date = formatDate(certificate.notAfter, locale);
          return (
            <Notice
              key={`${provider.id}:${certificate.pem}`}
              tone="warning"
              closable={false}
              title={
                days < 0
                  ? t("providers.certExpired", {
                      name: provider.displayName,
                      date,
                    })
                  : t("providers.certExpiresSoon", {
                      name: provider.displayName,
                      days,
                      date,
                    })
              }
            />
          );
        }),
        ...(overage.has(overageKey(provider.kind, provider.slug))
          ? [
              <Notice
                key={`${provider.id}:overage`}
                tone="warning"
                closable={false}
                title={t("providers.overageTitle", {
                  name: provider.displayName,
                })}
                description={t("providers.overage", {
                  date: formatDate(
                    overage.get(overageKey(provider.kind, provider.slug)) ?? "",
                    locale,
                  ),
                })}
              />,
            ]
          : []),
      ])}
    </>
  );
}
