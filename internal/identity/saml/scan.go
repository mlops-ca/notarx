package saml

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"slices"
)

// Namespaces and algorithms the scan recognises.
const (
	nsProtocol  = "urn:oasis:names:tc:SAML:2.0:protocol"
	nsAssertion = "urn:oasis:names:tc:SAML:2.0:assertion"
	nsDSig      = "http://www.w3.org/2000/09/xmldsig#"

	maxElements = 20000
	maxDepth    = 64
	// maxTextBytes bounds the character data kept per element (a PEM certificate is under 8 KiB).
	maxTextBytes = 1 << 16
)

// allowedSignatureMethods is RSA-SHA256, RSA-SHA384, RSA-SHA512 and ECDSA-SHA256; SHA-1 is refused.
var allowedSignatureMethods = []string{
	"http://www.w3.org/2001/04/xmldsig-more#rsa-sha256",
	"http://www.w3.org/2001/04/xmldsig-more#rsa-sha384",
	"http://www.w3.org/2001/04/xmldsig-more#rsa-sha512",
	"http://www.w3.org/2001/04/xmldsig-more#ecdsa-sha256",
}

var allowedDigestMethods = []string{
	"http://www.w3.org/2001/04/xmlenc#sha256",
	"http://www.w3.org/2001/04/xmldsig-more#sha384",
	"http://www.w3.org/2001/04/xmlenc#sha512",
}

type element struct {
	space, local string
	attrs        []xml.Attr
	text         string
	parent       *element
	children     []*element
}

func (e *element) attr(local string) string {
	for _, a := range e.attrs {
		if a.Name.Space == "" && a.Name.Local == local {
			return a.Value
		}
	}
	return ""
}

func (e *element) is(space, local string) bool { return e.space == space && e.local == local }

func (e *element) child(space, local string) []*element {
	var found []*element
	for _, c := range e.children {
		if c.is(space, local) {
			found = append(found, c)
		}
	}
	return found
}

// parseStructure reads the document into a small element tree with encoding/xml. It rejects
// document type declarations (so no entity is ever defined or expanded), processing
// instructions other than the XML declaration, and documents beyond the element and depth caps.
func parseStructure(raw []byte) (*element, error) {
	decoder := xml.NewDecoder(bytes.NewReader(raw))
	decoder.Strict = true
	decoder.Entity = nil
	var root, current *element
	elements, depth := 0, 0
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, reject(ReasonMalformed)
		}
		switch t := token.(type) {
		case xml.Directive:
			return nil, reject(ReasonDTD)
		case xml.ProcInst:
			if t.Target != "xml" {
				return nil, reject(ReasonMalformed)
			}
		case xml.StartElement:
			elements++
			depth++
			if elements > maxElements || depth > maxDepth {
				return nil, reject(ReasonMalformed)
			}
			node := &element{space: t.Name.Space, local: t.Name.Local, attrs: t.Attr, parent: current}
			if current == nil {
				if root != nil {
					return nil, reject(ReasonMalformed)
				}
				root = node
			} else {
				current.children = append(current.children, node)
			}
			current = node
		case xml.CharData:
			if current != nil && len(current.text) < maxTextBytes {
				current.text += string(t)
			}
		case xml.EndElement:
			depth--
			if current != nil {
				current = current.parent
			}
		}
	}
	if root == nil || current != nil {
		return nil, reject(ReasonMalformed)
	}
	return root, nil
}

func (e *element) walk(visit func(*element)) {
	visit(e)
	for _, c := range e.children {
		c.walk(visit)
	}
}

// checkStructure applies the wrapping defences that do not need a signature check: exactly one
// plain Assertion that is a direct child of the Response, no EncryptedAssertion, unique IDs, and
// signatures only where the profile allows them (the Response and the Assertion), each with
// exactly one Reference to the ID of the element that carries it and allowlisted algorithms.
func checkStructure(root *element) (assertion *element, err error) {
	if !root.is(nsProtocol, "Response") {
		return nil, reject(ReasonMalformed)
	}
	var assertions, encrypted, signatures []*element
	seen := map[string]bool{}
	duplicate := false
	root.walk(func(e *element) {
		if id := e.attr("ID"); id != "" {
			if seen[id] {
				duplicate = true
			}
			seen[id] = true
		}
		switch {
		case e.is(nsAssertion, "Assertion"):
			assertions = append(assertions, e)
		case e.is(nsAssertion, "EncryptedAssertion"):
			encrypted = append(encrypted, e)
		case e.is(nsDSig, "Signature"):
			signatures = append(signatures, e)
		}
	})
	switch {
	case len(encrypted) > 0:
		return nil, reject(ReasonEncryptedAssertion)
	case len(assertions) != 1:
		return nil, reject(ReasonAssertionCount)
	case assertions[0].parent != root:
		return nil, reject(ReasonAssertionPlacement)
	case duplicate:
		return nil, reject(ReasonDuplicateID)
	case len(signatures) == 0:
		return nil, reject(ReasonUnsigned)
	case len(signatures) > 2:
		return nil, reject(ReasonSignatureCount)
	}
	assertion = assertions[0]
	signedParents := map[*element]bool{}
	for _, signature := range signatures {
		parent := signature.parent
		if parent != root && parent != assertion {
			return nil, reject(ReasonSignaturePlacement)
		}
		if signedParents[parent] {
			return nil, reject(ReasonSignatureCount)
		}
		signedParents[parent] = true
		if err := checkSignature(signature, parent); err != nil {
			return nil, err
		}
	}
	return assertion, nil
}

func checkSignature(signature, parent *element) error {
	signedInfo := signature.child(nsDSig, "SignedInfo")
	if len(signedInfo) != 1 {
		return reject(ReasonSignatureCount)
	}
	references := signedInfo[0].child(nsDSig, "Reference")
	if len(references) != 1 {
		return reject(ReasonReferenceCount)
	}
	id := parent.attr("ID")
	if id == "" || references[0].attr("URI") != "#"+id {
		return reject(ReasonReferenceURI)
	}
	methods := signedInfo[0].child(nsDSig, "SignatureMethod")
	if len(methods) != 1 || !slices.Contains(allowedSignatureMethods, methods[0].attr("Algorithm")) {
		return reject(ReasonSignatureAlgorithm)
	}
	digests := references[0].child(nsDSig, "DigestMethod")
	if len(digests) != 1 || !slices.Contains(allowedDigestMethods, digests[0].attr("Algorithm")) {
		return reject(ReasonDigestAlgorithm)
	}
	return nil
}
