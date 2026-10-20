package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/tidwall/gjson"
)

var tokyo = tzContext{name: "Asia/Tokyo", date: "2026-10-21", abbr: "JST", offset: "+09:00"}

func mustRewrite(t *testing.T, body string) string {
	t.Helper()
	return mustRewriteWith(t, body, tokyo)
}

func mustRewriteWith(t *testing.T, body string, c tzContext) string {
	t.Helper()
	out, changed := rewrite([]byte(body), c)
	if !changed {
		t.Fatalf("expected a rewrite, got none for %s", body)
	}
	if !gjson.ValidBytes(out) {
		t.Fatalf("output is not valid JSON: %s", out)
	}
	return string(out)
}

func mustNotChange(t *testing.T, body string) {
	t.Helper()
	if out, changed := rewrite([]byte(body), tokyo); changed || string(out) != body {
		t.Fatalf("expected no change, got %s", out)
	}
}

// item builds an input item with one text part; kinds is the raw content_item_kinds JSON, or empty for no metadata.
func item(role, typ, text, kinds string) string {
	t, _ := json.Marshal(text)
	meta := ""
	if kinds != "" {
		meta = `,"internal_chat_message_metadata_passthrough":{"content_item_kinds":` + kinds + `}`
	}
	return `{"role":"` + role + `","content":[{"type":"` + typ + `","text":` + string(t) + `}]` + meta + `}`
}

func TestInjectInstructionsString(t *testing.T) {
	out := mustRewrite(t, `{"instructions":"be nice","input":[]}`)
	got := gjson.Get(out, "instructions").String()
	if !strings.HasPrefix(got, "be nice\n\n"+marker) || !strings.Contains(got, "Asia/Tokyo (JST, UTC+09:00)") {
		t.Fatalf("unexpected instructions: %q", got)
	}
}

func TestInjectEmptyOrNullOrMissingInstructions(t *testing.T) {
	for _, body := range []string{
		`{"instructions":"","input":[]}`,
		`{"instructions":null,"input":[]}`,
		`{"input":[{"role":"user","content":"hi"}]}`,
	} {
		out := mustRewrite(t, body)
		if got := gjson.Get(out, "instructions").String(); !strings.HasPrefix(got, marker) {
			t.Fatalf("%s -> instructions %q", body, got)
		}
	}
}

func TestInjectLeavesUnknownInstructionsShape(t *testing.T) {
	mustNotChange(t, `{"instructions":["x"],"input":[]}`)
}

func TestInjectMessagesPrependsDeveloperMessage(t *testing.T) {
	out := mustRewrite(t, `{"messages":[{"role":"user","content":"héllo 你好"}]}`)
	msgs := gjson.Get(out, "messages").Array()
	if len(msgs) != 2 || msgs[0].Get("role").String() != "developer" || !strings.Contains(msgs[0].Get("content").String(), marker) {
		t.Fatalf("unexpected messages: %s", out)
	}
	if msgs[1].Get("content").String() != "héllo 你好" {
		t.Fatalf("original message changed: %s", out)
	}
	empty := mustRewrite(t, `{"messages":[]}`)
	if n := len(gjson.Get(empty, "messages").Array()); n != 1 {
		t.Fatalf("empty messages should gain exactly one entry: %s", empty)
	}
}

func TestInjectIsIdempotent(t *testing.T) {
	for _, body := range []string{`{"instructions":"x","input":[]}`, `{"messages":[]}`, `{"input":[]}`} {
		once := mustRewrite(t, body)
		mustNotChange(t, once)
	}
}

func TestNoShapeNoInjection(t *testing.T) {
	mustNotChange(t, `{"model":"gpt-5.5"}`)
	mustNotChange(t, `[1,2]`)
}

const envBody = `{"input":[{"role":"user","content":[{"type":"input_text","text":"<environment_context>\n  <cwd>/w</cwd>\n  <timezone>America/Los_Angeles</timezone>\n  <current_date>2026-10-20</current_date>\n</environment_context>"}]}]}`

