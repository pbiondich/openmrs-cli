// Package secrets stores profile passwords in the OS credential store:
// macOS Keychain, Windows Credential Manager, or the freedesktop Secret
// Service (GNOME Keyring / KWallet) on Linux, via zalando/go-keyring.
//
// Callers treat failures as "store unavailable" and fall back to the
// config file — headless Linux servers typically have no keyring daemon.
package secrets

import (
	"errors"

	"github.com/zalando/go-keyring"
)

// service is the credential-store service name all omrs entries live
// under; the profile name is stored as the account.
const service = "omrs"

// ErrNotFound means the profile has no entry in the credential store.
var ErrNotFound = errors.New("credential not found")

// StoreName names the credential store for user-facing messages.
func StoreName() string {
	return "OS credential store"
}

// Set stores (or updates) the password for a profile. An error means the
// store is unavailable (or rejected the write); callers should fall back
// to the config file.
func Set(profile, password string) error {
	return keyring.Set(service, profile, password)
}

// Get retrieves the password for a profile.
func Get(profile string) (string, error) {
	pw, err := keyring.Get(service, profile)
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return "", ErrNotFound
		}
		return "", err
	}
	return pw, nil
}

// Delete removes the password for a profile. Missing entries are not an error.
func Delete(profile string) error {
	err := keyring.Delete(service, profile)
	if err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return err
	}
	return nil
}
