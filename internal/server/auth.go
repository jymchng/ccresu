package server

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// Token file constants.
const (
	// tokenBytes is the entropy behind a generated token.
	tokenBytes = 32
	// minTokenLength rejects a truncated or placeholder token file rather than
	// silently running with a token an attacker could guess.
	minTokenLength = 16
	// tokenFileMode is the only permission the token file may have. The token is
	// a credential: group and world must not be able to read it.
	tokenFileMode = 0o600
	// tokenDirMode is used for the directory the token is created in.
	tokenDirMode = 0o700
)

// bearerPrefix is the only credential scheme accepted. The token is read from
// the Authorization header, never from a query parameter or a cookie, which is
// what makes a cross-site request unable to supply it.
const bearerPrefix = "Bearer "

// ensureToken reads the bearer token, creating it on first start.
//
// The file is written 0600 and any pre-existing looser mode is tightened, so a
// user who copied the file around cannot accidentally leave the credential
// group- or world-readable.
func ensureToken(path string) (string, error) {
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		return existingToken(path, data)
	case !os.IsNotExist(err):
		return "", fmt.Errorf("read api token %s: %w", path, err)
	}

	token, err := generateToken()
	if err != nil {
		return "", fmt.Errorf("generate api token: %w", err)
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, tokenDirMode); err != nil {
			return "", fmt.Errorf("create api token directory %s: %w", dir, err)
		}
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, tokenFileMode)
	if err != nil {
		if os.IsExist(err) {
			// Another process created it between the read and the create; its
			// token wins so both processes agree.
			return ensureToken(path)
		}
		return "", fmt.Errorf("create api token %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()
	if _, err := file.WriteString(token + "\n"); err != nil {
		return "", fmt.Errorf("write api token %s: %w", path, err)
	}
	if err := file.Sync(); err != nil {
		return "", fmt.Errorf("flush api token %s: %w", path, err)
	}
	// A restrictive umask can only remove bits, but chmod makes the mode exact
	// regardless of how the file was created.
	if err := os.Chmod(path, tokenFileMode); err != nil {
		return "", fmt.Errorf("set permissions on api token %s: %w", path, err)
	}
	return token, nil
}

// existingToken validates and, when needed, tightens a token file that is
// already on disk.
func existingToken(path string, data []byte) (string, error) {
	token := strings.TrimSpace(string(data))
	if len(token) < minTokenLength {
		return "", fmt.Errorf("%w: %s must hold at least %d characters", ErrNoToken, path, minTokenLength)
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("stat api token %s: %w", path, err)
	}
	if mode := info.Mode().Perm(); mode != tokenFileMode {
		if err := os.Chmod(path, tokenFileMode); err != nil {
			return "", fmt.Errorf("tighten permissions on api token %s: %w", path, err)
		}
	}
	return token, nil
}

// generateToken returns a fresh 256-bit token as lowercase hex.
func generateToken() (string, error) {
	buf := make([]byte, tokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// authenticate enforces the bearer token on state-changing requests.
//
// Read-only requests are deliberately exempt: the token is what protects writes,
// the listener is loopback-only, and a browser cannot read the response of
// another origin because of the allowlist. The comparison is constant-time so a
// wrong token cannot be recovered byte by byte from timing.
func (s *Server) authenticate(r *http.Request) *apiError {
	if !isMutating(r.Method) {
		return nil
	}
	header := r.Header.Get("Authorization")
	if header == "" || !strings.HasPrefix(header, bearerPrefix) {
		return errUnauthorized("this request changes state and needs an Authorization: Bearer <token> header")
	}
	presented := strings.TrimSpace(strings.TrimPrefix(header, bearerPrefix))
	if subtle.ConstantTimeCompare([]byte(presented), []byte(s.token)) != 1 {
		return errUnauthorized("the bearer token is not valid")
	}
	return nil
}

// isMutating reports whether method changes state and therefore needs the token.
func isMutating(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	default:
		return true
	}
}
