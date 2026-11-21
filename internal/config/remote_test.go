package config

import "testing"

func TestRemoteConfigAllowPrivateNetworks(t *testing.T) {
	got, err := remoteConfigFromEnv(RemoteConfig{})
	if err != nil || got.AllowPrivateNetworks {
		t.Fatalf("default = %+v, %v", got, err)
	}
	t.Setenv("NOTARX_REMOTE_ALLOW_PRIVATE_NETWORKS", "true")
	if got, err = remoteConfigFromEnv(RemoteConfig{}); err != nil || !got.AllowPrivateNetworks {
		t.Fatalf("true = %+v, %v", got, err)
	}
	t.Setenv("NOTARX_REMOTE_ALLOW_PRIVATE_NETWORKS", "yes")
	if _, err = remoteConfigFromEnv(RemoteConfig{}); err == nil {
		t.Fatal("a value that is not true or false must be refused")
	}
}
