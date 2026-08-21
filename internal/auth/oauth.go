// SPDX-License-Identifier: LGPL-3.0-only

package auth

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Wei-Shaw/cc-gpt-adapter/internal/upstream/codex"
)

const (
	ClientID           = "app_EMoamEEZ73f0CkXaXp7hrann"
	AuthorizeURL       = "https://auth.openai.com/oauth/authorize"
	TokenURL           = "https://auth.openai.com/oauth/token"
	DefaultRedirectURI = "http://localhost:1455/auth/callback"
	DefaultScopes      = "openid profile email offline_access"
	RefreshScopes      = "openid profile email"

	maxTokenResponseBytes = 1 << 20
)

// OAuthConfig makes the provider endpoints injectable without changing the
// production Codex CLI defaults.
type OAuthConfig struct {
	ClientID      string
	AuthorizeURL  string
	TokenURL      string
	RedirectURI   string
	Scopes        string
	RefreshScopes string
}

func DefaultOAuthConfig() OAuthConfig {
	return OAuthConfig{
		ClientID: ClientID, AuthorizeURL: AuthorizeURL, TokenURL: TokenURL,
		RedirectURI: DefaultRedirectURI, Scopes: DefaultScopes, RefreshScopes: RefreshScopes,
	}
}

type OAuthClient struct {
	config     OAuthConfig
	httpClient *http.Client
}

type AuthorizationFlow struct {
	State            string
	CodeVerifier     string
	CodeChallenge    string
	AuthorizationURL string
}

type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token,omitempty"`
	IDToken      string `json:"id_token,omitempty"`
	TokenType    string `json:"token_type,omitempty"`
	ExpiresIn    int64  `json:"expires_in"`
	Scope        string `json:"scope,omitempty"`
}

