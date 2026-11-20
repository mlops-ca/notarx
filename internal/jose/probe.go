package jose

import (
	"context"
	"net/http"
	"slices"
)

// ProbeKey describes one key of a probed issuer.
type ProbeKey struct {
	KeyID string
	// KeyType is the JWK kty: RSA, EC or OKP.
	KeyType string
	// Algorithm is the JWK alg, or the algorithm the key type implies when the key names none.
	Algorithm string
}

// ProbeResult is what an issuer serves: where its keys come from and which keys and algorithms it offers.
type ProbeResult struct {
	JWKSURI    string
	Keys       []ProbeKey
	Algorithms []string
}

// Probe fetches the discovery document and JWKS of issuer (or reads its static JWKS) through the
// SSRF-guarded client and reports the key ids and algorithms, without verifying any token. It is
// the reachability check of the management API and of preflight (ADR 0019). A nil client selects the
// guarded client built from the issuer's CA bundle and private-network setting.
func Probe(ctx context.Context, issuer Issuer, client *http.Client) (ProbeResult, error) {
	var keys []Key
	var uri string
	if issuer.Mode == ModeStatic {
		parsed, err := ParseJWKS(issuer.StaticJWKS)
		if err != nil {
			return ProbeResult{}, err
		}
		keys = parsed
	} else {
		if client == nil {
			guarded, err := newGuardedClient(issuer.AllowPrivateNetwork, issuer.CABundlePEM)
			if err != nil {
				return ProbeResult{}, err
			}
			client = guarded
		}
		uri = issuer.JWKSURI
		if uri == "" {
			discovered, err := discover(ctx, client, issuer.Issuer)
			if err != nil {
				return ProbeResult{}, err
			}
			uri = discovered
		}
		body, err := get(ctx, client, uri)
		if err != nil {
			return ProbeResult{}, err
		}
		if keys, err = ParseJWKS(body); err != nil {
			return ProbeResult{}, err
		}
	}
	result := ProbeResult{JWKSURI: uri}
	for _, key := range keys {
		kty := keyType(key.Key)
		alg := key.Algorithm
		if alg == "" {
			alg = impliedAlgorithm(kty)
		}
		result.Keys = append(result.Keys, ProbeKey{KeyID: key.KeyID, KeyType: kty, Algorithm: alg})
		if alg != "" && !slices.Contains(result.Algorithms, alg) {
			result.Algorithms = append(result.Algorithms, alg)
		}
	}
	slices.Sort(result.Algorithms)
	return result, nil
}

func impliedAlgorithm(kty string) string {
	switch kty {
	case "RSA":
		return RS256
	case "EC":
		return ES256
	case "OKP":
		return EdDSA
	}
	return ""
}
