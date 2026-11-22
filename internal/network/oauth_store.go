package network

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

type oauthStore struct {
	directory   string
	source      string
	environment map[string]json.RawMessage
}

type oauthRotation struct {
	Source   string      `json:"source"`
	Original string      `json:"original"`
	Auth     networkAuth `json:"auth"`
}

func (store *oauthStore) authorization(ctx context.Context, name string, gateway *Gateway) (networkAuth, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	lock, err := lockOAuthStore(ctx, filepath.Join(store.directory, "lock"))
	if err != nil {
		return networkAuth{}, errors.New("lock host OAuth rotation store")
	}

	defer lock.Close()

	stored, err := store.read()
	if err != nil {
		return networkAuth{}, err
	}

	var original networkAuth

	err = json.Unmarshal(stored[name], &original)
	if err != nil || !validOAuthAuth(original) {
		return networkAuth{}, errors.New("host OAuth credentials changed or are invalid; restart contain after signing in")
	}

	rotations, err := store.rotations()
	if err != nil {
		return networkAuth{}, err
	}

	auth := original

	fingerprint := oauthFingerprint(original)
	rotation := rotations[name]

	if rotation.Source == store.source && rotation.Original == fingerprint {
		if !validOAuthAuth(rotation.Auth) {
			return networkAuth{}, errors.New("invalid saved host OAuth rotation")
		}

		auth = rotation.Auth
	}

	if name == "openai" && auth.AccountID == "" {
		auth.AccountID = oauthAccountID(auth.Access)
	}

	if !oauthFresh(auth) {
		auth, err = gateway.refreshOAuth(ctx, name, auth)
		if err != nil {
			return networkAuth{}, err
		}

		rotations[name] = oauthRotation{Source: store.source, Original: fingerprint, Auth: auth}

		// Journal the rotated token before updating OpenCode, so a failed writeback
		// cannot lose the provider's newly issued, single-use refresh token.
		err = writeOAuthJSON(filepath.Join(store.directory, "tokens.json"), rotations)
		if err != nil {
			return networkAuth{}, errors.New("persist host OAuth rotation; sign in again if the refresh token was rotated")
		}
	}

	if store.environment == nil && auth != original {
		err = store.persist(name, original, auth)
		if err != nil {
			return networkAuth{}, err
		}
	}

	return auth, nil
}

func (store *oauthStore) read() (map[string]json.RawMessage, error) {
	if store.environment != nil {
		return store.environment, nil
	}

	data, err := readNetworkFile(store.source, 1024*1024)
	if err != nil {
		return nil, errors.New("read host OpenCode credentials")
	}

	var stored map[string]json.RawMessage

	err = json.Unmarshal(data, &stored)
	if err != nil || stored == nil {
		return nil, errors.New("invalid host OpenCode credential JSON")
	}

	return stored, nil
}

func (store *oauthStore) rotations() (map[string]oauthRotation, error) {
	rotations := make(map[string]oauthRotation, 2)

	data, err := readNetworkFile(filepath.Join(store.directory, "tokens.json"), 256*1024)
	if errors.Is(err, os.ErrNotExist) {
		return rotations, nil
	}

	if err != nil {
		return nil, errors.New("read host OAuth rotation store")
	}

	err = json.Unmarshal(data, &rotations)
	if err != nil || rotations == nil || len(rotations) > 2 {
		return nil, errors.New("invalid host OAuth rotation store")
	}

	return rotations, nil
}

func (store *oauthStore) persist(name string, original, auth networkAuth) error {
	// Re-read before replacement: retain unrelated providers and unknown fields,
	// and refuse to overwrite a login changed since this refresh began.
	stored, err := store.read()
	if err != nil {
		return err
	}

	var current networkAuth

	err = json.Unmarshal(stored[name], &current)
	if err != nil || current != original {
		return errors.New("host OAuth login changed during refresh; restart contain")
	}

	var fields map[string]json.RawMessage

	err = json.Unmarshal(stored[name], &fields)
	if err != nil {
		return errors.New("invalid host OAuth entry")
	}

	fields["access"], _ = json.Marshal(auth.Access)
	fields["refresh"], _ = json.Marshal(auth.Refresh)
	fields["expires"], _ = json.Marshal(auth.Expires)

	if auth.AccountID != "" {
		fields["accountId"], _ = json.Marshal(auth.AccountID)
	}

	stored[name], err = json.Marshal(fields)
	if err != nil {
		return errors.New("encode host OAuth entry")
	}

	err = writeOAuthJSON(store.source, stored)
	if err != nil {
		return errors.New("write host OpenCode credentials; rotated token retained in host rotation store")
	}

	return nil
}

func newOAuthStore(dataDirectory, source string, data []byte, environment bool) (*oauthStore, error) {
	directory := filepath.Join(dataDirectory, "network-auth")

	err := os.MkdirAll(directory, 0700)
	if err != nil {
		return nil, errors.New("create host OAuth rotation directory")
	}

	err = protectNetworkDirectory(directory)
	if err != nil {
		return nil, errors.New("protect host OAuth rotation directory")
	}

	store := &oauthStore{directory: directory, source: source}

	if environment {
		store.source = "OPENCODE_AUTH_CONTENT"
		err = json.Unmarshal(data, &store.environment)
	} else {
		store.source, err = filepath.Abs(source)
		if err == nil {
			store.source, err = filepath.EvalSymlinks(store.source)
		}
	}

	if err != nil {
		return nil, errors.New("resolve host OAuth credential source")
	}

	return store, nil
}

func oauthFingerprint(auth networkAuth) string {
	encoded, _ := json.Marshal(auth)
	digest := sha256.Sum256(encoded)

	return hex.EncodeToString(digest[:])
}

func writeOAuthJSON(filename string, value any) error {
	file, err := os.CreateTemp(filepath.Dir(filename), ".contain-auth-*")
	if err != nil {
		return err
	}

	temporary := file.Name()

	defer os.Remove(temporary)
	defer file.Close()

	err = protectOAuthFile(temporary)
	if err != nil {
		return err
	}

	err = json.NewEncoder(file).Encode(value)
	if err != nil {
		return err
	}

	info, err := file.Stat()
	if err != nil {
		return err
	}

	if info.Size() > 1024*1024 {
		return errors.New("host OAuth credential file exceeds size limit")
	}

	err = file.Sync()
	if err != nil {
		return err
	}

	err = file.Close()
	if err != nil {
		return err
	}

	return replaceOAuthFile(temporary, filename)
}

func lockOAuthStore(ctx context.Context, filename string) (*os.File, error) {
	file, err := os.OpenFile(filename, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}

	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		locked, lockErr := tryOAuthLock(file)
		if lockErr != nil {
			file.Close()

			return nil, lockErr
		}

		if locked {
			return file, nil
		}

		select {
		case <-ctx.Done():
			file.Close()

			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}
