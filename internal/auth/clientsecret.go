package auth

import "os"

// ResolveClientSecret resolves the confidential OAuth client secret for a
// profile. GIVMO_CLIENT_SECRET takes precedence over the stored secret.
func ResolveClientSecret(store *Store, profile string) (secret, source string, err error) {
	if secret := os.Getenv("GIVMO_CLIENT_SECRET"); secret != "" {
		return secret, "env", nil
	}
	secret, err = store.LoadClientSecret(profile)
	if err != nil {
		return "", "", err
	}
	if secret != "" {
		return secret, "store", nil
	}
	return "", "", nil
}
