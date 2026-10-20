package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync/atomic"
	"time"
	_ "time/tzdata" // the host may run without a system zoneinfo (e.g. Windows)

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"gopkg.in/yaml.v3"
)

const pluginID = "codex-timezone"

var zoneNameRe = regexp.MustCompile(`^[A-Za-z0-9_+/-]{1,80}$`)

// version is overridden at release time: -ldflags "-X main.version=<tag without v>".
var version = "0.1.0"

// zoneState is the configured target zone; nil means "not configured", which makes the plugin a no-op.
type zoneState struct {
	name string
	loc  *time.Location
}

var current atomic.Pointer[zoneState]

// tzContext is the date and zone text written into requests, always computed from the current instant
// so midnight and DST transitions between requests are handled by the tz database.
type tzContext struct {
	name, date, abbr, offset string
}

func (z *zoneState) at(now time.Time) tzContext {
	t := now.In(z.loc)
	abbr, _ := t.Zone()
	return tzContext{name: z.name, date: t.Format("2006-01-02"), abbr: abbr, offset: t.Format("-07:00")}
}

// configure parses the lifecycle request shared by plugin.register and plugin.reconfigure.
// A rejected config keeps the in-plugin zone, but the host then unregisters the plugin, so nothing is rewritten until a valid zone is saved.
func configure(raw []byte) error {
	var req struct {
		ConfigYAML []byte `json:"config_yaml"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &req); err != nil {
			return err
		}
	}
	var cfg struct {
		Timezone string `yaml:"timezone"`
	}
	if len(req.ConfigYAML) > 0 {
		if err := yaml.Unmarshal(req.ConfigYAML, &cfg); err != nil {
			return err
		}
	}
	name := strings.TrimSpace(cfg.Timezone)
	if name == "" {
		current.Store(nil)
		return nil
	}
	// The name ends up in prompts, so reject anything outside the IANA charset; "Local" would resolve to the host zone.
	if !zoneNameRe.MatchString(name) || strings.EqualFold(name, "Local") {
		return fmt.Errorf("invalid timezone %q: want an IANA name such as Asia/Shanghai", name)
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return fmt.Errorf("invalid timezone %q: want an IANA name such as Asia/Shanghai", name)
	}
	current.Store(&zoneState{name: name, loc: loc})
	return nil
}

func registration() any {
	return struct {
		SchemaVersion uint32             `json:"schema_version"`
		Metadata      pluginapi.Metadata `json:"metadata"`
		Capabilities  struct {
			RequestNormalizer bool `json:"request_normalizer"`
		} `json:"capabilities"`
	}{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             pluginID,
			Version:          version,
			Author:           "dhso",
			GitHubRepository: "https://github.com/dhso/cpa-timezone",
			ConfigFields: []pluginapi.ConfigField{{
				Name:        "timezone",
				Type:        pluginapi.ConfigFieldTypeEnum,
				EnumValues:  curatedZones,
				Description: "IANA timezone written into Codex requests. Empty disables rewriting. Names outside this list can be set in the YAML directly.",
			}},
		},
		Capabilities: struct {
			RequestNormalizer bool `json:"request_normalizer"`
		}{RequestNormalizer: true},
	}
}
