package object

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/opensvc/om3/v3/util/asset"
	"github.com/opensvc/om3/v3/util/san"
)

func TestAssetDataForCollectorV2Properties(t *testing.T) {
	data := asset.Data{
		Properties: asset.Properties{
			ClusterID: asset.Property{Value: "cluster-id"},
			Nodename:  asset.Property{Value: "node1"},
			Serial:    asset.Property{Value: nil},
			Version:   asset.Property{Value: "v3-test"},
		},
	}

	_, vars, vals := assetDataForCollectorV2(data, "node1")

	require.Len(t, vals, len(vars))
	require.True(t, sort.StringsAreSorted(vars))
	require.Equal(t, "cluster-id", collectorV2PropertyValue(t, vars, vals, "cluster_id"))
	require.Equal(t, "node1", collectorV2PropertyValue(t, vars, vals, "nodename"))
	require.Equal(t, "", collectorV2PropertyValue(t, vars, vals, "serial"))
	require.Equal(t, "v3-test", collectorV2PropertyValue(t, vars, vals, "version"))

	// The conversion must not mutate the source asset data.
	require.Nil(t, data.Properties.Serial.Value)
}

func TestAssetDataForCollectorV2GenericTables(t *testing.T) {
	data := asset.Data{
		Hardware: []asset.Device{{Path: "/dev/test", Type: "disk"}},
		HBA:      []san.Initiator{{Name: "hba1", Type: san.FC}},
		Targets: san.Paths{{
			Initiator: san.Initiator{Name: "hba1", Type: san.FC},
			Target:    san.Target{Name: "target1", Type: san.FC},
		}},
		LAN: map[string][]asset.LAN{
			"02:00:00:00:00:02": {{
				Intf:           "eth1",
				Type:           "ipv4",
				Address:        "192.0.2.2",
				Mask:           "24",
				FlagDeprecated: true,
			}},
			"02:00:00:00:00:01": {{
				Intf:    "eth0",
				Type:    "ipv4",
				Address: "192.0.2.1",
				Mask:    "24",
			}},
		},
		UIDS: []asset.User{{Name: "alice", ID: 1000}},
		GIDS: []asset.Group{{Name: "users", ID: 1000}},
	}

	gen, _, _ := assetDataForCollectorV2(data, "node1")

	require.Equal(t, data.Hardware, gen["hardware"])
	require.Equal(t, []any{
		[]string{"nodename", "hba_id", "hba_type"},
		[][]any{{"node1", "hba1", san.FC}},
	}, gen["hba"])
	require.Equal(t, []any{
		[]string{"hba_id", "tgt_id"},
		[][]any{{"hba1", "target1"}},
	}, gen["targets"])
	require.Equal(t, []any{
		[]string{"mac", "intf", "type", "addr", "mask", "flag_deprecated"},
		[][]any{
			{"02:00:00:00:00:01", "eth0", "ipv4", "192.0.2.1", "24", false},
			{"02:00:00:00:00:02", "eth1", "ipv4", "192.0.2.2", "24", true},
		},
	}, gen["lan"])
	require.Equal(t, []any{
		[]string{"user_name", "user_id"},
		[][]any{{"alice", 1000}},
	}, gen["uids"])
	require.Equal(t, []any{
		[]string{"group_name", "group_id"},
		[][]any{{"users", 1000}},
	}, gen["gids"])

	// Sorting the LAN output must not reorder or rewrite the source map.
	require.Equal(t, "eth1", data.LAN["02:00:00:00:00:02"][0].Intf)
}

func collectorV2PropertyValue(t *testing.T, vars []string, vals []any, name string) any {
	t.Helper()
	for i, variable := range vars {
		if variable == name {
			return vals[i]
		}
	}
	t.Fatalf("property %s not found", name)
	return nil
}