func TestEnvBlockReplacedInPlaceWithoutInjection(t *testing.T) {
	out := mustRewrite(t, envBody)
	text := gjson.Get(out, "input.0.content.0.text").String()
	if !strings.Contains(text, "<timezone>Asia/Tokyo</timezone>") || !strings.Contains(text, "<current_date>2026-10-21</current_date>") || !strings.Contains(text, "<cwd>/w</cwd>") {
		t.Fatalf("unexpected block: %q", text)
	}
	if gjson.Get(out, "instructions").Exists() || strings.Contains(out, marker) {
		t.Fatalf("must not inject when a block exists: %s", out)
	}
	mustNotChange(t, out)
}

func TestEnvBlockExpandsSelfClosingAndAppendsNothing(t *testing.T) {
	out := mustRewrite(t, `{"input":[`+item("user", "input_text", "<environment_context><cwd>/w</cwd><timezone/></environment_context>", "")+`]}`)
	text := gjson.Get(out, "input.0.content.0.text").String()
	if !strings.Contains(text, "<timezone>Asia/Tokyo</timezone>") || strings.Contains(text, "<timezone/>") || strings.Contains(text, "current_date") {
		t.Fatalf("unexpected block: %q", text)
	}
}

const desktopBlock = "<codex_apps_client_time_context>\n  <timezone>Asia/Singapore</timezone>\n  <current_date>2026-10-20</current_date>\n  Use this client time context for user-facing dates.\n</codex_apps_client_time_context>"

