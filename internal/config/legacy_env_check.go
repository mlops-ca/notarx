package config

import (
	"fmt"
	"sort"
	"strings"
)

// kubernetesServiceLinkEnv lists the variables Kubernetes injects for a Service
// named notarx. They land in the NOTARX_ namespace but are not settings; the
// loader never reads them and the legacy check never flags them (ADR 0013,
// Security considerations). Pod specs also set enableServiceLinks: false.
var kubernetesServiceLinkEnv = []string{"NOTARX_SERVICE_HOST", "NOTARX_PORT"}

// IgnoredServiceLinkEnv reports whether name is a Kubernetes service-link
// variable that the loader ignores.
func IgnoredServiceLinkEnv(name string) bool {
	for _, ignored := range kubernetesServiceLinkEnv {
		if name == ignored {
			return true
		}
	}
	return false
}

// RefuseLegacyEnv returns an error naming the replacement key for every
// variable in environ ("KEY=value" entries, as returned by os.Environ) that is
// in the closed legacy list. It is a refusal check, not a compatibility
// reader: legacy values are never used and never printed. Unrelated variables
// with the legacy prefix are not matched.
func RefuseLegacyEnv(environ []string) error {
	var found []string
	for _, entry := range environ {
		name, _, _ := strings.Cut(entry, "=")
		if replacement, ok := renamedObjectStoreEnv[name]; ok {
			found = append(found, fmt.Sprintf("%s was renamed to %s", name, replacement))
		} else if replacement, ok := legacyEnvNames[name]; ok {
			found = append(found, fmt.Sprintf("%s was renamed to %s", name, replacement))
		}
	}
	if len(found) == 0 {
		return nil
	}
	sort.Strings(found)
	return fmt.Errorf("legacy environment variables are not supported, remove them and set the new names: %s", strings.Join(found, "; "))
}
