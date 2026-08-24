package object

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/opensvc/om3/v3/core/collector"
	"github.com/opensvc/om3/v3/core/oc3path"
	"github.com/opensvc/om3/v3/core/rawconfig"
	"github.com/opensvc/om3/v3/daemon/daemonenv"
	"github.com/opensvc/om3/v3/util/asset"
	"github.com/opensvc/om3/v3/util/hostname"
	"github.com/opensvc/om3/v3/util/key"
	"github.com/opensvc/om3/v3/util/san"
	"github.com/opensvc/om3/v3/util/version"
)

const collectorV2DateTimeLayout = "2006-01-02 15:04:05"

// collectorV2AssetPropertyNames is the asset property contract implemented by
// the v2 agent for Collector v2. Keep this list explicit: newer om3-only asset
// properties must not leak into the legacy Collector nodes table.
var collectorV2AssetPropertyNames = []string{
	"asset_env",
	"bios_version",
	"cluster_id",
	"connect_to",
	"cpu_cores",
	"cpu_dies",
	"cpu_freq",
	"cpu_model",
	"cpu_threads",
	"enclosure",
	"fqdn",
	"last_boot",
	"listener_port",
	"loc_addr",
	"loc_building",
	"loc_city",
	"loc_country",
	"loc_floor",
	"loc_rack",
	"loc_room",
	"loc_zip",
	"manufacturer",
	"mem_banks",
	"mem_bytes",
	"mem_slots",
	"model",
	"node_env",
	"nodename",
	"os_arch",
	"os_kernel",
	"os_name",
	"os_release",
	"os_vendor",
	"sec_zone",
	"serial",
	"sp_version",
	"team_integ",
	"team_support",
	"tz",
	"version",
}

type (
	// prober is responsible for a bunch of asset properties, and is
	// able to report them one by one.
	// Most prober use a command output, syscall, file content cache
	// where the properties can be found (ex: dmidecode)
	prober interface {
		Get(string) (interface{}, error)
	}
)

func (t Node) nodeSystemCacheFile() string {
	return filepath.Join(rawconfig.NodeVarDir(), "system.json")
}

// assetValueFromDefinedConfig return asset.property from config keyword eval
// when the keyword is present in current config or return asset.property from
// default value
func (t Node) assetValueFromDefinedConfig(kw string, title string, defaultValue interface{}) (data asset.Property) {
	data.Title = title
	k := key.Parse(kw)
	if t.MergedConfig().HasKey(k) {
		data.Source = asset.SrcConfig
		s, err := t.MergedConfig().Eval(k)
		if err != nil {
			data.Error = fmt.Sprint(err)
		} else {
			data.Value = s
		}
		return
	}
	data.Source = asset.SrcDefault
	data.Value = defaultValue
	return
}

// assetValueFromConfigEval return asset.property from config keyword eval
func (t Node) assetValueFromConfigEval(kw string, title string) (data asset.Property) {
	data.Title = title
	data.Source = asset.SrcConfig
	k := key.Parse(kw)
	s, err := t.MergedConfig().Eval(k)
	if err != nil {
		data.Error = fmt.Sprint(err)
	} else {
		data.Value = s
	}
	return
}

// assetValueFromDefinedConfigOrProbe return asset.property with the following evaluation order:
//
//	1- config keyword eval if the keyword is present in current config, or present
//	2- probe.Get
//	3- defaultValue
func (t Node) assetValueFromDefinedConfigOrProbe(kw string, title string, probe prober, defaultValue interface{}) (data asset.Property) {
	data.Title = title
	k := key.Parse(kw)
	if t.MergedConfig().HasKey(k) {
		data.Source = asset.SrcConfig
		s, err := t.MergedConfig().Eval(k)
		if err != nil {
			data.Error = fmt.Sprint(err)
		} else {
			data.Value = s
		}
		return
	}
	if probe != nil {
		s, err := probe.Get(k.Option)
		if err == nil {
			data.Source = asset.SrcProbe
			data.Value = s
			return
		}
		if !errors.Is(err, asset.ErrIgnore) {
			data.Error = fmt.Sprint(err)
		}
	}
	data.Source = asset.SrcDefault
	data.Value = defaultValue
	return
}

