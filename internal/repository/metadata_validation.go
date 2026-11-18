package repository

// MaxMetadataJSONBytes bounds a format store's metadata_json document. It replaced the
// jsonb_typeof(metadata_json::jsonb) = 'object' CHECK constraints dropped when the
// column became jsonb (ADR 0023 section 9); the shape rule itself lives in
// internal/jsonmeta and is applied by the session validators of the Helm, NuGet and
// RubyGems stores. The octet_length(payload::text) checks of the OCI discovery tables
// are enforced by ocidiscovery.Payload (MaxPayload, 8 MiB).
const MaxMetadataJSONBytes = 256 << 10
