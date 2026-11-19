package saml

import (
	"context"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	saml2 "github.com/russellhaering/gosaml2"
	"github.com/russellhaering/gosaml2/types"
	dsig "github.com/russellhaering/goxmldsig"

	"github.com/mlops-ca/notarx/internal/identity"
)

// Limits of the verifier; they mirror ADR 0018 section 3.
const (
	// DefaultMaxResponseBytes is the 1 MiB cap on the decoded response.
	DefaultMaxResponseBytes int64 = 1 << 20
	// MaxClockSkew is the largest accepted clock skew.
	MaxClockSkew = 5 * time.Minute
	// maxNameIDLength bounds the identity key.
	maxNameIDLength      = 512
	maxAssertionIDLength = 256
)

// NameID formats. transient is refused at login.
const (
	NameIDPersistent    = "persistent"
	NameIDEmailAddress  = "emailAddress"
	NameIDUnspecified   = "unspecified"
	nameIDURIPersistent = "urn:oasis:names:tc:SAML:2.0:nameid-format:persistent"
	nameIDURIEmail      = "urn:oasis:names:tc:SAML:1.1:nameid-format:emailAddress"
	nameIDURIUnspecific = "urn:oasis:names:tc:SAML:1.1:nameid-format:unspecified"
	nameIDURITransient  = "urn:oasis:names:tc:SAML:2.0:nameid-format:transient"
	subjectBearer       = "urn:oasis:names:tc:SAML:2.0:cm:bearer"
	statusSuccess       = "urn:oasis:names:tc:SAML:2.0:status:Success"
)

// NameIDFormatURI maps a configured format to the URI requested from the IdP; it reports false
// for an unknown format (transient is deliberately unknown).
func NameIDFormatURI(format string) (string, bool) {
	switch format {
	case "", NameIDPersistent:
		return nameIDURIPersistent, true
	case NameIDEmailAddress:
		return nameIDURIEmail, true
	case NameIDUnspecified:
		return nameIDURIUnspecific, true
	}
	return "", false
}

// Config is everything the verifier needs about one provider and this service provider.
type Config struct {
	// Certificates are the pinned IdP signing certificates; any listed certificate verifies.
	Certificates []*x509.Certificate
	// EntityID is the IdP entity id the Issuer must equal.
	EntityID string
	// ACSURL is the assertion consumer service URL that Destination and Recipient must equal.
	ACSURL string
	// SPEntityID is the audience the assertion must name.
	SPEntityID string
	// ClockSkew is the leeway for NotBefore and NotOnOrAfter (at most MaxClockSkew).
	ClockSkew time.Duration
	// MaxResponseBytes caps the decoded response; zero selects DefaultMaxResponseBytes.
	MaxResponseBytes int64
	// NameIDFormat is the configured format (persistent by default).
	NameIDFormat string
	// Mapping overrides the attribute defaults.
	Mapping identity.AttributeMapping
	// Now is the clock; nil selects time.Now.
	Now func() time.Time
}

// Request is one posted response.
type Request struct {
	// SAMLResponse is the base64 value of the SAMLResponse form field.
	SAMLResponse string
	// InResponseTo is the AuthnRequest id the login flow expects. It is empty only for an
	// IdP-initiated response, which the verifier accepts only when AllowUnsolicited is set.
	InResponseTo string
	// AllowUnsolicited accepts a response that carries no InResponseTo (settings.allowIdpInitiated).
	AllowUnsolicited bool
}

// Claims is what a verified assertion asserts; it is read only from the verified element.
type Claims struct {
	AssertionID string
	// ReplayUntil is the later of the NotOnOrAfter values plus the clock skew: the assertion id
	// must be refused until then.
	ReplayUntil time.Time
	// NameID is the identity key of the user for this provider (lower-cased for emailAddress).
	NameID       string
	NameIDFormat string
	SessionIndex string
	// AuthnContext is recorded, not enforced.
	AuthnContext string
	Username     string
	Email        string
	DisplayName  string
	ExternalID   string
	Groups       []string
	// Attributes holds every asserted attribute by name.
	Attributes map[string][]string
}

// Verifier validates a posted SAML response.
type Verifier interface {
	Verify(ctx context.Context, request Request) (Claims, error)
}

type verifier struct{ config Config }