func (t Node) assetAgentVersion() (data asset.Property) {
	data.Title = "agent version"
	data.Source = asset.SrcProbe
	data.Value = version.Version()
	return
}

func (t Node) assetNodename() (data asset.Property) {
	data.Title = "nodename"
	data.Source = asset.SrcProbe
	data.Value = hostname.Hostname()
	return
}

func (t Node) assetValueClusterID() (data asset.Property) {
	k := key.T{Section: "cluster", Option: "id"}
	data.Title = "cluster id"
	data.Source = asset.SrcProbe
	data.Value, _ = t.MergedConfig().Eval(k)
	return
}

// PushAsset assembles the asset inventory data.
// Each entry value comes from:
// * overrides (in config)
// * probes
// * default (code)
func (t Node) PushAsset() (asset.Data, error) {
	data, err := t.getAsset()
	if err != nil {
		return data, err
	}
	if err := t.dumpSystem(data); err != nil {
		return data, err
	}
	if err := t.pushAsset(data); err != nil {
		return data, err
	}
	return data, nil
}

func (t Node) PushAssetDryRun() (asset.Data, error) {
	data, err := t.getAsset()
	if err != nil {
		return data, err
	}
	if err := t.dumpSystem(data); err != nil {
		return data, err
	}
	return data, nil
}

func (t Node) dumpSystem(data asset.Data) error {
	filename := t.nodeSystemCacheFile()
	tryOpen := func() (*os.File, error) {
		return os.OpenFile(filename, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0660)
	}
	file, err := tryOpen()
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(filename), 0700); err != nil {
			return err
		}
		file, err = tryOpen()
	}
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	return json.NewEncoder(file).Encode(data)
}

func (t Node) LoadSystem() (asset.Data, error) {
	var data asset.Data
	file, err := os.Open(t.nodeSystemCacheFile())
	if err != nil {
		return data, err
	}
	defer func() { _ = file.Close() }()
	err = json.NewDecoder(file).Decode(&data)
	return data, err
}

