package network

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

const oauthResponseLimit = 64 * 1024

type oauthRefreshRequest struct {
	GrantType    string `json:"grant_type"`
	ClientID     string `json:"client_id"`
	RefreshToken string `json:"refresh_token"`
}

type oauthRefreshResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	ExpiresIn    int64  `json:"expires_in"`
	TokenType    string `json:"token_type"`
}

func (gateway *Gateway) refreshOAuth(ctx context.Context, name string, auth networkAuth) (networkAuth, error) {
	host := "auth.openai.com"
	endpoint := "https://auth.openai.com/oauth/token"
	contentType := "application/x-www-form-urlencoded"

	form := url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {"app_EMoamEEZ73f0CkXaXp7hrann"},
		"refresh_token": {auth.Refresh},
	}

	var body io.Reader = strings.NewReader(form.Encode())

	if name == "anthropic" {
		host = "console.anthropic.com"
		endpoint = "https://console.anthropic.com/v1/oauth/token"
		contentType = "application/json"

		payload := oauthRefreshRequest{
			GrantType: "refresh_token",
			// Public OAuth client ID from opencode-anthropic-auth@0.0.13/index.mjs; refresh tokens must belong to this client.
			ClientID:     "9d1c250a-e61b-44d9-88ed-5944d1962f5e",
			RefreshToken: auth.Refresh,
		}

		encoded, err := json.Marshal(payload)
		if err != nil {
			return networkAuth{}, errors.New("encode host OAuth refresh")
		}

		body = bytes.NewReader(encoded)
	} else if name != "openai" {
		return networkAuth{}, errors.New("unsupported OAuth provider")
	}

	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, protocol, address string) (net.Conn, error) {
			if protocol != "tcp" || address != net.JoinHostPort(host, "443") {
				return nil, errNetworkDenied
			}

			return gateway.dialOAuth(ctx, host)
		},
		TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12, ServerName: host},
		DisableKeepAlives:      true,
		DisableCompression:     true,
		TLSHandshakeTimeout:    networkDialTimeout,
		ResponseHeaderTimeout:  networkDialTimeout,
		MaxResponseHeaderBytes: 16 * 1024,
		WriteBufferSize:        4096,
		ReadBufferSize:         4096,
	}

	defer transport.CloseIdleConnections()

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return networkAuth{}, errors.New("create host OAuth refresh")
	}

	request.Header.Set("Content-Type", contentType)
	request.Header.Set("Accept", "application/json")

	// RoundTrip never follows a redirect and never retries this POST with its refresh token.
	response, err := transport.RoundTrip(request)
	if err != nil {
		if errors.Is(err, errNetworkDenied) {
			return networkAuth{}, errNetworkDenied
		}

		return networkAuth{}, errors.New("host OAuth refresh connection failed")
	}

	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return networkAuth{}, errors.New("host OAuth refresh rejected; sign in again on the host if credentials were revoked")
	}

	return decodeOAuthRefresh(response.Body, name, auth)
}

func (gateway *Gateway) dialOAuth(ctx context.Context, host string) (net.Conn, error) {
	addresses, err := gateway.lookupDNSAddresses(ctx, host)
	if err != nil {
		return nil, err
	}

	if len(addresses) > 64 {
		return nil, errors.New("too many OAuth endpoint addresses")
	}

	var lastErr = errNetworkDenied

	for _, address := range addresses {
		destination := netip.AddrPortFrom(address.Unmap(), 443)

		connection, dialErr := gateway.dial(ctx, "tcp", destination)
		if dialErr != nil {
			lastErr = dialErr

			continue
		}

		return &networkHTTPSConn{Conn: connection, timeout: networkDialTimeout, gateway: gateway}, nil
	}

	return nil, lastErr
}

func decodeOAuthRefresh(reader io.Reader, name string, previous networkAuth) (networkAuth, error) {
	limited := &io.LimitedReader{R: reader, N: oauthResponseLimit + 1}
	decoder := json.NewDecoder(limited)

	var response oauthRefreshResponse

	err := decoder.Decode(&response)
	if err != nil || limited.N <= 1 {
		return networkAuth{}, errors.New("invalid or oversized host OAuth refresh response")
	}

	var extra json.RawMessage

	err = decoder.Decode(&extra)
	if !errors.Is(err, io.EOF) || limited.N <= 1 {
		return networkAuth{}, errors.New("invalid or oversized host OAuth refresh response")
	}

	if !validOAuthToken(response.AccessToken) || response.ExpiresIn < 120 || response.ExpiresIn > 365*24*60*60 || (response.TokenType != "" && !strings.EqualFold(response.TokenType, "Bearer")) {
		return networkAuth{}, errors.New("invalid host OAuth refresh fields")
	}

	if response.RefreshToken != "" {
		if !validOAuthToken(response.RefreshToken) {
			return networkAuth{}, errors.New("invalid rotated OAuth refresh token")
		}

		previous.Refresh = response.RefreshToken
	}

	previous.Access = response.AccessToken
	previous.Expires = time.Now().Add(time.Duration(response.ExpiresIn) * time.Second).UnixMilli()

	if name == "openai" {
		account := oauthAccountID(response.IDToken)
		if account == "" {
			account = oauthAccountID(response.AccessToken)
		}

		if account != "" {
			previous.AccountID = account
		}
	}

	return previous, nil
}
