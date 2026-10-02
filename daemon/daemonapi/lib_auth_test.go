package daemonapi

import (
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/opensvc/om3/v3/core/cluster"
	"github.com/opensvc/om3/v3/daemon/daemonauth"
	"github.com/opensvc/om3/v3/daemon/rbac"
)

type tokenClaimsRecorder struct {
	claims   map[string]any
	duration time.Duration
}

func (r *tokenClaimsRecorder) CreateToken(duration time.Duration, claims map[string]any) (string, time.Time, error) {
	r.duration = duration
	r.claims = claims
	return "test-token", time.Unix(1800000000, 0), nil
}

func TestCreateTokenIncludesLocalClusterID(t *testing.T) {
	withClusterConfig(t)
	for _, clusterID := range []string{"00000000-0000-4000-8000-000000000001", "another-cluster-id"} {
		t.Run(clusterID, func(t *testing.T) {
			config := cluster.ConfigData.Get()
			config.ID = clusterID
			cluster.ConfigData.Set(config)
			for _, tokenUse := range []string{daemonauth.TkUseAccess, daemonauth.TkUseRefresh, daemonauth.TkUseProxy} {
				t.Run(tokenUse, func(t *testing.T) {
					creator := &tokenClaimsRecorder{}
					a := &DaemonAPI{localhost: "node1", JWTcreator: creator}
					claims := map[string]any{"cluster_id": "not-the-local-cluster", "grant": []string{"guest:system"}}
					token, expiresAt, err := a.createToken("alice", tokenUse, time.Minute, claims)
					require.NoError(t, err)
					assert.Equal(t, "test-token", token)
					assert.Equal(t, time.Unix(1800000000, 0), expiresAt)
					assert.Equal(t, time.Minute, creator.duration)
					assert.Equal(t, map[string]any{
						"cluster_id": clusterID,
						"sub":        "alice",
						"iss":        "node1",
						"token_use":  tokenUse,
						"grant":      []string{"guest:system"},
					}, creator.claims)
					assert.Equal(t, "not-the-local-cluster", claims["cluster_id"], "additional claims must not be mutated")
				})
			}
		})
	}
}

func TestCreateAccessTokenIncludesClusterID(t *testing.T) {
	withClusterConfig(t)
	config := cluster.ConfigData.Get()
	config.ID = "00000000-0000-4000-8000-000000000001"
	cluster.ConfigData.Set(config)
	creator := &tokenClaimsRecorder{}
	a := &DaemonAPI{localhost: "node1", JWTcreator: creator}
	ctx := echo.New().NewContext(nil, nil)
	ctx.Set("user", &daemonauth.Info{Username: "root", Strategy: daemonauth.StrategyUX})
	ctx.Set("strategy", daemonauth.StrategyUX)
	ctx.Set("grants", rbac.NewGrants("root"))
	token, err := a.createAccessToken(ctx, "root", time.Minute, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, "test-token", token.AccessToken)
	assert.Equal(t, config.ID, creator.claims["cluster_id"])
	assert.Equal(t, "node1", creator.claims["iss"])
	assert.Equal(t, "root", creator.claims["sub"])
	assert.Equal(t, daemonauth.TkUseAccess, creator.claims["token_use"])
	assert.Equal(t, []string{"root"}, creator.claims["grant"])
}
