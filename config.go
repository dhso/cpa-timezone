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

// placeRe limits the free-text location fields; they end up in the web_search tool the model sees.
var placeRe = regexp.MustCompile(`^[\p{L}\p{N} .,'()-]{1,80}$`)

// countryRe is an ISO 3166-1 alpha-2 code.
var countryRe = regexp.MustCompile(`^[A-Za-z]{2}$`)

// zoneState is the configured target zone; nil means "not configured", which makes the plugin a no-op.
type zoneState struct {
	name                  string
	loc                   *time.Location
	country, region, city string // optional web_search user_location fields
}

var current atomic.Pointer[zoneState]

// tzContext is the date and zone text written into requests, always computed from the current instant
// so midnight and DST transitions between requests are handled by the tz database.
type tzContext struct {
	name, date, abbr, offset string
	userLocation             string // raw JSON for web_search tools' user_location
}

func (z *zoneState) at(now time.Time) tzContext {
	t := now.In(z.loc)
	abbr, _ := t.Zone()
	return tzContext{name: z.name, date: t.Format("2006-01-02"), abbr: abbr, offset: t.Format("-07:00"), userLocation: z.searchLocation()}
}

// searchLocation builds the user_location object. City falls back to the last zone segment (Asia/Ho_Chi_Minh
// gives "Ho Chi Minh"); country and region are never guessed, so they are only written when configured.
func (z *zoneState) searchLocation() string {
	loc := map[string]string{"type": "approximate", "timezone": z.name}
	city := z.city
	if i := strings.LastIndex(z.name, "/"); city == "" && i >= 0 {
		city = strings.ReplaceAll(z.name[i+1:], "_", " ")
	}
	for k, v := range map[string]string{"country": z.country, "region": z.region, "city": city} {
		if v != "" {
			loc[k] = v
		}
	}
	raw, _ := json.Marshal(loc) // map[string]string cannot fail to marshal
	return string(raw)
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
		Country  string `yaml:"country"`
		Region   string `yaml:"region"`
		City     string `yaml:"city"`
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
	z := &zoneState{name: name, loc: loc, country: strings.ToUpper(strings.TrimSpace(cfg.Country)), region: strings.TrimSpace(cfg.Region), city: strings.TrimSpace(cfg.City)}
	if z.country != "" && !countryRe.MatchString(z.country) {
		return fmt.Errorf("invalid country %q: want an ISO 3166-1 alpha-2 code such as JP", cfg.Country)
	}
	for field, v := range map[string]string{"region": z.region, "city": z.city} {
		if v != "" && !placeRe.MatchString(v) {
			return fmt.Errorf("invalid %s %q: want letters, digits, spaces and . , ' ( ) - only, up to 80 characters", field, v)
		}
	}
	current.Store(z)
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
			}, {
				Name:        "country",
				Type:        pluginapi.ConfigFieldTypeString,
				Description: "Optional ISO 3166-1 alpha-2 country code (for example JP) written into web_search user_location. Empty omits it.",
			}, {
				Name:        "region",
				Type:        pluginapi.ConfigFieldTypeString,
				Description: "Optional region or state written into web_search user_location. Empty omits it.",
			}, {
				Name:        "city",
				Type:        pluginapi.ConfigFieldTypeString,
				Description: "Optional city written into web_search user_location. Empty uses the last segment of the timezone name.",
			}},
		},
		Capabilities: struct {
			RequestNormalizer bool `json:"request_normalizer"`
		}{RequestNormalizer: true},
	}
}
