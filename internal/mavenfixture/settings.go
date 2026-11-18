package mavenfixture

import "strings"

// MirrorXML is a Maven settings mirror that sends Maven Central requests (the
// default "central" repository, which plugins and their dependencies use) to
// the fixture served at baseURL.
func MirrorXML(baseURL string) string {
	return "<mirror><id>central-fixture</id><mirrorOf>central</mirrorOf><url>" + baseURL + "/" + CentralRoute + "</url></mirror>"
}

// AddBlockedProxies inserts BlockedProxiesXML into a complete settings document.
func AddBlockedProxies(settings string) string {
	proxies := BlockedProxiesXML()
	if proxies == "" {
		return settings
	}
	return strings.Replace(settings, "</settings>", "<proxies>"+proxies+"</proxies></settings>", 1)
}

// BlockedProxiesXML is a pair of Maven settings proxies that send every request
// except loopback to a closed port, so a client that tries to reach an external
// host fails instead of silently depending on the network. In record mode it is
// empty, because recording needs the network. The result goes inside <proxies>.
func BlockedProxiesXML() string {
	if RecordMode() {
		return ""
	}
	proxy := func(protocol string) string {
		return "<proxy><id>no-outbound-" + protocol + "</id><active>true</active><protocol>" + protocol +
			"</protocol><host>127.0.0.1</host><port>9</port><nonProxyHosts>127.0.0.1|localhost|[::1]</nonProxyHosts></proxy>"
	}
	return proxy("http") + proxy("https")
}