func (t Node) getAsset() (asset.Data, error) {
	data := asset.NewData()

	// from core
	data.Properties.ClusterID = t.assetValueClusterID()
	data.Properties.Nodename = t.assetNodename()
	data.Properties.Version = t.assetAgentVersion()

	// from probe
	probe := asset.New()
	data.Properties.FQDN = t.assetValueFromDefinedConfigOrProbe("node.fqdn", "fqdn", probe, nil)
	data.Properties.OSName = t.assetValueFromDefinedConfigOrProbe("node.os_name", "os name", probe, nil)
	data.Properties.OSVendor = t.assetValueFromDefinedConfigOrProbe("node.os_vendor", "os vendor", probe, nil)
	data.Properties.OSRelease = t.assetValueFromDefinedConfigOrProbe("node.os_release", "os release", probe, nil)
	data.Properties.OSKernel = t.assetValueFromDefinedConfigOrProbe("node.os_kernel", "os kernel", probe, nil)
	data.Properties.OSArch = t.assetValueFromDefinedConfigOrProbe("node.os_arch", "os arch", probe, nil)
	data.Properties.MemBytes = t.assetValueFromDefinedConfigOrProbe("node.mem_bytes", "mem bytes", probe, nil)
	data.Properties.MemSlots = t.assetValueFromDefinedConfigOrProbe("node.mem_slots", "mem slots", probe, nil)
	data.Properties.MemBanks = t.assetValueFromDefinedConfigOrProbe("node.mem_banks", "mem banks", probe, nil)
	data.Properties.CPUFreq = t.assetValueFromDefinedConfigOrProbe("node.cpu_freq", "cpu freq", probe, nil)
	data.Properties.CPUThreads = t.assetValueFromDefinedConfigOrProbe("node.cpu_threads", "cpu threads", probe, nil)
	data.Properties.CPUCores = t.assetValueFromDefinedConfigOrProbe("node.cpu_cores", "cpu cores", probe, nil)
	data.Properties.CPUDies = t.assetValueFromDefinedConfigOrProbe("node.cpu_dies", "cpu dies", probe, nil)
	data.Properties.CPUModel = t.assetValueFromDefinedConfigOrProbe("node.cpu_model", "cpu model", probe, nil)
	data.Properties.BIOSVersion = t.assetValueFromDefinedConfigOrProbe("node.bios_version", "bios version", probe, nil)
	data.Properties.Serial = t.assetValueFromDefinedConfigOrProbe("node.serial", "serial", probe, nil)
	data.Properties.SPVersion = t.assetValueFromDefinedConfigOrProbe("node.sp_version", "sp version", probe, nil)
	data.Properties.Enclosure = t.assetValueFromDefinedConfigOrProbe("node.enclosure", "enclosure", probe, nil)
	data.Properties.TZ = t.assetValueFromDefinedConfigOrProbe("node.tz", "timezone", probe, nil)
	data.Properties.Manufacturer = t.assetValueFromDefinedConfigOrProbe("node.manufacturer", "manufacturer", probe, nil)
	data.Properties.Model = t.assetValueFromDefinedConfigOrProbe("node.model", "model", probe, nil)
	data.Properties.ConnectTo = t.assetValueFromDefinedConfigOrProbe("node.connect_to", "connect to", probe, "")
	data.Properties.LastBoot = t.assetValueFromDefinedConfigOrProbe("node.last_boot", "last boot", probe, nil)
	data.Properties.BootID = t.assetValueFromDefinedConfigOrProbe("node.boot_id", "boot id", probe, nil)
	data.UIDS, _ = asset.Users()
	data.GIDS, _ = asset.Groups()
	data.Hardware, _ = asset.Hardware()
	data.LAN, _ = asset.GetLANS()
	data.HBA, _ = san.GetInitiators()
	data.Targets, _ = san.GetPaths()

	// from config eval only
	data.Properties.NodeEnv = t.assetValueFromConfigEval("node.env", "environment")

	// from existing config key
	data.Properties.SecZone = t.assetValueFromDefinedConfig("node.sec_zone", "security zone", nil)
	data.Properties.AssetEnv = t.assetValueFromDefinedConfig("node.asset_env", "asset environment", nil)
	data.Properties.ListenerPort = t.assetValueFromDefinedConfig("listener.port", "listener port", nil)
	data.Properties.LocCountry = t.assetValueFromDefinedConfig("node.loc_country", "loc, country", nil)
	data.Properties.LocCity = t.assetValueFromDefinedConfig("node.loc_city", "loc, city", nil)
	data.Properties.LocBuilding = t.assetValueFromDefinedConfig("node.loc_building", "loc, building", nil)
	data.Properties.LocRoom = t.assetValueFromDefinedConfig("node.loc_room", "loc, room", nil)
	data.Properties.LocRack = t.assetValueFromDefinedConfig("node.loc_rack", "loc, rack", nil)
	data.Properties.LocAddr = t.assetValueFromDefinedConfig("node.loc_addr", "loc, address", nil)
	data.Properties.LocFloor = t.assetValueFromDefinedConfig("node.loc_floor", "loc, floor", nil)
	data.Properties.LocZIP = t.assetValueFromDefinedConfig("node.loc_zip", "loc, zip", nil)
	data.Properties.TeamInteg = t.assetValueFromDefinedConfig("node.team_integ", "team, integration", nil)
	data.Properties.TeamSupport = t.assetValueFromDefinedConfig("node.team_support", "team, support", nil)

	return data, nil
}

