package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"github.com/tidwall/gjson"
)

func lifecycle(t *testing.T, yaml string) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"config_yaml": []byte(yaml)})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func normalize(t *testing.T, to, body string) string {
	t.Helper()
	req, _ := json.Marshal(pluginapi.RequestTransformRequest{FromFormat: "openai", ToFormat: to, Model: "gpt-5.5", Body: []byte(body)})
	raw, err := normalizeRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	var env pluginabi.Envelope
	if err := json.Unmarshal(raw, &env); err != nil || !env.OK {
		t.Fatalf("bad envelope %s (%v)", raw, err)
	}
	var resp pluginapi.PayloadResponse
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		t.Fatal(err)
	}
	return string(resp.Body)
}

func TestNormalizeEndToEnd(t *testing.T) {
	t.Cleanup(func() { current.Store(nil) })
	body := `{"instructions":"x","input":[]}`

	if got := normalize(t, "codex", body); got != "" {
		t.Fatalf("unconfigured plugin must not rewrite, got %s", got)
	}
	if err := configure(lifecycle(t, "timezone: Asia/Tokyo\n")); err != nil {
		t.Fatal(err)
	}
	if got := normalize(t, "codex", body); !strings.Contains(gjson.Get(got, "instructions").String(), "Asia/Tokyo") {
		t.Fatalf("codex request not rewritten: %s", got)
	}
	if got := normalize(t, "CODEX", body); got == "" {
		t.Fatal("ToFormat match must be case-insensitive")
	}
	if got := normalize(t, "claude", body); got != "" {
		t.Fatalf("non-codex request rewritten: %s", got)
	}
	if got := normalize(t, "codex", "not json"); got != "" {
		t.Fatalf("invalid body must pass through, got %s", got)
	}
}

func TestConfigureValidation(t *testing.T) {
	t.Cleanup(func() { current.Store(nil) })
	if err := configure(lifecycle(t, "timezone: Europe/Paris")); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"timezone: Mars/Base", "timezone: Local", "timezone: \"a b\"", "timezone: [x]"} {
		if err := configure(lifecycle(t, bad)); err == nil {
			t.Fatalf("%q should be rejected", bad)
		}
		if z := current.Load(); z == nil || z.name != "Europe/Paris" {
			t.Fatalf("rejected config must keep the previous zone, got %+v", z)
		}
	}
	if err := configure(lifecycle(t, "timezone: \"\"")); err != nil || current.Load() != nil {
		t.Fatalf("empty timezone must disable rewriting (err=%v)", err)
	}
	if err := configure(nil); err != nil || current.Load() != nil {
		t.Fatalf("missing config must disable rewriting (err=%v)", err)
	}
}

func TestRegisterDeclaresNormalizerAndTimezoneField(t *testing.T) {
	t.Cleanup(func() { current.Store(nil) })
	raw, err := handleMethod(pluginabi.MethodPluginRegister, lifecycle(t, "timezone: UTC"))
	if err != nil {
		t.Fatal(err)
	}
	res := gjson.GetBytes(raw, "result")
	if !gjson.GetBytes(raw, "ok").Bool() || !res.Get("capabilities.request_normalizer").Bool() {
		t.Fatalf("unexpected registration: %s", raw)
	}
	for _, f := range []string{"Name", "Version", "Author", "GitHubRepository"} {
		if res.Get("metadata."+f).String() == "" {
			t.Fatalf("host rejects empty metadata.%s: %s", f, raw)
		}
	}
	field := res.Get("metadata.ConfigFields.0")
	if field.Get("Name").String() != "timezone" || field.Get("Type").String() != "enum" || !field.Get("EnumValues").IsArray() {
		t.Fatalf("unexpected config field: %s", field.Raw)
	}
	for _, z := range curatedZones {
		if _, err := time.LoadLocation(z); err != nil {
			t.Fatalf("curated zone %q is not loadable: %v", z, err)
		}
	}
}

func TestUnknownMethodAndLifecycleNoops(t *testing.T) {
	raw, err := handleMethod("nope", nil)
	if err != nil || gjson.GetBytes(raw, "error.code").String() != "unknown_method" {
		t.Fatalf("unknown method: %s %v", raw, err)
	}
	for _, m := range []string{pluginabi.MethodPluginQuiesce, pluginabi.MethodPluginShutdown} {
		if raw, err := handleMethod(m, nil); err != nil || !gjson.GetBytes(raw, "ok").Bool() {
			t.Fatalf("%s: %s %v", m, raw, err)
		}
	}
}
