package auth

import "os"

// ResolveClientID resolves the confidential OAuth client id. GIVMO_CLIENT_ID
// overrides the built-in first-party default (ClientID).
func ResolveClientID() string {
	if id := os.Getenv("GIVMO_CLIENT_ID"); id != "" {
		return id
	}
	return ClientID
}

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
