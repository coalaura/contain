package network

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

const oauthRefreshMargin = time.Minute

type oauthFailure string

type oauthCredential struct {
	auth networkAuth
	gate chan struct{}
	loaded bool
	retryAfter time.Time
	failure error
}

type oauthClaims struct {
	AccountID string `json:"chatgpt_account_id"`
	Authentication oauthAccount `json:"https://api.openai.com/auth"`
	Organizations []oauthOrganization `json:"organizations"`
}

type oauthAccount struct {
	AccountID string `json:"chatgpt_account_id"`
}

type oauthOrganization struct {
	ID string `json:"id"`
}

func (failure oauthFailure) Error() string {
	return string(failure)
}

func (credential *oauthCredential) authorization(ctx context.Context, store *oauthStore, name string, gateway *Gateway) (networkAuth, error) {
	select {
	case credential.gate <- struct{}{}:
		defer func() {
			<-credential.gate
		}()
	case <-ctx.Done():
		return networkAuth{}, ctx.Err()
	}

	if time.Now().Before(credential.retryAfter) {
		return networkAuth{}, credential.failure
	}

	if credential.loaded && oauthFresh(credential.auth) {
		return credential.auth, nil
	}

	auth, err := store.authorization(ctx, name, gateway)
	if err != nil {
		credential.failure = err
		credential.retryAfter = time.Now().Add(30 * time.Second)

		return networkAuth{}, err
	}

	credential.auth = auth
	credential.loaded = true

	return auth, nil
}

func oauthFresh(auth networkAuth) bool {
	return auth.Access != "" && auth.Expires > time.Now().Add(oauthRefreshMargin).UnixMilli()
}

func validOAuthAuth(auth networkAuth) bool {
	return auth.Type == "oauth" && validOAuthToken(auth.Refresh) && (auth.Access == "" || validOAuthToken(auth.Access)) && auth.Expires >= 0 && (auth.AccountID == "" || validOAuthToken(auth.AccountID))
}

func validOAuthToken(value string) bool {
	if len(value) == 0 || len(value) > 16384 {
		return false
	}

	for index := range len(value) {
		if value[index] < 33 || value[index] > 126 {
			return false
		}
	}

	return true
}

func oauthDestination(name, identity string, request *http.Request) bool {
	// Exact decoded paths only: credentials never authorize other provider APIs or redirects.
	if request.Method != http.MethodPost || request.URL.RawPath != "" {
		return false
	}

	switch name {
	case "openai":
		return identity == "chatgpt.com" && (request.URL.Path == "/backend-api/codex/responses" || request.URL.Path == "/backend-api/codex/responses/compact")
	case "anthropic":
		return identity == "api.anthropic.com" && (request.URL.Path == "/v1/messages" || request.URL.Path == "/v1/messages/count_tokens")
	default:
		return false
	}
}

func addOAuthBeta(headers http.Header, required string) {
	values := headers.Values("Anthropic-Beta")

	for _, value := range values {
		for item := range strings.SplitSeq(value, ",") {
			if strings.TrimSpace(item) == required {
				return
			}
		}
	}

	values = append(values, required)
	headers.Set("Anthropic-Beta", strings.Join(values, ","))
}

func oauthAccountID(token string) string {
	_, remainder, found := strings.Cut(token, ".")
	if !found {
		return ""
	}

	payload, _, found := strings.Cut(remainder, ".")
	if !found || len(payload) > 16384 {
		return ""
	}

	data, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return ""
	}

	var claims oauthClaims

	err = json.Unmarshal(data, &claims)
	if err != nil {
		return ""
	}

	account := claims.AccountID
	if account == "" {
		account = claims.Authentication.AccountID
	}

	if account == "" && len(claims.Organizations) > 0 {
		account = claims.Organizations[0].ID
	}

	// This is an account selector from the host's token, never proof of upstream identity.
	if !validOAuthToken(account) {
		return ""
	}

	return account
}

func oauthPublicError(err error) string {
	failure, known := errors.AsType[oauthFailure](err)
	if known {
		return failure.Error()
	}

	if errors.Is(err, errNetworkDenied) {
		return "host OAuth refresh denied by network policy (including refresh DNS)"
	}

	return "host OAuth authorization unavailable; check host login, refresh connectivity and credential storage"
}
