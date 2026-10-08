package omcmd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/opensvc/om3/v3/core/client"
	clientai "github.com/opensvc/om3/v3/core/client/ai"
	"github.com/opensvc/om3/v3/core/naming"
	"github.com/opensvc/om3/v3/daemon/api"
)

const tokenValidityMargin = time.Minute

type authTokenClient interface {
	PostAuthTokenWithResponse(context.Context, *api.PostAuthTokenParams, ...api.RequestEditorFn) (*api.PostAuthTokenResponse, error)
	GetClusterStatusWithResponse(context.Context, *api.GetClusterStatusParams, ...api.RequestEditorFn) (*api.GetClusterStatusResponse, error)
}

type authTokenClientFactory func() (authTokenClient, error)

// issueAICredential asks the same daemon for an access token and its cluster
// ID. The agent forwards both; the cluster ID selects where the token is
// verified, so it must come from the issuing daemon, not the local node.
func issueAICredential(ctx context.Context, requestTimeout time.Duration, factory authTokenClientFactory) (clientai.Credential, error) {
	if factory == nil {
		factory = func() (authTokenClient, error) {
			return client.New()
		}
	}
	tokenClient, err := factory()
	if err != nil {
		return clientai.Credential{}, fmt.Errorf("create local daemon client: %w", err)
	}
	token, err := issueAIAccessToken(ctx, requestTimeout, tokenClient)
	if err != nil {
		return clientai.Credential{}, err
	}
	clusterID, err := fetchAIClusterID(ctx, tokenClient)
	if err != nil {
		return clientai.Credential{}, err
	}
	return clientai.Credential{Token: token, ClusterID: clusterID}, nil
}

func issueAIAccessToken(ctx context.Context, requestTimeout time.Duration, tokenClient authTokenClient) (string, error) {
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

// fetchAIClusterID reads cluster.config.id from the cluster status, selecting
// only the cluster object to keep the response small.
func fetchAIClusterID(ctx context.Context, tokenClient authTokenClient) (string, error) {
	selector := naming.Cluster.String()
	response, err := tokenClient.GetClusterStatusWithResponse(ctx, &api.GetClusterStatusParams{Selector: &selector})
	if err != nil {
		return "", fmt.Errorf("get AI cluster ID: %w", err)
	}
	if response == nil {
		return "", fmt.Errorf("get AI cluster ID: daemon returned an empty response")
	}
	if response.StatusCode() != http.StatusOK {
		return "", fmt.Errorf("get AI cluster ID: daemon returned HTTP %d", response.StatusCode())
	}
	var status struct {
		Cluster struct {
			Config struct {
				ID string `json:"id"`
			} `json:"config"`
		} `json:"cluster"`
	}
	if err := json.Unmarshal(response.Body, &status); err != nil {
		return "", fmt.Errorf("get AI cluster ID: decode cluster status: %w", err)
	}
	if status.Cluster.Config.ID == "" {
		return "", fmt.Errorf("get AI cluster ID: daemon returned an empty cluster ID")
	}
	return status.Cluster.Config.ID, nil
}
