package app

import (
	ociprotocol "github.com/mlops-ca/notarx/internal/protocol/oci"
)

// Compatibility aliases retain the app composition surface while OCI cache
// behavior is owned by the protocol module.
type CachedOCIContent = ociprotocol.CachedContent
type OCICache = ociprotocol.Cache

var NewOCICache = ociprotocol.NewCache
var NewDefaultOCICache = ociprotocol.NewDefaultCache

var errOCICacheMiss = ociprotocol.ErrCacheMiss
var errOCICacheNegative = ociprotocol.ErrNegativeCache
