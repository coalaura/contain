package network

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

type networkProvider struct {
	name string
	host string
	prefix string
	header string
	bearer bool
}

type networkAuth struct {
	Type string `json:"type"`
	Key string `json:"key,omitempty"`
	Access string `json:"access,omitempty"`
	Refresh string `json:"refresh,omitempty"`
	Expires int64 `json:"expires,omitempty"`
	AccountID string `json:"accountId,omitempty"`
}

type Credentials struct {
	keys map[string]string
	placeholder string
	oauth map[string]*oauthCredential
	store *oauthStore
	configuration string
}

type oauthGuestConfiguration struct {
	Provider map[string]oauthGuestProvider `json:"provider"`
}

type oauthGuestProvider struct {
	Options oauthGuestOptions `json:"options"`
}

type oauthGuestOptions struct {
	APIKey string `json:"apiKey"`
	BaseURL string `json:"baseURL,omitempty"`
}

var networkProviders = []networkProvider{
	{name: "openai", host: "api.openai.com", prefix: "/v1/", header: "Authorization", bearer: true},
	{name: "openrouter", host: "openrouter.ai", prefix: "/api/v1/", header: "Authorization", bearer: true},
	{name: "anthropic", host: "api.anthropic.com", prefix: "/v1/", header: "X-Api-Key"},
	{name: "groq", host: "api.groq.com", prefix: "/openai/v1/", header: "Authorization", bearer: true},
	{name: "mistral", host: "api.mistral.ai", prefix: "/v1/", header: "Authorization", bearer: true},
	{name: "deepseek", host: "api.deepseek.com", prefix: "/", header: "Authorization", bearer: true},
}

func (credentials *Credentials) Placeholder() string {
	return credentials.placeholder
}

func (credentials *Credentials) Configuration() string {
	return credentials.configuration
}

func (credentials *Credentials) inject(request *http.Request, identity string, gateway *Gateway) error {
	if credentials == nil {
		return nil
	}

	for name, credential := range credentials.oauth {
		if !oauthDestination(name, identity, request) {
			if name == "openai" && (identity == "api.openai.com" || identity == "chatgpt.com" && strings.HasPrefix(request.URL.Path, "/backend-api/codex/")) {
				return oauthFailure("OpenAI OAuth requires a supported ChatGPT Codex Responses endpoint")
			}

			if name == "anthropic" && identity == "api.anthropic.com" {
				return oauthFailure("Anthropic OAuth supports POST /v1/messages and /v1/messages/count_tokens only")
			}

			continue
		}

		auth, err := credential.authorization(request.Context(), credentials.store, name, gateway)
		if err != nil {
			return err
		}

		request.Header.Del("X-Api-Key")
		request.Header.Set("Authorization", "Bearer "+auth.Access)

		if name == "openai" {
			request.Header.Del("ChatGPT-Account-Id")

			if auth.AccountID != "" {
				request.Header.Set("ChatGPT-Account-Id", auth.AccountID)
			}
		} else {
			addOAuthBeta(request.Header, "oauth-2025-04-20")
		}

		return nil
	}

	for index := range networkProviders {
		provider := &networkProviders[index]
		key := credentials.keys[provider.name]
		cleanPath := path.Clean(request.URL.Path)

		if key == "" || identity != provider.host || !strings.HasPrefix(cleanPath, provider.prefix) || strings.Contains(request.URL.Path, "\\") {
			continue
		}

		if provider.bearer {
			key = "Bearer " + key
		}

		request.Header.Set(provider.header, key)

		return nil
	}

	return nil
}

