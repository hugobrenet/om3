package omcmd

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/opensvc/om3/v3/core/client"
	"github.com/opensvc/om3/v3/daemon/api"
)

const tokenValidityMargin = time.Minute

type authTokenClient interface {
	PostAuthTokenWithResponse(context.Context, *api.PostAuthTokenParams, ...api.RequestEditorFn) (*api.PostAuthTokenResponse, error)
}

type authTokenClientFactory func() (authTokenClient, error)

func issueAIAccessToken(ctx context.Context, requestTimeout time.Duration, factory authTokenClientFactory) (string, error) {
	if factory == nil {
		factory = func() (authTokenClient, error) {
			return client.New()
		}
	}
	tokenClient, err := factory()
	if err != nil {
		return "", fmt.Errorf("create local daemon client: %w", err)
	}
	tokenDuration := (requestTimeout + tokenValidityMargin).String()
	tokenResponse, err := tokenClient.PostAuthTokenWithResponse(ctx, &api.PostAuthTokenParams{AccessDuration: &tokenDuration})
	if err != nil {
		return "", fmt.Errorf("create AI access token: %w", err)
	}
	if tokenResponse == nil {
		return "", fmt.Errorf("create AI access token: daemon returned an empty response")
	}
	if tokenResponse.StatusCode() != http.StatusOK || tokenResponse.JSON200 == nil {
		return "", fmt.Errorf("create AI access token: daemon returned HTTP %d", tokenResponse.StatusCode())
	}
	token := tokenResponse.JSON200.AccessToken
	if token == "" {
		return "", fmt.Errorf("create AI access token: daemon returned an empty token")
	}
	return token, nil
}