// NewVerifier validates the configuration and returns the verifier.
func NewVerifier(config Config) (Verifier, error) {
	if len(config.Certificates) == 0 {
		return nil, fmt.Errorf("saml: at least one pinned certificate is required")
	}
	if config.EntityID == "" || config.ACSURL == "" || config.SPEntityID == "" {
		return nil, fmt.Errorf("saml: entity id, ACS URL and service provider entity id are required")
	}
	if config.ClockSkew < 0 || config.ClockSkew > MaxClockSkew {
		return nil, fmt.Errorf("saml: clock skew must be between 0 and %s", MaxClockSkew)
	}
	if config.MaxResponseBytes <= 0 {
		config.MaxResponseBytes = DefaultMaxResponseBytes
	}
	if _, ok := NameIDFormatURI(config.NameIDFormat); !ok {
		return nil, fmt.Errorf("saml: nameIdFormat must be persistent, emailAddress or unspecified")
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	return &verifier{config: config}, nil
}

func (v *verifier) Verify(_ context.Context, request Request) (Claims, error) {
	c := v.config
	if int64(len(request.SAMLResponse)) > c.MaxResponseBytes*4/3+8 {
		return Claims{}, reject(ReasonTooLarge)
	}
	raw, err := base64.StdEncoding.DecodeString(request.SAMLResponse)
	if err != nil {
		return Claims{}, reject(ReasonMalformed)
	}
	if int64(len(raw)) > c.MaxResponseBytes {
		return Claims{}, reject(ReasonTooLarge)
	}
	root, err := parseStructure(raw)
	if err != nil {
		return Claims{}, err
	}
	if _, err := checkStructure(root); err != nil {
		return Claims{}, err
	}
	now := c.Now()
	provider := &saml2.SAMLServiceProvider{
		IdentityProviderIssuer:      c.EntityID,
		AssertionConsumerServiceURL: c.ACSURL,
		AudienceURI:                 c.SPEntityID,
		IDPCertificateStore:         &dsig.MemoryX509CertificateStore{Roots: c.Certificates},
		// The library checks its own time rules strictly; running its clock back by the skew keeps it
		// from rejecting what the skew-aware checks below accept. Those checks are authoritative.
		Clock:                       dsig.NewFakeClockAt(now.Add(-c.ClockSkew)),
		MaximumDecompressedBodySize: c.MaxResponseBytes,
	}
	response, err := provider.ValidateEncodedResponse(request.SAMLResponse)
	if err != nil {
		return Claims{}, libraryRejection(err)
	}
	return v.postChecks(response, request, now)
}

// libraryRejection classifies a library error without keeping its text, which can quote values.
func libraryRejection(err error) error {
	var invalid saml2.ErrInvalidValue
	switch {
	case errors.Is(err, dsig.ErrMissingSignature):
		return reject(ReasonUnsigned)
	case errors.As(err, &invalid):
		switch invalid.Key {
		case saml2.IssuerTag:
			return reject(ReasonIssuer)
		case saml2.DestinationAttr:
			return reject(ReasonDestination)
		case saml2.RecipientAttr:
			return reject(ReasonRecipient)
		case saml2.StatusCodeTag:
			return reject(ReasonStatus)
		case saml2.SubjectConfirmationTag:
			return reject(ReasonConfirmationMethod)
		case saml2.NotOnOrAfterAttr:
			return reject(ReasonExpired)
		}
		return reject(ReasonLibraryRejected)
	}
	var missing saml2.ErrMissingElement
	if errors.As(err, &missing) {
		return reject(ReasonLibraryRejected)
	}
	return reject(ReasonSignatureInvalid)
}

// postChecks are notarx's own rules; they run on the element the library verified and do not
// rely on the library having checked the same thing.
func (v *verifier) postChecks(response *types.Response, request Request, now time.Time) (Claims, error) {
	c := v.config
	if response.Version != "2.0" {
		return Claims{}, reject(ReasonVersion)
	}
	if len(response.Assertions) != 1 || len(response.EncryptedAssertions) != 0 {
		return Claims{}, reject(ReasonAssertionCount)
	}
	assertion := response.Assertions[0]
	if assertion.Version != "2.0" {
		return Claims{}, reject(ReasonVersion)
	}
	if response.Status == nil || response.Status.StatusCode == nil || response.Status.StatusCode.Value != statusSuccess {
		return Claims{}, reject(ReasonStatus)
	}
	if response.Issuer == nil || response.Issuer.Value != c.EntityID || assertion.Issuer == nil || assertion.Issuer.Value != c.EntityID {
		return Claims{}, reject(ReasonIssuer)
	}
	if response.Destination != c.ACSURL {
		return Claims{}, reject(ReasonDestination)
	}
	if assertion.ID == "" || len(assertion.ID) > maxAssertionIDLength {
		return Claims{}, reject(ReasonAssertionID)
	}
	subject := assertion.Subject
	if subject == nil || subject.SubjectConfirmation == nil {
		return Claims{}, reject(ReasonConfirmationData)
	}
	confirmation := subject.SubjectConfirmation
	if confirmation.Method != subjectBearer {
		return Claims{}, reject(ReasonConfirmationMethod)
	}
	data := confirmation.SubjectConfirmationData
	if data == nil || data.NotOnOrAfter == "" {
		return Claims{}, reject(ReasonConfirmationData)
	}
	if data.Recipient != c.ACSURL {
		return Claims{}, reject(ReasonRecipient)
	}
	if err := checkInResponseTo(response.InResponseTo, data.InResponseTo, request); err != nil {
		return Claims{}, err
	}
	dataEnd, err := time.Parse(time.RFC3339, data.NotOnOrAfter)
	if err != nil {
		return Claims{}, reject(ReasonConfirmationData)
	}
	conditions := assertion.Conditions
	if conditions == nil || conditions.NotOnOrAfter == "" {
		return Claims{}, reject(ReasonConditions)
	}
	conditionsEnd, err := time.Parse(time.RFC3339, conditions.NotOnOrAfter)
	if err != nil {
		return Claims{}, reject(ReasonConditions)
	}
	if conditions.NotBefore != "" {
		start, err := time.Parse(time.RFC3339, conditions.NotBefore)
		if err != nil {
			return Claims{}, reject(ReasonConditions)
		}
		if now.Add(c.ClockSkew).Before(start) {
			return Claims{}, reject(ReasonNotYetValid)
		}
	}
	for _, end := range []time.Time{dataEnd, conditionsEnd} {
		if !now.Add(-c.ClockSkew).Before(end) {
			return Claims{}, reject(ReasonExpired)
		}
	}
	if err := v.checkAudience(conditions); err != nil {
		return Claims{}, err
	}
	nameID, format, err := v.nameID(subject)
	if err != nil {
		return Claims{}, err
	}
	end := dataEnd
	if conditionsEnd.After(end) {
		end = conditionsEnd
	}
	claims := Claims{AssertionID: assertion.ID, ReplayUntil: end.Add(c.ClockSkew), NameID: nameID, NameIDFormat: format, Attributes: map[string][]string{}}
	if assertion.AuthnStatement != nil {
		claims.SessionIndex = assertion.AuthnStatement.SessionIndex
		if context := assertion.AuthnStatement.AuthnContext; context != nil && context.AuthnContextClassRef != nil {
			claims.AuthnContext = strings.TrimSpace(context.AuthnContextClassRef.Value)
		}
	}
	collectAttributes(&claims, assertion.AttributeStatement)
	applyMapping(&claims, c.Mapping)
	return claims, nil
}

// checkInResponseTo requires the expected request id on the Response and on the
// SubjectConfirmationData. An empty expectation is an IdP-initiated response, accepted only when
// the provider allows it and then only without any InResponseTo.
func checkInResponseTo(responseTo, confirmationTo string, request Request) error {
	if request.InResponseTo == "" {
		if !request.AllowUnsolicited {
			return reject(ReasonUnsolicited)
		}
		if responseTo != "" || confirmationTo != "" {
			return reject(ReasonInResponseTo)
		}
		return nil
	}
	if responseTo != request.InResponseTo || confirmationTo != request.InResponseTo {
		return reject(ReasonInResponseTo)
	}
	return nil
}

// checkAudience requires at least one AudienceRestriction and the service provider entity id in
// every one of them (a SAML condition holds only when all restrictions hold).
func (v *verifier) checkAudience(conditions *types.Conditions) error {
	if len(conditions.AudienceRestrictions) == 0 {
		return reject(ReasonAudience)
	}
	for _, restriction := range conditions.AudienceRestrictions {
		matched := false
		for _, audience := range restriction.Audiences {
			if strings.TrimSpace(audience.Value) == v.config.SPEntityID {
				matched = true
			}
		}
		if !matched {
			return reject(ReasonAudience)
		}
	}
	return nil
}