// pushAsset selects the Collector protocol from the node configuration.
// Explicit Collector v3 feeder settings take precedence over the legacy
// Collector v2 node.dbopensvc setting.
func (t Node) pushAsset(data asset.Data) error {
	if t.CollectorRawConfig().FeederUrl() != "" {
		return t.pushAssetV3(data)
	}
	if t.MergedConfig().GetString(key.Parse("node.dbopensvc")) != "" {
		return t.pushAssetV2(data)
	}

	// Preserve the existing ErrConfig error returned by CollectorFeeder when
	// neither a Collector v3 nor a Collector v2 endpoint is configured.
	return t.pushAssetV3(data)
}

// callCollectorV2 executes a Collector v2 JSON-RPC call and checks both the
// transport error and the error possibly embedded in the JSON-RPC response.
func callCollectorV2(client *collector.Client, method string, params ...interface{}) error {
	response, err := client.Call(method, params...)
	if err != nil {
		return err
	}
	if response == nil {
		return fmt.Errorf("collector rpc %s: empty response", method)
	}
	if response.Error != nil {
		return fmt.Errorf("collector rpc %s: %s: %v", method, response.Error.Message, response.Error.Data)
	}
	return nil
}

// assetDataForCollectorV2 converts the current asset.Data representation to
// the vars/vals and generic table representation expected by Collector v2.
func assetDataForCollectorV2(data asset.Data, nodename string) (map[string]any, []string, []any, error) {
	gen := make(map[string]any)
	gen["hardware"] = data.Hardware

	hbaVars := []string{"nodename", "hba_id", "hba_type"}
	hbaVals := make([][]any, 0, len(data.HBA))
	for _, e := range data.HBA {
		hbaVals = append(hbaVals, []any{nodename, e.Name, e.Type})
	}
	gen["hba"] = []any{hbaVars, hbaVals}

	targetVars := []string{"hba_id", "tgt_id"}
	targetVals := make([][]any, 0, len(data.Targets))
	for _, e := range data.Targets {
		targetVals = append(targetVals, []any{e.Initiator.Name, e.Target.Name})
	}
	gen["targets"] = []any{targetVars, targetVals}

	lanVars := []string{"mac", "intf", "type", "addr", "mask", "flag_deprecated"}
	lanVals := make([][]any, 0)
	macs := make([]string, 0, len(data.LAN))
	for mac := range data.LAN {
		macs = append(macs, mac)
	}
	sort.Strings(macs)
	for _, mac := range macs {
		for _, e := range data.LAN[mac] {
			lanVals = append(lanVals, []any{mac, e.Intf, e.Type, e.Address, e.Mask, e.FlagDeprecated})
		}
	}
	gen["lan"] = []any{lanVars, lanVals}

	uidVars := []string{"user_name", "user_id"}
	uidVals := make([][]any, 0, len(data.UIDS))
	for _, e := range data.UIDS {
		uidVals = append(uidVals, []any{e.Name, e.ID})
	}
	gen["uids"] = []any{uidVars, uidVals}

	gidVars := []string{"group_name", "group_id"}
	gidVals := make([][]any, 0, len(data.GIDS))
	for _, e := range data.GIDS {
		gidVals = append(gidVals, []any{e.Name, e.ID})
	}
	gen["gids"] = []any{gidVars, gidVals}

	properties := make(map[string]asset.Property)
	for _, property := range data.Values() {
		properties[property.Name] = property
	}
	vars := make([]string, 0, len(collectorV2AssetPropertyNames))
	vals := make([]any, 0, len(collectorV2AssetPropertyNames))
	for _, name := range collectorV2AssetPropertyNames {
		property, ok := properties[name]
		if !ok {
			continue
		}
		value, err := assetPropertyValueForCollectorV2(property)
		if err != nil {
			return nil, nil, nil, err
		}
		vars = append(vars, name)
		vals = append(vals, value)
	}

	return gen, vars, vals, nil
}

