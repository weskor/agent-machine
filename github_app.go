package main

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// githubAppEnvFromEnvironment mints a short-lived installation token for Pi/gh.
// The token is only passed through process env and is never written to logs or run records.
func githubAppEnvFromEnvironment() (map[string]string, string, error) {
	appID := strings.TrimSpace(os.Getenv("GITHUB_APP_ID"))
	installationID := strings.TrimSpace(os.Getenv("GITHUB_APP_INSTALLATION_ID"))
	keyPath := strings.TrimSpace(os.Getenv("GITHUB_APP_PRIVATE_KEY_PATH"))
	if appID == "" && installationID == "" && keyPath == "" {
		return nil, "default_gh_auth", nil
	}
	if appID == "" || installationID == "" || keyPath == "" {
		return nil, "", errors.New("GITHUB_APP_ID, GITHUB_APP_INSTALLATION_ID, and GITHUB_APP_PRIVATE_KEY_PATH must all be set to use GitHub App auth")
	}
	token, err := mintGitHubInstallationToken(appID, installationID, keyPath)
	if err != nil {
		return nil, "", err
	}
	return map[string]string{"GH_TOKEN": token, "GITHUB_TOKEN": token}, "github_app_installation", nil
}

const githubAppPRAuthorLogin = "app/compound-symphony-bot"
const githubAppRESTPRAuthorLogin = "compound-symphony-bot[bot]"
const githubAppBotName = "compound-symphony-bot[bot]"
const githubAppBotEmail = "285402021+compound-symphony-bot[bot]@users.noreply.github.com"

func isExpectedGitHubAppPRAuthor(login string) bool {
	switch strings.TrimSpace(login) {
	case "":
		return false
	case githubAppPRAuthorLogin, githubAppRESTPRAuthorLogin:
		return true
	default:
		return false
	}
}

func expectedGitHubAppPRAuthorLogins() string {
	return githubAppPRAuthorLogin + " or " + githubAppRESTPRAuthorLogin
}

func isExpectedGitHubAppCommitAuthor(author prCommitAuthor) bool {
	return strings.TrimSpace(author.Name) == githubAppBotName && strings.TrimSpace(author.Email) == githubAppBotEmail
}

func commitAuthorInvariantBlockReason(pr pullRequestSummary) string {
	for _, commit := range pr.Commits {
		if isExpectedGitHubAppCommitAuthor(commit.Author) {
			continue
		}
		name := emptyAsUnknown(strings.TrimSpace(commit.Author.Name))
		email := emptyAsUnknown(strings.TrimSpace(commit.Author.Email))
		oid := strings.TrimSpace(commit.OID)
		if oid != "" {
			return fmt.Sprintf("commit author for %s is %s <%s>; expected %s <%s>", oid, name, email, githubAppBotName, githubAppBotEmail)
		}
		return fmt.Sprintf("commit author is %s <%s>; expected %s <%s>", name, email, githubAppBotName, githubAppBotEmail)
	}
	return ""
}

func configureGitHubAppCommitIdentity(workspace string, timeout time.Duration) error {
	return shellWithTimeout(
		"git config user.name "+shellQuote(githubAppBotName)+" && git config user.email "+shellQuote(githubAppBotEmail),
		workspace,
		timeout,
	)
}

func mintGitHubInstallationToken(appID, installationID, privateKeyPath string) (string, error) {
	jwt, err := githubAppJWT(appID, privateKeyPath)
	if err != nil {
		return "", err
	}
	url := fmt.Sprintf("https://api.github.com/app/installations/%s/access_tokens", installationID)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader([]byte("{}")))
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(res.Body)
	if res.StatusCode >= 300 {
		return "", fmt.Errorf("GitHub App installation token request failed: HTTP %d: %s", res.StatusCode, string(data))
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", err
	}
	if out.Token == "" {
		return "", errors.New("GitHub App installation token response did not include a token")
	}
	return out.Token, nil
}

func githubAppJWT(appID, privateKeyPath string) (string, error) {
	key, err := readRSAPrivateKey(privateKeyPath)
	if err != nil {
		return "", err
	}
	now := time.Now().Add(-30 * time.Second).Unix()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claimsJSON, _ := json.Marshal(map[string]any{"iat": now, "exp": now + 9*60, "iss": appID})
	claims := base64.RawURLEncoding.EncodeToString(claimsJSON)
	signingInput := header + "." + claims
	digest := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

func readRSAPrivateKey(path string) (*rsa.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil && !filepath.IsAbs(path) {
		data, err = os.ReadFile(filepath.Join("../..", path))
	}
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("GitHub App private key is not PEM encoded")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("GitHub App private key is not an RSA private key")
	}
	return key, nil
}
