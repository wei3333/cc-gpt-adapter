// SPDX-License-Identifier: LGPL-3.0-only

package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

const RefreshBeforeExpiry = 3 * time.Minute

type Access struct {
	AccessToken      string
	ChatGPTAccountID string
}

type TokenManager struct {
	store *Store
	oauth *OAuthClient

	refreshMu sync.Mutex
	now       func() time.Time
}

func NewTokenManager(store *Store, oauth *OAuthClient) (*TokenManager, error) {
	if store == nil || oauth == nil {
		return nil, errors.New("create token manager: store and OAuth client are required")
	}
	return &TokenManager{store: store, oauth: oauth, now: time.Now}, nil
}

func (manager *TokenManager) GetValidToken(ctx context.Context) (Access, error) {
	if manager == nil {
		return Access{}, errors.New("get valid token: nil manager")
	}
	credentials, err := manager.store.Load()
	if err != nil {
		return Access{}, err
	}
	if credentials.ExpiresAt.After(manager.now().Add(RefreshBeforeExpiry)) {
		return accessFromCredentials(credentials), nil
	}
	return manager.refresh(ctx, "")
}

// ForceRefresh refreshes after an upstream 401. If another goroutine already
// replaced rejectedAccessToken, the new token is returned without a second
// refresh request.
func (manager *TokenManager) ForceRefresh(ctx context.Context, rejectedAccessToken string) (Access, error) {
	if manager == nil {
		return Access{}, errors.New("force token refresh: nil manager")
	}
	if strings.TrimSpace(rejectedAccessToken) == "" {
		return Access{}, errors.New("force token refresh: rejected access token is required")
	}
	return manager.refresh(ctx, rejectedAccessToken)
}

func (manager *TokenManager) refresh(ctx context.Context, rejectedAccessToken string) (Access, error) {
	manager.refreshMu.Lock()
	defer manager.refreshMu.Unlock()

	credentials, err := manager.store.Load()
	if err != nil {
		return Access{}, err
	}
	if rejectedAccessToken != "" && credentials.AccessToken != rejectedAccessToken {
		return accessFromCredentials(credentials), nil
	}
	if rejectedAccessToken == "" && credentials.ExpiresAt.After(manager.now().Add(RefreshBeforeExpiry)) {
		return accessFromCredentials(credentials), nil
	}
	if strings.TrimSpace(credentials.RefreshToken) == "" {
		return Access{}, errors.New("refresh OAuth token: stored refresh token is missing")
	}

	response, err := manager.oauth.Refresh(ctx, credentials.RefreshToken)
	if err != nil {
		return Access{}, err
	}
	credentials.AccessToken = response.AccessToken
	credentials.ExpiresAt = manager.now().Add(time.Duration(response.ExpiresIn) * time.Second)
	if rotated := strings.TrimSpace(response.RefreshToken); rotated != "" {
		credentials.RefreshToken = rotated
	}
	if strings.TrimSpace(response.IDToken) != "" {
		if accountID, decodeErr := AccountIDFromIDToken(response.IDToken); decodeErr == nil {
			credentials.ChatGPTAccountID = accountID
		}
	}
	if err := manager.store.Save(credentials); err != nil {
		return Access{}, fmt.Errorf("persist refreshed OAuth token: %w", err)
	}
	return accessFromCredentials(credentials), nil
}

func accessFromCredentials(credentials Credentials) Access {
	return Access{
		AccessToken: credentials.AccessToken, ChatGPTAccountID: credentials.ChatGPTAccountID,
	}
}
