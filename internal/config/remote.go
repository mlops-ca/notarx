package config

// RemoteConfig holds the remote instance policy of ADR 0021 ("Remote targets").
type RemoteConfig struct {
	// AllowPrivateNetworks lets remote instance requests reach private, loopback and link-local addresses
	// (NOTARX_REMOTE_ALLOW_PRIVATE_NETWORKS, default false). TLS verification stays mandatory and redirects are
	// never followed.
	AllowPrivateNetworks bool
}

func remoteConfigFromEnv(current RemoteConfig) (RemoteConfig, error) {
	var err error
	current.AllowPrivateNetworks, err = boolEnv("NOTARX_REMOTE_ALLOW_PRIVATE_NETWORKS", current.AllowPrivateNetworks)
	return current, err
}