func NewOAuthClient(config OAuthConfig, httpClient *http.Client) (*OAuthClient, error) {
	defaults := DefaultOAuthConfig()
	if strings.TrimSpace(config.ClientID) == "" {
		config.ClientID = defaults.ClientID
	}
	if strings.TrimSpace(config.AuthorizeURL) == "" {
		config.AuthorizeURL = defaults.AuthorizeURL
	}
	if strings.TrimSpace(config.TokenURL) == "" {
		config.TokenURL = defaults.TokenURL
	}
	if strings.TrimSpace(config.RedirectURI) == "" {
		config.RedirectURI = defaults.RedirectURI
	}
	if strings.TrimSpace(config.Scopes) == "" {
		config.Scopes = defaults.Scopes
	}
	if strings.TrimSpace(config.RefreshScopes) == "" {
		config.RefreshScopes = defaults.RefreshScopes
	}
	for name, rawURL := range map[string]string{
		"authorize endpoint": config.AuthorizeURL,
		"token endpoint":     config.TokenURL,
		"redirect URI":       config.RedirectURI,
	} {
		parsed, err := url.Parse(rawURL)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" ||
			(parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil {
			return nil, fmt.Errorf("create OAuth client: %s must be an absolute HTTP(S) URL", name)
		}
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 2 * time.Minute}
	}
	privateClient := &http.Client{
		Transport: httpClient.Transport,
		Jar:       httpClient.Jar,
		Timeout:   httpClient.Timeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return &OAuthClient{config: config, httpClient: privateClient}, nil
}

func (client *OAuthClient) Config() OAuthConfig {
	if client == nil {
		return OAuthConfig{}
	}
	return client.config
}

func (client *OAuthClient) BeginAuthorization() (AuthorizationFlow, error) {
	if client == nil {
		return AuthorizationFlow{}, errors.New("begin OAuth authorization: nil client")
	}
	state, err := randomHex(32)
	if err != nil {
		return AuthorizationFlow{}, fmt.Errorf("generate OAuth state: %w", err)
	}
	verifier, err := randomHex(64)
	if err != nil {
		return AuthorizationFlow{}, fmt.Errorf("generate PKCE verifier: %w", err)
	}
	challengeDigest := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(challengeDigest[:])

	parameters := url.Values{}
	parameters.Set("response_type", "code")
	parameters.Set("client_id", client.config.ClientID)
	parameters.Set("redirect_uri", client.config.RedirectURI)
	parameters.Set("scope", client.config.Scopes)
	parameters.Set("state", state)
	parameters.Set("code_challenge", challenge)
	parameters.Set("code_challenge_method", "S256")
	parameters.Set("id_token_add_organizations", "true")
	parameters.Set("codex_cli_simplified_flow", "true")

	authorizationEndpoint, err := url.Parse(client.config.AuthorizeURL)
	if err != nil {
		return AuthorizationFlow{}, fmt.Errorf("build OAuth authorization URL: %w", err)
	}
	query := authorizationEndpoint.Query()
	for key, values := range parameters {
		query[key] = values
	}
	authorizationEndpoint.RawQuery = query.Encode()

	return AuthorizationFlow{
		State: state, CodeVerifier: verifier, CodeChallenge: challenge,
		AuthorizationURL: authorizationEndpoint.String(),
	}, nil
}

func (client *OAuthClient) ExchangeCode(ctx context.Context, code, verifier string) (TokenResponse, error) {
	if client == nil {
		return TokenResponse{}, errors.New("exchange OAuth code: nil client")
	}
	if strings.TrimSpace(code) == "" || strings.TrimSpace(verifier) == "" {
		return TokenResponse{}, errors.New("exchange OAuth code: code and verifier are required")
	}
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("client_id", client.config.ClientID)
	form.Set("code", code)
	form.Set("redirect_uri", client.config.RedirectURI)
	form.Set("code_verifier", verifier)
	return client.postToken(ctx, form, true)
}

func (client *OAuthClient) Refresh(ctx context.Context, refreshToken string) (TokenResponse, error) {
	if client == nil {
		return TokenResponse{}, errors.New("refresh OAuth token: nil client")
	}
	if strings.TrimSpace(refreshToken) == "" {
		return TokenResponse{}, errors.New("refresh OAuth token: refresh token is required")
	}
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("client_id", client.config.ClientID)
	form.Set("refresh_token", refreshToken)
	form.Set("scope", client.config.RefreshScopes)
	return client.postToken(ctx, form, false)
}

func (client *OAuthClient) postToken(ctx context.Context, form url.Values, requireRefresh bool) (TokenResponse, error) {
	if client == nil {
		return TokenResponse{}, errors.New("request OAuth token: nil client")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	request, err := http.NewRequestWithContext(
		ctx, http.MethodPost, client.config.TokenURL, strings.NewReader(form.Encode()),
	)
	if err != nil {
		return TokenResponse{}, fmt.Errorf("create OAuth token request: %w", err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", codex.UserAgent)
	request.Header.Set("originator", codex.Originator)

	response, err := client.httpClient.Do(request)
	if err != nil {
		return TokenResponse{}, fmt.Errorf("request OAuth token: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxTokenResponseBytes+1))
	if err != nil {
		return TokenResponse{}, fmt.Errorf("read OAuth token response: %w", err)
	}
	if len(body) > maxTokenResponseBytes {
		return TokenResponse{}, errors.New("read OAuth token response: body exceeds 1 MiB")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return TokenResponse{}, tokenEndpointError(response.StatusCode, body)
	}
	var token TokenResponse
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(&token); err != nil {
		return TokenResponse{}, fmt.Errorf("decode OAuth token response: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return TokenResponse{}, errors.New("decode OAuth token response: multiple JSON values")
	}
	if strings.TrimSpace(token.AccessToken) == "" || token.ExpiresIn <= 0 {
		return TokenResponse{}, errors.New("decode OAuth token response: missing access_token or positive expires_in")
	}
	if requireRefresh && strings.TrimSpace(token.RefreshToken) == "" {
		return TokenResponse{}, errors.New("decode OAuth token response: missing refresh_token")
	}
	return token, nil
}

func tokenEndpointError(status int, body []byte) error {
	var payload struct {
		Code string `json:"error"`
	}
	_ = json.Unmarshal(body, &payload)
	code := strings.TrimSpace(payload.Code)
	for _, character := range code {
		if !((character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '_' || character == '-') {
			code = ""
			break
		}
	}
	if code == "" {
		return fmt.Errorf("OAuth token endpoint returned HTTP %d", status)
	}
	return fmt.Errorf("OAuth token endpoint returned HTTP %d (%s)", status, code)
}

func randomHex(byteCount int) (string, error) {
	data := make([]byte, byteCount)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return hex.EncodeToString(data), nil
}

func newLocalSecret() (string, error) {
	data := make([]byte, 32)
	if _, err := rand.Read(data); err != nil {
		return "", fmt.Errorf("generate local secret: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

// AccountIDFromIDToken decodes only the JWT payload. The result is provider
// metadata, never proof of identity and never used for local authorization.
func AccountIDFromIDToken(idToken string) (string, error) {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return "", errors.New("decode ID token: expected three JWT parts")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", fmt.Errorf("decode ID token payload: %w", err)
	}
	var claims struct {
		OpenAIAuth struct {
			ChatGPTAccountID string `json:"chatgpt_account_id"`
		} `json:"https://api.openai.com/auth"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", fmt.Errorf("decode ID token claims: %w", err)
	}
	accountID := strings.TrimSpace(claims.OpenAIAuth.ChatGPTAccountID)
	if accountID == "" {
		return "", errors.New("decode ID token claims: missing chatgpt_account_id")
	}
	return accountID, nil
}
