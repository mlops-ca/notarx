// Package scim holds the protocol side of the SCIM 2.0 server of ADR 0018 section 4 (RFC 7643 and RFC 7644): the
// error envelope, the discovery documents, the resource representations, the filter parser and the PATCH normalizer.
// It has no dependency on storage or HTTP; internal/app maps the results onto the repository stores.
package scim
