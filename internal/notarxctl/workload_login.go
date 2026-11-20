package notarxctl

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const (
	tokenExchangePath       = "/auth/token-exchange"
	tokenExchangeGrantType  = "urn:ietf:params:oauth:grant-type:token-exchange"
	tokenExchangeSubjectJWT = "urn:ietf:params:oauth:token-type:jwt"
	workloadTokenLimit      = 16 << 10
)

// loginWithWorkloadToken exchanges a workload JWT for a short-lived notarx token (RFC 8693, ADR 0019)
// and stores it like any other session token: in a profile, or in a new private file.
func loginWithWorkloadToken(ctx context.Context, env *commandContext, tokenFile, policy, output string) error {
	if strings.TrimSpace(tokenFile) == "" {
		return &UsageError{Message: "--workload-token-file cannot be empty", Hint: "pass the file the platform wrote the workload token to"}
	}
	data, err := readWorkloadFile("workload-token-file", tokenFile)
	if err != nil {
		return err
	}
	subject := strings.TrimSpace(string(data))
	if subject == "" || len(subject) > workloadTokenLimit {
		return &UsageError{Message: "--workload-token-file must hold one JWT of at most 16 KiB", Hint: "for GitHub Actions fetch it with ACTIONS_ID_TOKEN_REQUEST_URL and the audience of this server"}
	}
	env.addKnownSecret(subject)

	var profile string
	var target string
	var destination *secretDestination
	if env.flagSet["secret-output"] {
		if env.flagSet["profile"] {
			return &UsageError{Message: "choose one of --profile or --secret-output for the session token", Hint: "--profile stores it in the config file; --secret-output writes it to a new private file"}
		}
		if destination, err = prepareSecretDestination(output); err != nil {
			return err
		}
	} else {
		var stored profileConfig
		if profile, stored, err = explicitProfile(env); err != nil {
			return err
		}
		if target, err = checkProfileTarget(env, profile, stored); err != nil {
			return err
		}
	}
	discard := func() {
		if destination != nil {
			destination.discard()
		}
	}
	client, err := env.client()
	if err != nil {
		discard()
		return err
	}
	form := url.Values{"grant_type": {tokenExchangeGrantType}, "subject_token": {subject}, "subject_token_type": {tokenExchangeSubjectJWT}}
	if policy != "" {
		form.Set("policy", policy)
	}
	encoded := form.Encode()
	var exchanged struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		ExpiresIn   int    `json:"expires_in"`
	}
	// The same JWT may be exchanged again after a lost response, unless the policy is single use, where a
	// repeat is refused: the exchange is therefore not retried.
	if _, err := client.sendJSON(ctx, http.MethodPost, tokenExchangePath, requestOptions{
		stream:      func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader(encoded)), nil },
		length:      int64(len(encoded)),
		contentType: "application/x-www-form-urlencoded",
	}, &exchanged); err != nil {
		discard()
		return err
	}
	if exchanged.AccessToken == "" {
		discard()
		return &APIError{Code: "invalid_response", HTTPStatus: http.StatusOK, Message: "the server returned no access token"}
	}
	env.addKnownSecret(exchanged.AccessToken)
	result := authLoginResult{
		Server:             redactServerURL(env.settings.Server, env.settings.Token, exchanged.AccessToken, subject),
		AuthenticationKind: "workload_federation",
		ExpiresIn:          exchanged.ExpiresIn,
	}
	if destination != nil {
		result.SecretOutput = &secretDelivery{Path: destination.path, Mode: "0600", Delivered: true}
		if err := destination.deliver(exchanged.AccessToken); err != nil {
			result.SecretOutput.Delivered = false
			return &resultError{err: &deliveryError{message: err.Error()}, data: result, hint: "the exchange succeeded but its token was not saved; it expires on its own, so run the login again"}
		}
		return env.render(result, nil, func(w io.Writer) error {
			_, err := fmt.Fprintf(w, "authenticated by workload federation against %s; token (valid %ds) written to %s (mode 0600) and not shown\n", result.Server, result.ExpiresIn, destination.path)
			return err
		})
	}
	if err := storeProfileToken(env, profile, target, exchanged.AccessToken); err != nil {
		return err
	}
	result.Profile = profile
	return env.render(result, nil, func(w io.Writer) error {
		_, err := fmt.Fprintf(w, "authenticated by workload federation against %s; token (valid %ds) stored in profile %q\n", result.Server, result.ExpiresIn, result.Profile)
		return err
	})
}
