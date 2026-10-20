package main

import (
	"os"
	"testing"

	"github.com/tidwall/gjson"
)

// The store installs by registry id, so a drift from pluginID makes the zip's library name unmatchable.
func TestRegistryMatchesPlugin(t *testing.T) {
	raw, err := os.ReadFile("registry.json")
	if err != nil {
		t.Fatal(err)
	}
	if v := gjson.GetBytes(raw, "schema_version").Int(); v != 1 {
		t.Errorf("schema_version = %d, want 1", v)
	}
	if n := len(gjson.GetBytes(raw, "plugins").Array()); n != 1 {
		t.Fatalf("plugins = %d entries, want 1", n)
	}
	if id := gjson.GetBytes(raw, "plugins.0.id").String(); id != pluginID {
		t.Errorf("id = %q, want %q", id, pluginID)
	}
	for _, f := range []string{"name", "description", "author", "repository"} {
		if gjson.GetBytes(raw, "plugins.0."+f).String() == "" {
			t.Errorf("missing required field %s", f)
		}
	}
}
