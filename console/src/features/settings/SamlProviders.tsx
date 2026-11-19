import { useQuery } from "@tanstack/react-query";
import { listIdentityProviders } from "../../client";
import type { IdentityProvider } from "../../client";
import { Card } from "../../components/ui/Layout";
import { Notice } from "../../components/ui/Feedback";
import { formatDate } from "../../lib/format";
import { usePreferences } from "../../lib/preferences";
import { unwrap } from "../../lib/query/client";
import { settingsKeys } from "../../lib/query/keys";

/** Certificates are flagged this many days before their notAfter. */
export const SAML_CERTIFICATE_WARNING_DAYS = 30;
const DAY_MS = 24 * 60 * 60 * 1000;

/** Whole days until a certificate expires; negative once it has. */
export function daysUntil(notAfter: string, now: number = Date.now()): number {
  return Math.ceil((Date.parse(notAfter) - now) / DAY_MS);
}

function useSamlProviders() {
  return useQuery({
    queryKey: settingsKeys.samlProviders(),
    queryFn: async ({ signal }) => {
      const list = await unwrap(listIdentityProviders({ signal }));
      return (list?.items ?? []).filter((provider) => provider.kind === "saml");
    },
    staleTime: 0,
  });
}

function CertificateWarnings({ provider }: { provider: IdentityProvider }) {
  const { locale, t } = usePreferences();
  const certificates = provider.saml?.certificates ?? [];
  return (
    <>
      {certificates.map((certificate) => {
        const days = daysUntil(certificate.notAfter);
        if (days > SAML_CERTIFICATE_WARNING_DAYS) return null;
        const date = formatDate(certificate.notAfter, locale);
        return (
          <Notice
            key={certificate.pem}
            tone="warning"
            title={
              days < 0
                ? t("settings.samlCertificateExpired", {
                    name: provider.displayName,
                    date,
                  })
                : t("settings.samlCertificateExpiresSoon", {
                    name: provider.displayName,
                    days,
                    date,
                  })
            }
          />
        );
      })}
    </>
  );
}

/**
 * SAML providers are managed through the API in this release; this card shows what an administrator
 * registers at the identity provider and warns before a pinned certificate expires.
 */
export function SamlProvidersCard() {
  const { locale, t } = usePreferences();
  const query = useSamlProviders();
  const providers = query.data ?? [];
  if (providers.length === 0) return null;
  return (
    <Card>
      <div className="space-y-4">
        <div>
          <h2 className="text-base font-semibold">
            {t("settings.samlProviders")}
          </h2>
          <p className="text-sm text-muted">
            {t("settings.samlProvidersDescription")}
          </p>
        </div>
        {providers.map((provider) => (
          <section
            key={provider.id}
            className="space-y-2"
            aria-label={provider.displayName}
          >
            <h3 className="font-medium">
              {provider.displayName}{" "}
              <span className="text-sm text-muted">
                {provider.enabled
                  ? t("settings.enabled")
                  : t("settings.disabled")}
              </span>
            </h3>
            <CertificateWarnings provider={provider} />
            {provider.saml && (
              <dl className="text-sm">
                <dt>{t("settings.samlMetadataURL")}</dt>
                <dd>
                  <code>{provider.saml.metadataEndpoint || "-"}</code>
                </dd>
                <dt>{t("settings.samlACSURL")}</dt>
                <dd>
                  <code>{provider.saml.acsUrl || "-"}</code>
                </dd>
                <dt>{t("settings.samlPinnedCertificates")}</dt>
                <dd>
                  {provider.saml.certificates.length === 0 ? (
                    t("settings.samlNoCertificates")
                  ) : (
                    <ul>
                      {provider.saml.certificates.map((certificate) => (
                        <li key={certificate.pem}>
                          {certificate.state === "next"
                            ? t("settings.samlCertificateNext")
                            : t("settings.samlCertificateActive")}
                          {" - "}
                          {t("settings.samlCertificateExpires", {
                            date: formatDate(certificate.notAfter, locale),
                          })}
                        </li>
                      ))}
                    </ul>
                  )}
                </dd>
              </dl>
            )}
            {provider.saml && (
              <p className="text-sm">
                {t("settings.samlServiceProviderKeys", {
                  count: provider.saml.spKeys.length,
                })}
              </p>
            )}
          </section>
        ))}
      </div>
    </Card>
  );
}