func assetPropertyValueForCollectorV2(property asset.Property) (any, error) {
	if property.Name == "listener_port" && property.Value == nil {
		return fmt.Sprint(daemonenv.HTTPPort), nil
	}
	if property.Value == nil {
		return "", nil
	}
	if property.Name == "listener_port" && property.Value == "" {
		return fmt.Sprint(daemonenv.HTTPPort), nil
	}
	if property.Name != "last_boot" {
		return property.Value, nil
	}

	s, ok := property.Value.(string)
	if !ok {
		return nil, fmt.Errorf("convert asset property last_boot for Collector v2: expected string, got %T", property.Value)
	}
	if s == "" {
		return "", nil
	}
	if _, err := time.ParseInLocation(collectorV2DateTimeLayout, s, time.Local); err == nil {
		return s, nil
	}
	tm, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return nil, fmt.Errorf("convert asset property last_boot for Collector v2: %w", err)
	}
	return tm.Format(collectorV2DateTimeLayout), nil
}

// pushAssetV2 sends the node inventory using the Collector v2 JSON-RPC API.
func (t Node) pushAssetV2(data asset.Data) error {
	endpoint := t.MergedConfig().GetString(key.Parse("node.dbopensvc"))
	secret := t.Config().GetString(key.Parse("node.uuid"))
	if endpoint == "" {
		return collector.ErrConfig
	}
	if secret == "" {
		return collector.ErrUnregistered
	}

	cfg := collector.Config{
		FeederUrl: endpoint,
		Password:  secret,
		Insecure:  t.MergedConfig().GetBool(key.Parse("node.dbinsecure")),
	}
	client, err := cfg.NewFeedClient()
	if err != nil {
		return err
	}

	gen, vars, vals, err := assetDataForCollectorV2(data, hostname.Hostname())
	if err != nil {
		return err
	}
	if len(gen) > 0 {
		if err := callCollectorV2(client, "insert_generic", gen); err != nil {
			return err
		}
	}
	return callCollectorV2(client, "update_asset", vars, vals)
}

// pushAssetV3 sends the node inventory using the Collector v3 feeder API.
func (t Node) pushAssetV3(data asset.Data) error {
	var (
		req  *http.Request
		resp *http.Response

		ioReader io.Reader

		method = http.MethodPost
		path   = oc3path.FeedNodeSystem
	)
	oc3, err := t.CollectorFeeder()
	if err != nil {
		return err
	}

	hba := func() []any {
		l := make([]any, len(data.HBA))
		for i, e := range data.HBA {
			l[i] = map[string]any{
				"hba_id":   e.Name,
				"hba_type": e.Type,
			}
		}
		return l
	}
	targets := func() []any {
		l := make([]any, len(data.Targets))
		for i, e := range data.Targets {
			l[i] = map[string]any{
				"hba_id": e.Initiator.Name,
				"tgt_id": e.Target.Name,
			}
		}
		return l
	}

	gen := make(map[string]any)

	gen["properties"] = data.Properties
	gen["hardware"] = data.Hardware
	gen["lan"] = data.LAN
	gen["uids"] = data.UIDS
	gen["gids"] = data.GIDS

	// Transformations
	gen["hba"] = hba()
	gen["targets"] = targets()

	if b, err := json.MarshalIndent(gen, "  ", "  "); err != nil {
		return fmt.Errorf("encode request body: %w", err)
	} else {
		ioReader = bytes.NewBuffer(b)
	}

	ctx, cancel := context.WithTimeout(context.Background(), defaultPostCollectorTimeout)
	defer cancel()

	req, err = oc3.NewRequestWithContext(ctx, method, path, ioReader)
	if err != nil {
		return fmt.Errorf("create collector request %s %s: %w", method, path, err)
	}

	resp, err = oc3.Do(req)
	if err != nil {
		return fmt.Errorf("collector %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("unexpected collector response status code for %s %s: wanted %d got %d",
			method, path, http.StatusAccepted, resp.StatusCode)
	}

	return nil
}
