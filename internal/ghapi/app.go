package ghapi

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

	sh "github.com/weskor/agent-machine/internal/shell"
)

// AppEnvFromEnvironment mints a short-lived installation token for Pi/gh.
// The token is only passed through process env and is never written to logs or run records.
func AppEnvFromEnvironment() (map[string]string, string, error) {
	appID := strings.TrimSpace(os.Getenv("GITHUB_APP_ID"))
	installationID := strings.TrimSpace(os.Getenv("GITHUB_APP_INSTALLATION_ID"))
	keyPath := strings.TrimSpace(os.Getenv("GITHUB_APP_PRIVATE_KEY_PATH"))
	if appID == "" && installationID == "" && keyPath == "" {
		return nil, "default_gh_auth", nil
	}
	if appID == "" || installationID == "" || keyPath == "" {
		return nil, "", errors.New("GITHUB_APP_ID, GITHUB_APP_INSTALLATION_ID, and GITHUB_APP_PRIVATE_KEY_PATH must all be set to use GitHub App auth")
	}
	token, err := MintInstallationToken(appID, installationID, keyPath)
	if err != nil {
		return nil, "", err
	}
	return map[string]string{"GH_TOKEN": token, "GITHUB_TOKEN": token}, "github_app_installation", nil
}

const defaultAppSlug = "agent-machine-bot"

var DefaultAppIdentity = AppIdentity{Slug: defaultAppSlug, Source: "default GitHub App slug"}

const (
	AppPRAuthorLogin     = "app/agent-machine-bot"
	AppRESTPRAuthorLogin = "agent-machine-bot[bot]"
	AppBotName           = "agent-machine-bot[bot]"
	AppBotEmail          = "agent-machine-bot[bot]@users.noreply.github.com"
)

type AppIdentity struct {
	Slug   string
	Source string
}

func NewAppIdentity(slug, source string) (AppIdentity, bool) {
	trimmed := strings.TrimSpace(slug)
	if trimmed == "" {
		return AppIdentity{Source: strings.TrimSpace(source)}, false
	}
	return AppIdentity{Slug: trimmed, Source: strings.TrimSpace(source)}, true
}

func AppIdentityFromEnvironment() (AppIdentity, bool) {
	return NewAppIdentity(os.Getenv("GITHUB_APP_SLUG"), "GITHUB_APP_SLUG")
}

func (i AppIdentity) AppPRAuthorLogin() string {
	if strings.TrimSpace(i.Slug) == "" {
		return ""
	}
	return "app/" + strings.TrimSpace(i.Slug)
}

func (i AppIdentity) AppRESTPRAuthorLogin() string {
	if strings.TrimSpace(i.Slug) == "" {
		return ""
	}
	return strings.TrimSpace(i.Slug) + "[bot]"
}

func (i AppIdentity) BotName() string {
	return i.AppRESTPRAuthorLogin()
}

func (i AppIdentity) BotEmail() string {
	name := i.BotName()
	if name == "" {
		return ""
	}
	return name + "@users.noreply.github.com"
}

func (i AppIdentity) ExpectedPRAuthorLogins() []string {
	if strings.TrimSpace(i.Slug) == "" {
		return nil
	}
	return []string{i.AppPRAuthorLogin(), i.AppRESTPRAuthorLogin()}
}

func (i AppIdentity) ExpectedPRAuthorSource() string {
	logins := i.ExpectedPRAuthorLogins()
	source := strings.TrimSpace(i.Source)
	if source == "" {
		source = "GitHub App slug"
	}
	if len(logins) == 0 {
		return "no expected GitHub App author could be derived from " + source
	}
	return fmt.Sprintf("%s or %s (from %s)", logins[0], logins[1], source)
}

func IsExpectedAppPRAuthor(login string) bool {
	switch strings.TrimSpace(login) {
	case "":
		return false
	case AppPRAuthorLogin, AppRESTPRAuthorLogin:
		return true
	default:
		return false
	}
}

func ExpectedAppPRAuthorLogins() string {
	return AppPRAuthorLogin + " or " + AppRESTPRAuthorLogin
}

func IsExpectedAppCommitAuthor(author PRCommitAuthor) bool {
	return DefaultAppIdentity.IsExpectedCommitAuthor(author)
}

func (i AppIdentity) IsExpectedCommitAuthor(author PRCommitAuthor) bool {
	return strings.TrimSpace(author.Name) == i.BotName() && strings.TrimSpace(author.Email) == i.BotEmail()
}

func CommitAuthorInvariantBlockReason(identity AppIdentity, pr PullRequestSummary) string {
	for _, commit := range pr.Commits {
		if strings.TrimSpace(identity.Slug) == "" {
			return identity.ExpectedPRAuthorSource()
		}
		if identity.IsExpectedCommitAuthor(commit.Author) {
			continue
		}
		name := EmptyAsUnknown(strings.TrimSpace(commit.Author.Name))
		email := EmptyAsUnknown(strings.TrimSpace(commit.Author.Email))
		oid := strings.TrimSpace(commit.OID)
		if oid != "" {
			return fmt.Sprintf("commit author for %s is %s <%s>; expected %s <%s>", oid, name, email, identity.BotName(), identity.BotEmail())
		}
		return fmt.Sprintf("commit author is %s <%s>; expected %s <%s>", name, email, identity.BotName(), identity.BotEmail())
	}
	return ""
}

func ConfigureAppCommitIdentity(identity AppIdentity, workspace string, timeout time.Duration) error {
	if strings.TrimSpace(identity.Slug) == "" {
		return errors.New(identity.ExpectedPRAuthorSource())
	}
	return sh.RunWithTimeout(
		"git config user.name "+sh.Quote(identity.BotName())+" && git config user.email "+sh.Quote(identity.BotEmail()),
		workspace,
		timeout,
	)
}

func MintInstallationToken(appID, installationID, privateKeyPath string) (string, error) {
	jwt, err := AppJWT(appID, privateKeyPath)
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

func AppJWT(appID, privateKeyPath string) (string, error) {
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
