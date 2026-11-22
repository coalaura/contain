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
}

type Credentials struct {
	keys map[string]string
	placeholder string
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

func (credentials *Credentials) inject(request *http.Request, identity string) {
	if credentials == nil {
		return
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

		return
	}
}

func LoadCredentials(adapter string) (*Credentials, error) {
	credentials := &Credentials{keys: make(map[string]string), placeholder: "{}"}

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

		if !supported || auth.Type != "api" {
			return nil, fmt.Errorf("OpenCode provider %q: unsupported provider or credential type (only approved API-key providers are supported)", name)
		}

		if auth.Key == "" || len(auth.Key) > 16384 || strings.ContainsAny(auth.Key, "\r\n\x00") {
			return nil, fmt.Errorf("OpenCode provider %q: invalid API key", name)
		}

		credentials.keys[name] = auth.Key
		placeholders[name] = networkAuth{Type: "api", Key: "contain-host-managed"}
	}

	if len(placeholders) == 0 {
		return nil, errors.New("no supported OpenCode API-key credentials found on host")
	}

	encoded, err := json.Marshal(placeholders)
	if err != nil {
		return nil, err
	}

	credentials.placeholder = string(encoded)

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
