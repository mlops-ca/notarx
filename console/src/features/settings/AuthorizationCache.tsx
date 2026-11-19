import { useQuery } from "@tanstack/react-query";
import { getSecuritySettings } from "../../client";
import { Card } from "../../components/ui/Layout";
import { usePreferences } from "../../lib/preferences";
import { unwrap } from "../../lib/query/client";
import { settingsKeys } from "../../lib/query/keys";

function useSecuritySettings() {
  return useQuery({
    queryKey: settingsKeys.security(),
    queryFn: ({ signal }) => unwrap(getSecuritySettings({ signal })),
    staleTime: 0,
  });
}

/**
 * The revocation window of the read caches (NOTARX_AUTHZ_CACHE_TTL, ADR 0023): the longest a
 * node may keep honouring a revoked credential, a removed grant or a visibility change when an
 * invalidation is lost. It is configuration, so the card is read-only.
 */
export function AuthorizationCacheCard() {
  const { t } = usePreferences();
  const settings = useSecuritySettings().data;
  if (!settings) return null;
  return (
    <Card>
      <div className="space-y-2">
        <h2 className="text-base font-semibold">
          {t("settings.authorizationCache")}
        </h2>
        <p className="text-sm text-muted">
          {t("settings.authorizationCacheDescription")}
        </p>
        <dl className="text-sm">
          <dt>{t("settings.authorizationCacheWindow")}</dt>
          <dd>
            <strong>
              {settings.authorizationCacheEnabled
                ? t("settings.authorizationCacheSeconds", {
                    seconds: settings.authorizationCacheTtlSeconds,
                  })
                : t("settings.authorizationCacheDisabled")}
            </strong>
          </dd>
        </dl>
        <p className="text-sm text-muted">
          {t("settings.authorizationCacheRegulatedProfile")}
        </p>
      </div>
    </Card>
  );
}
