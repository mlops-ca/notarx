// Package saml is the SAML 2.0 Web SSO service provider core of ADR 0018 section 3.
//
// It wraps github.com/russellhaering/gosaml2 and goxmldsig behind the narrow Verifier
// port, so the rest of the code sees only a Request in and Claims out. XML signature
// wrapping is the principal threat, so notarx runs its own structural scan before the
// library sees a byte and its own post-checks on the element the library returns,
// whatever the library validates itself. Claims are read only from that verified
// element, never queried again from the original document.
//
// Errors carry a machine-readable Reason for the audit log and a generic message for
// callers; neither ever contains assertion content.
package saml
