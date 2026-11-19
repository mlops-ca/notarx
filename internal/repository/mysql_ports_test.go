package repository

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestMySQLStoreReportsEveryUnimplementedPort(t *testing.T) {
	missing := MissingMySQLStorePorts()
	if len(missing) != len(gatewayStorePorts) {
		t.Fatalf("an empty MySQLStore implements %d of %d ports", len(gatewayStorePorts)-len(missing), len(gatewayStorePorts))
	}
	for i := 1; i < len(missing); i++ {
		if missing[i-1] >= missing[i] {
			t.Fatalf("not sorted: %v", missing)
		}
	}
}

// TestGatewayStorePortsCoverTheArchitectureBaseline keeps the port registry in
// step with internal/app.GatewayStore, whose members the baseline lists.
func TestGatewayStorePortsCoverTheArchitectureBaseline(t *testing.T) {
	data, err := os.ReadFile("../../tools/architecture/baseline.json")
	if err != nil {
		t.Fatal(err)
	}
	var baseline struct {
		GatewayStoreMembers []string `json:"gatewayStoreMembers"`
	}
	if err := json.Unmarshal(data, &baseline); err != nil {
		t.Fatal(err)
	}
	for _, member := range baseline.GatewayStoreMembers {
		name, ok := strings.CutPrefix(member, "repository.")
		if !ok {
			t.Fatalf("%s is not a repository port; extend gatewayStorePorts and MissingMySQLStorePorts", member)
		}
		if _, registered := gatewayStorePorts[name]; !registered {
			t.Errorf("GatewayStore member %s is missing from gatewayStorePorts", member)
		}
	}
}
