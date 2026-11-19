package authorization

import (
	"encoding/base64"
	"testing"
)

func fakeJWT(payload string) string {
	return "e30." + base64.RawURLEncoding.EncodeToString([]byte(payload)) + ".sig"
}

func TestUnverifiedIssuerReadsAndNormalizesTheClaimOnly(t *testing.T) {
	for token, want := range map[string]string{
		fakeJWT(`{"iss":"https://idp.example.test/"}`):  "https://idp.example.test",
		fakeJWT(`{"iss":" https://idp.example.test "}`): "https://idp.example.test",
		fakeJWT(`{"sub":"x"}`):                          "",
		fakeJWT(`{"iss":5}`):                            "",
		fakeJWT("not json"):                             "",
		"e30.!!!.sig":                                   "",
		"only.two":                                      "",
		"":                                              "",
	} {
		if got := UnverifiedIssuer(token); got != want {
			t.Errorf("UnverifiedIssuer(%q)=%q want %q", token, got, want)
		}
	}
}
