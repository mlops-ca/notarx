package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoadOTLPLogsDefaultsAndSeparateEndpoint(t *testing.T) {
	setCompleteConfiguration(t)
	for _, name := range []string{"NOTARX_OTLP_LOGS_ENDPOINT", "NOTARX_OTLP_LOGS_HEADERS", "NOTARX_OTLP_LOGS_QUEUE_SIZE", "NOTARX_OTLP_LOGS_TIMEOUT", "NOTARX_OTLP_LOGS_SHUTDOWN_TIMEOUT"} {
		t.Setenv(name, "")
	}
	t.Setenv("NOTARX_OTLP_HTTP_ENDPOINT", "synthetic-trace-endpoint")
	cfg, err := Load()
	if err != nil || cfg.OTLPLogsEndpoint != "" || len(cfg.OTLPLogsHeaders) != 0 || cfg.OTLPLogsQueueSize != 256 || cfg.OTLPLogsTimeout != 3*time.Second || cfg.OTLPLogsShutdownTimeout != 5*time.Second {
		t.Fatalf("Logs defaults = %q %d %s %s, %v", cfg.OTLPLogsEndpoint, cfg.OTLPLogsQueueSize, cfg.OTLPLogsTimeout, cfg.OTLPLogsShutdownTimeout, err)
	}
	t.Setenv("NOTARX_OTLP_LOGS_ENDPOINT", "https://collector.example.test/v1/logs")
	t.Setenv("NOTARX_OTLP_LOGS_HEADERS", `{"Authorization":"Bearer synthetic-credential-marker"}`)
	t.Setenv("NOTARX_OTLP_LOGS_QUEUE_SIZE", "16")
	t.Setenv("NOTARX_OTLP_LOGS_TIMEOUT", "250ms")
	t.Setenv("NOTARX_OTLP_LOGS_SHUTDOWN_TIMEOUT", "2")
	cfg, err = Load()
	if err != nil || cfg.OTLPLogsEndpoint != "https://collector.example.test/v1/logs" || cfg.OTLPHTTPEndpoint != "synthetic-trace-endpoint" || cfg.OTLPLogsHeaders["Authorization"] != "Bearer synthetic-credential-marker" || cfg.OTLPLogsQueueSize != 16 || cfg.OTLPLogsTimeout != 250*time.Millisecond || cfg.OTLPLogsShutdownTimeout != 2*time.Second {
		t.Fatalf("Logs config mismatch: %v", err)
	}
}

func TestLoadRejectsInvalidOTLPLogsWithoutEchoingCredentials(t *testing.T) {
	for name, values := range map[string][]string{
		"NOTARX_OTLP_LOGS_ENDPOINT":         {"collector:4318", "file:///tmp/synthetic", "https://synthetic-secret-marker@collector.example.test", "https://collector.example.test?synthetic-secret-marker", "https://collector.example.test#synthetic-secret-marker"},
		"NOTARX_OTLP_LOGS_HEADERS":          {"null", "[]", `{"Authorization":5}`, `{"Authorization":"synthetic-secret-marker\nunsafe"}`, `{"Content-Type":"synthetic-secret-marker"}`},
		"NOTARX_OTLP_LOGS_QUEUE_SIZE":       {"0", "-1", "1025", "many"},
		"NOTARX_OTLP_LOGS_TIMEOUT":          {"0", "31s", "1ns", "9223372036854775807"},
		"NOTARX_OTLP_LOGS_SHUTDOWN_TIMEOUT": {"0", "11s", "-1s", "many"},
	} {
		for _, value := range values {
			t.Run(name+"/"+value, func(t *testing.T) {
				setCompleteConfiguration(t)
				t.Setenv(name, value)
				_, err := Load()
				if err == nil || strings.Contains(err.Error(), "synthetic-secret-marker") {
					t.Fatalf("invalid or unsafe configuration error: %v", err)
				}
			})
		}
	}
}
