package state

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	credsFile       = "credentials"
	credsUsername   = "x"
	credsTokenBytes = 32
)

// Credentials holds the basic-auth username/password pair that the proxy
// enforces on every incoming request.
type Credentials struct {
	Username string
	Password string
}

// LoadOrGenerateCredentials reads the credentials file at dir/credentials, or
// generates a fresh "x:<random>" pair and writes it if the file is missing.
// A present-but-malformed file is a hard error: the user may have hand-edited
// it, and silently regenerating would invalidate every provisioned sandbox.
func LoadOrGenerateCredentials(dir string) (Credentials, error) {
	path := filepath.Join(dir, credsFile)

	raw, err := os.ReadFile(path)
	if err == nil {
		return parseCredentials(raw)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return Credentials{}, fmt.Errorf("reading credentials: %w", err)
	}

	creds, err := generateCredentials()
	if err != nil {
		return Credentials{}, err
	}
	line := creds.Username + ":" + creds.Password + "\n"
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		return Credentials{}, fmt.Errorf("writing credentials: %w", err)
	}
	return creds, nil
}

func parseCredentials(raw []byte) (Credentials, error) {
	line := strings.TrimRight(string(raw), "\n")
	user, pass, ok := strings.Cut(line, ":")
	if !ok || user == "" || pass == "" || strings.ContainsAny(line, "\n\r") {
		return Credentials{}, fmt.Errorf("credentials file is malformed; delete it to regenerate")
	}
	if user != credsUsername {
		return Credentials{}, fmt.Errorf("credentials file is malformed: username must be %q", credsUsername)
	}
	return Credentials{Username: user, Password: pass}, nil
}

func generateCredentials() (Credentials, error) {
	buf := make([]byte, credsTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return Credentials{}, fmt.Errorf("reading random: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(buf)
	return Credentials{Username: credsUsername, Password: token}, nil
}
