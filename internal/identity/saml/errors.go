package saml

import "errors"

// Reason is the precise, machine-readable cause of a rejected response. It is written to the
// audit log; callers show only the generic error text.
type Reason string

// Rejection reasons. They never embed assertion content.
const (
	ReasonTooLarge           Reason = "response_too_large"
	ReasonMalformed          Reason = "malformed_response"
	ReasonDTD                Reason = "dtd_not_allowed"
	ReasonEncryptedAssertion Reason = "encrypted_assertion"
	ReasonAssertionCount     Reason = "assertion_count"
	ReasonAssertionPlacement Reason = "assertion_not_direct_child"
	ReasonDuplicateID        Reason = "duplicate_id"
	ReasonUnsigned           Reason = "unsigned"
	ReasonSignatureCount     Reason = "signature_count"
	ReasonSignaturePlacement Reason = "signature_placement"
	ReasonReferenceCount     Reason = "signature_reference_count"
	ReasonReferenceURI       Reason = "signature_reference_uri"
	ReasonSignatureAlgorithm Reason = "signature_algorithm"
	ReasonDigestAlgorithm    Reason = "digest_algorithm"
	ReasonSignatureInvalid   Reason = "signature_invalid"
	ReasonLibraryRejected    Reason = "library_rejected"
	ReasonStatus             Reason = "status_not_success"
	ReasonIssuer             Reason = "issuer_mismatch"
	ReasonDestination        Reason = "destination_mismatch"
	ReasonRecipient          Reason = "recipient_mismatch"
	ReasonAudience           Reason = "audience_mismatch"
	ReasonInResponseTo       Reason = "in_response_to_mismatch"
	ReasonUnsolicited        Reason = "unsolicited_response"
	ReasonConfirmationMethod Reason = "subject_confirmation_method"
	ReasonConfirmationData   Reason = "subject_confirmation_data"
	ReasonNotYetValid        Reason = "assertion_not_yet_valid"
	ReasonExpired            Reason = "assertion_expired"
	ReasonConditions         Reason = "conditions_missing"
	ReasonNameID             Reason = "name_id_missing"
	ReasonTransientNameID    Reason = "name_id_transient"
	ReasonAssertionID        Reason = "assertion_id_invalid"
	ReasonVersion            Reason = "version_unsupported"
	ReasonConfiguration      Reason = "verifier_configuration"
)

// Error is a rejected response. Its text is deliberately generic.
type Error struct {
	Reason Reason
}

func (e *Error) Error() string { return "saml response rejected" }

func reject(reason Reason) error { return &Error{Reason: reason} }

// ReasonOf returns the rejection reason of err, or "" when err is not a verifier rejection.
func ReasonOf(err error) Reason {
	var rejected *Error
	if errors.As(err, &rejected) {
		return rejected.Reason
	}
	return ""
}
