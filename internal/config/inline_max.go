package config

// DefaultInlineMaxBytes is the default of NOTARX_INLINE_MAX_BYTES (ADR 0023 section 9):
// a payload at or below it stays inline in its database row, a larger one is stored
// in the object store at meta/sha256/<hex> and the row keeps only its digest, size
// and key. Zero externalises every non-empty payload.
const DefaultInlineMaxBytes = 16384
