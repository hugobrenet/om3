package omcmd

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/opensvc/om3/v3/daemon/api"
)

type fakeAuthTokenClient struct {
	token         string
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

func TestIssueAIAccessToken(t *testing.T) {
	client := &fakeAuthTokenClient{token: "access-token"}
	token, err := issueAIAccessToken(t.Context(), 2*time.Second, func() (authTokenClient, error) {
		return client, nil
	})
	if err != nil {
		t.Fatalf("issue access token: %v", err)
	}
	if token != "access-token" {
		t.Fatalf("token = %q", token)
	}
	if want := (2*time.Second + tokenValidityMargin).String(); client.duration != want {
		t.Fatalf("token duration = %q, want %q", client.duration, want)
	}
}

func TestIssueAIAccessTokenReturnsStableFailures(t *testing.T) {
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
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := issueAIAccessToken(t.Context(), time.Second, test.factory)
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