func TestDesktopTimeContextAlignedWithEnvironmentContext(t *testing.T) {
	env := item("user", "input_text", "<environment_context><timezone>UTC</timezone></environment_context>", "")
	for _, kinds := range []string{"", `["developer.text","additional_content.codex_apps_client_time_context"]`} {
		dev := `{"role":"developer","content":[{"type":"input_text","text":"Keep Asia/Singapore in this instruction"},{"type":"input_text","text":` + jsonString(desktopBlock) + `}]`
		if kinds != "" {
			dev += `,"internal_chat_message_metadata_passthrough":{"content_item_kinds":` + kinds + `}`
		}
		out := mustRewrite(t, `{"input":[`+env+`,`+dev+`}]}`)
		if got := gjson.Get(out, "input.1.content.0.text").String(); got != "Keep Asia/Singapore in this instruction" {
			t.Fatalf("kinds=%q: other text changed: %q", kinds, got)
		}
		block := gjson.Get(out, "input.1.content.1.text").String()
		want := strings.NewReplacer("Asia/Singapore", "Asia/Tokyo", "2026-10-20", "2026-10-21").Replace(desktopBlock)
		if block != want {
			t.Fatalf("kinds=%q: desktop block\n got %q\nwant %q", kinds, block, want)
		}
		if got := gjson.Get(out, "input.0.content.0.text").String(); !strings.Contains(got, "Asia/Tokyo") {
			t.Fatalf("kinds=%q: environment block not aligned: %q", kinds, got)
		}
		if strings.Contains(out, marker) {
			t.Fatalf("must not inject when blocks exist: %s", out)
		}
		mustNotChange(t, out)
	}
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestTimeContextOutsideStrictShapeIsLeftAlone(t *testing.T) {
	env := "<environment_context><timezone>Asia/Singapore</timezone></environment_context>"
	cases := map[string]string{
		"desktop block under user role":     item("user", "input_text", desktopBlock, ""),
		"environment block under developer": item("developer", "input_text", env, ""),
		"assistant output":                  item("assistant", "output_text", env, ""),
		"output_text under user":            item("user", "output_text", env, ""),
		"tool output":                       `{"type":"function_call_output","call_id":"c","output":` + jsonString(env) + `}`,
		"string content":                    `{"role":"user","content":` + jsonString(env) + `}`,
		"explicit plain text":               item("user", "input_text", env, `["user.text"]`),
		"unknown kind":                      item("user", "input_text", env, `["future.context"]`),
		"empty kinds":                       item("user", "input_text", env, `[]`),
		"text before block":                 item("user", "input_text", "Example:\n"+env, ""),
		"text after block":                  item("user", "input_text", env+"\nKeep this example", ""),
		"mismatched close tag":              item("user", "input_text", "<environment_context><timezone>Asia/Singapore</environment_context>", ""),
		"unclosed block":                    item("user", "input_text", "<environment_context><timezone>Asia/Singapore</timezone>", ""),
		"tag case differs":                  item("user", "input_text", "<ENVIRONMENT_CONTEXT><timezone>Asia/Singapore</timezone></ENVIRONMENT_CONTEXT>", ""),
		"nested fields":                     item("user", "input_text", "<environment_context><example><timezone>Asia/Singapore</timezone></example></environment_context>", ""),
		"two blocks in one text":            item("user", "input_text", env+env, ""),
	}
	for name, it := range cases {
		body := `{"input":[` + it + `]}`
		out := mustRewrite(t, body) // nothing recognised, so only the marker text is injected
		if gjson.Get(out, "input").Raw != gjson.Get(body, "input").Raw {
			t.Errorf("%s: input was modified: %s", name, out)
		}
		if !strings.Contains(gjson.Get(out, "instructions").String(), marker) {
			t.Errorf("%s: expected the marker text to be injected: %s", name, out)
		}
	}
}

func TestAssistantAndToolOutputUntouched(t *testing.T) {
	block := `<environment_context><timezone>UTC</timezone></environment_context>`
	body := `{"input":[{"role":"assistant","content":[{"type":"output_text","text":"` + block + `"}]},{"type":"function_call_output","output":"` + block + `"},{"role":"user","content":"hi"}]}`
	out := mustRewrite(t, body) // user message has no block, so the marker text is injected; the blocks above must survive
	if strings.Count(out, "<timezone>UTC</timezone>") != 2 {
		t.Fatalf("assistant or tool output was modified: %s", out)
	}
}

func TestToolsFieldIgnored(t *testing.T) {
	mustNotChange(t, `{"instructions":"x [CODEX_TIMEZONE_V1]","tools":[{"description":"<environment_context><timezone>UTC</timezone></environment_context>"}]}`)
}

func TestWebSearchLocationAligned(t *testing.T) {
	c := tokyo
	c.userLocation = (&zoneState{name: "Asia/Tokyo", country: "JP"}).searchLocation()
	const keep = `{"type":"function","name":"keep","description":"Keep Asia/Shanghai unchanged","user_location":{"country":"US"}}`
	body := `{"instructions":"x ` + marker + `","tools":[{"type":"web_search","user_location":{"type":"approximate","country":"US","city":"Piketon","timezone":"America/New_York"}},{"type":"web_search_preview"},` + keep + `]}`
	out := mustRewriteWith(t, body, c)
	for _, i := range []string{"0", "1"} {
		loc := gjson.Get(out, "tools."+i+".user_location")
		if loc.Get("country").String() != "JP" || loc.Get("city").String() != "Tokyo" || loc.Get("timezone").String() != "Asia/Tokyo" || loc.Get("type").String() != "approximate" || loc.Get("region").Exists() {
			t.Fatalf("tools.%s.user_location: %s", i, loc.Raw)
		}
	}
	if gjson.Get(out, "tools.2").Raw != keep {
		t.Fatalf("non web_search tool was modified: %s", out)
	}
	if again, changed := rewrite([]byte(out), c); changed {
		t.Fatalf("second pass must not change anything: %s", again)
	}
	mustNotChange(t, body) // no location configured: tools stay as they are
}

func TestClockUsesZoneRulesAtGivenInstant(t *testing.T) {
	z := &zoneState{name: "America/Los_Angeles"}
	z.loc, _ = time.LoadLocation(z.name)
	summer := z.at(time.Date(2026, 10, 11, 20, 0, 0, 0, time.UTC))
	winter := z.at(time.Date(2026, 12, 1, 3, 0, 0, 0, time.UTC))
	if summer.abbr != "PDT" || summer.offset != "-07:00" || summer.date != "2026-10-11" {
		t.Fatalf("summer: %+v", summer)
	}
	if winter.abbr != "PST" || winter.offset != "-08:00" || winter.date != "2026-11-30" {
		t.Fatalf("winter (date must roll back across the offset): %+v", winter)
	}
}
