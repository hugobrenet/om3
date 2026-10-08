package omcmd

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/opensvc/om3/v3/daemon/api"
)

const testAIClusterID = "00000000-0000-4000-8000-000000000001"

type fakeAuthTokenClient struct {
	token         string
	clusterID     string
	clusterStatus int
	clusterErr    error
	selector      string
	status        int
	duration      string
	err           error
	emptyResponse bool
	omitPayload   bool
}

func (c *fakeAuthTokenClient) PostAuthTokenWithResponse(_ context.Context, params *api.PostAuthTokenParams, _ ...api.RequestEditorFn) (*api.PostAuthTokenResponse, error) {
	if c.err != nil {
		return nil, c.err
	}
	if params.AccessDuration != nil {
		c.duration = *params.AccessDuration
	}
	if c.emptyResponse {
		return nil, nil
	}
	status := c.status
	if status == 0 {
		status = http.StatusOK
	}
	response := &api.PostAuthTokenResponse{
		HTTPResponse: &http.Response{StatusCode: status},
	}
	if status == http.StatusOK && !c.omitPayload {
		response.JSON200 = &api.AuthToken{AccessToken: c.token}
	}
	return response, nil
}

func (c *fakeAuthTokenClient) GetClusterStatusWithResponse(_ context.Context, params *api.GetClusterStatusParams, _ ...api.RequestEditorFn) (*api.GetClusterStatusResponse, error) {
	if c.clusterErr != nil {
		return nil, c.clusterErr
	}
	if params.Selector != nil {
		c.selector = *params.Selector
	}
	status := c.clusterStatus
	if status == 0 {
		status = http.StatusOK
	}
	clusterID := c.clusterID
	if clusterID == "" {
		clusterID = testAIClusterID
	}
	if clusterID == "-" {
		clusterID = ""
	}
	body := fmt.Sprintf(`{"cluster":{"config":{"id":%q,"name":"cluster-a"},"node":{}}}`, clusterID)
	return &api.GetClusterStatusResponse{Body: []byte(body), HTTPResponse: &http.Response{StatusCode: status}}, nil
}

func TestIssueAICredential(t *testing.T) {
	client := &fakeAuthTokenClient{token: "access-token"}
	cred, err := issueAICredential(t.Context(), 2*time.Second, func() (authTokenClient, error) {
		return client, nil
	})
	if err != nil {
		t.Fatalf("issue credential: %v", err)
	}
	if cred.Token != "access-token" || cred.ClusterID != testAIClusterID {
		t.Fatalf("credential = %+v", cred)
	}
	if client.selector != "cluster" {
		t.Fatalf("cluster status selector = %q", client.selector)
	}
	if want := (2*time.Second + tokenValidityMargin).String(); client.duration != want {
		t.Fatalf("token duration = %q, want %q", client.duration, want)
	}
}

func TestIssueAICredentialReturnsStableFailures(t *testing.T) {
	factoryError := errors.New("factory failed")
	requestError := errors.New("request failed")
	tests := []struct {
		name       string
		factory    authTokenClientFactory
		wantTarget error
		wantText   string
	}{
		{
			name: "client factory",
			factory: func() (authTokenClient, error) {
				return nil, factoryError
			},
			wantTarget: factoryError,
			wantText:   "create local daemon client",
		},
		{
			name: "token request",
			factory: func() (authTokenClient, error) {
				return &fakeAuthTokenClient{err: requestError}, nil
			},
			wantTarget: requestError,
			wantText:   "create AI access token",
		},
		{
			name: "empty response",
			factory: func() (authTokenClient, error) {
				return &fakeAuthTokenClient{emptyResponse: true}, nil
			},
			wantText: "daemon returned an empty response",
		},
		{
			name: "HTTP status",
			factory: func() (authTokenClient, error) {
				return &fakeAuthTokenClient{status: http.StatusForbidden}, nil
			},
			wantText: "daemon returned HTTP 403",
		},
		{
			name: "missing payload",
			factory: func() (authTokenClient, error) {
				return &fakeAuthTokenClient{omitPayload: true}, nil
			},
			wantText: "daemon returned HTTP 200",
		},
		{
			name: "empty token",
			factory: func() (authTokenClient, error) {
				return &fakeAuthTokenClient{}, nil
			},
			wantText: "daemon returned an empty token",
		},
		{
			name: "cluster status request",
			factory: func() (authTokenClient, error) {
				return &fakeAuthTokenClient{token: "token", clusterErr: requestError}, nil
			},
			wantTarget: requestError,
			wantText:   "get AI cluster ID",
		},
		{
			name: "cluster status HTTP status",
			factory: func() (authTokenClient, error) {
				return &fakeAuthTokenClient{token: "token", clusterStatus: http.StatusForbidden}, nil
			},
			wantText: "get AI cluster ID: daemon returned HTTP 403",
		},
		{
			name: "empty cluster ID",
			factory: func() (authTokenClient, error) {
				return &fakeAuthTokenClient{token: "token", clusterID: "-"}, nil
			},
			wantText: "daemon returned an empty cluster ID",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := issueAICredential(t.Context(), time.Second, test.factory)
			if err == nil {
				t.Fatal("token issuance succeeded")
			}
			if test.wantTarget != nil && !errors.Is(err, test.wantTarget) {
				t.Fatalf("error = %v, want wrapping %v", err, test.wantTarget)
			}
			if !strings.Contains(err.Error(), test.wantText) {
				t.Fatalf("error = %v, want containing %q", err, test.wantText)
			}
		})
	}
}
