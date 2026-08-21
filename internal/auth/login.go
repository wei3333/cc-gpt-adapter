// SPDX-License-Identifier: LGPL-3.0-only

package auth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

const DefaultLoginTimeout = 5 * time.Minute

type LoginOptions struct {
	OAuth    *OAuthClient
	Store    *Store
	Listener net.Listener
	OpenURL  func(context.Context, string) error
	Output   io.Writer
	Timeout  time.Duration
	Now      func() time.Time
}

func Login(ctx context.Context, options LoginOptions) (Credentials, error) {
	if options.OAuth == nil || options.Store == nil {
		return Credentials{}, errors.New("login: OAuth client and credential store are required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if options.Timeout <= 0 {
		options.Timeout = DefaultLoginTimeout
	}
	if options.OpenURL == nil {
		options.OpenURL = OpenBrowser
	}
	if options.Output == nil {
		options.Output = io.Discard
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	listener := options.Listener
	if listener == nil {
		var err error
		listener, err = net.Listen("tcp", "localhost:1455")
		if err != nil {
			return Credentials{}, fmt.Errorf("listen for OAuth callback: %w", err)
		}
	}

	flow, err := options.OAuth.BeginAuthorization()
	if err != nil {
		_ = listener.Close()
		return Credentials{}, err
	}
	if err := options.OpenURL(ctx, flow.AuthorizationURL); err != nil {
		_, _ = fmt.Fprintf(options.Output, "Open this URL to continue login:\n%s\n", flow.AuthorizationURL)
	}

	waitContext, cancel := context.WithTimeout(ctx, options.Timeout)
	defer cancel()
	code, err := WaitForCallback(
		waitContext, listener, options.OAuth.Config().RedirectURI, flow.State,
	)
	if err != nil {
		return Credentials{}, err
	}
	token, err := options.OAuth.ExchangeCode(ctx, code, flow.CodeVerifier)
	if err != nil {
		return Credentials{}, err
	}
	accountID, err := AccountIDFromIDToken(token.IDToken)
	if err != nil {
		return Credentials{}, fmt.Errorf("complete login: %w", err)
	}

	localSecret := ""
	existing, loadErr := options.Store.Load()
	switch {
	case loadErr == nil:
		localSecret = existing.LocalSecret
	case errors.Is(loadErr, ErrNotLoggedIn):
		localSecret, err = newLocalSecret()
		if err != nil {
			return Credentials{}, err
		}
	default:
		return Credentials{}, fmt.Errorf("load existing credentials: %w", loadErr)
	}
	credentials := Credentials{
		Version: CredentialsVersion, AccessToken: token.AccessToken,
		RefreshToken: token.RefreshToken, ExpiresAt: options.Now().Add(time.Duration(token.ExpiresIn) * time.Second),
		ChatGPTAccountID: accountID, LocalSecret: localSecret,
	}
	if err := options.Store.Save(credentials); err != nil {
		return Credentials{}, err
	}
	return credentials, nil
}

func OpenBrowser(ctx context.Context, target string) error {
	if strings.TrimSpace(target) == "" {
		return errors.New("open browser: URL is required")
	}
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		command = exec.CommandContext(ctx, "open", target)
	case "windows":
		command = exec.CommandContext(ctx, "rundll32", "url.dll,FileProtocolHandler", target)
	default:
		command = exec.CommandContext(ctx, "xdg-open", target)
	}
	if err := command.Run(); err != nil {
		return fmt.Errorf("open browser: %w", err)
	}
	return nil
}