func LoadCredentials(adapter, dataDirectory string) (*Credentials, error) {
	credentials := &Credentials{
		keys: make(map[string]string),
		placeholder: "{}",
		oauth: make(map[string]*oauthCredential, 2),
	}

	if adapter == "" {
		return credentials, nil
	}

	source, err := networkAuthPath()
	if err != nil {
		return nil, err
	}

	content, exists := os.LookupEnv("OPENCODE_AUTH_CONTENT")
	data := []byte(content)

	if !exists {
		data, err = readNetworkFile(source, 1024*1024)
		if errors.Is(err, os.ErrNotExist) {
			return nil, errors.New("OpenCode credentials not found on host")
		}

		if err != nil {
			return nil, err
		}
	}

	if len(data) > 1024*1024 {
		return nil, errors.New("OpenCode credential store exceeds 1 MiB")
	}

	var stored map[string]networkAuth

	err = json.Unmarshal(data, &stored)
	if err != nil {
		// JSON errors may contain credential text: do not include the underlying error.
		return nil, errors.New("invalid host OpenCode credential JSON")
	}

	placeholders := make(map[string]networkAuth, len(networkProviders))

	for name, auth := range stored {
		supported := false

		for index := range networkProviders {
			if networkProviders[index].name == name {
				supported = true

				break
			}
		}

		if !supported {
			return nil, fmt.Errorf("OpenCode provider %q: unsupported provider", name)
		}

		if auth.Type == "oauth" {
			if name != "openai" && name != "anthropic" {
				return nil, fmt.Errorf("OpenCode provider %q: OAuth is unsupported", name)
			}

			if !validOAuthAuth(auth) {
				return nil, fmt.Errorf("OpenCode provider %q: invalid OAuth credentials; log in again on the host", name)
			}

			credentials.oauth[name] = &oauthCredential{auth: auth, gate: make(chan struct{}, 1)}
			placeholders[name] = networkAuth{Type: "oauth", Access: "contain-host-managed", Refresh: "contain-host-managed", Expires: time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli()}

			continue
		}

		if auth.Type != "api" {
			return nil, fmt.Errorf("OpenCode provider %q: unsupported credential type", name)
		}

		if auth.Key == "" || len(auth.Key) > 16384 || strings.ContainsAny(auth.Key, "\r\n\x00") {
			return nil, fmt.Errorf("OpenCode provider %q: invalid API key", name)
		}

		credentials.keys[name] = auth.Key
		placeholders[name] = networkAuth{Type: "api", Key: "contain-host-managed"}
	}

	if len(placeholders) == 0 {
		return nil, errors.New("no supported OpenCode credentials found on host")
	}

	encoded, err := json.Marshal(placeholders)
	if err != nil {
		return nil, err
	}

	credentials.placeholder = string(encoded)

	if len(credentials.oauth) > 0 {
		credentials.store, err = newOAuthStore(dataDirectory, source, data, exists)
		if err != nil {
			return nil, err
		}

		// Keep the OAuth client path active, but never give its refresh code a real token.
		// The base URL also covers clients without a built-in Codex endpoint override.
		configuration := oauthGuestConfiguration{Provider: make(map[string]oauthGuestProvider, len(credentials.oauth))}

		for name := range credentials.oauth {
			options := oauthGuestOptions{APIKey: "contain-host-managed"}

			if name == "openai" {
				options.BaseURL = "https://chatgpt.com/backend-api/codex"
			}

			configuration.Provider[name] = oauthGuestProvider{Options: options}
		}

		encoded, err = json.Marshal(configuration)
		if err != nil {
			return nil, err
		}

		credentials.configuration = string(encoded)
	}

	return credentials, nil
}

func networkAuthPath() (string, error) {
	directory := os.Getenv("XDG_DATA_HOME")
	if directory == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}

		directory = filepath.Join(home, ".local", "share")
	}

	return filepath.Join(directory, "opencode", "auth.json"), nil
}

func readNetworkFile(filename string, limit int64) ([]byte, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}

	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return nil, err
	}

	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, errors.New("network configuration file is not regular or exceeds its size limit")
	}

	// Only bounded local configuration is read here, never proxied HTTP bodies.
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if int64(len(data)) > limit {
		return nil, errors.New("network configuration file exceeds its size limit")
	}

	return data, err
}
