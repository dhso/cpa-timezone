package main

import (
	"strings"
	"testing"
	"time"

	"github.com/tidwall/gjson"
)

var tokyo = tzContext{name: "Asia/Tokyo", date: "2026-10-21", abbr: "JST", offset: "+09:00"}

func mustRewrite(t *testing.T, body string) string {
	t.Helper()
	out, changed := rewrite([]byte(body), tokyo)
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

func TestEnvBlockMissingAndSelfClosingFields(t *testing.T) {
	out := mustRewrite(t, `{"input":"<environment_context><cwd>/w</cwd><timezone/></environment_context>"}`)
	text := gjson.Get(out, "input").String()
	if !strings.Contains(text, "<timezone>Asia/Tokyo</timezone>") || !strings.Contains(text, "<current_date>2026-10-21</current_date>") || strings.Contains(text, "<timezone/>") {
		t.Fatalf("unexpected block: %q", text)
	}
}

func TestEnvBlockCaseMultipleAndEscapedBrackets(t *testing.T) {
	// < is how some serializers escape "<"; gjson decodes it before matching.
	body := `{"instructions":"<ENVIRONMENT_CONTEXT><TimeZone>UTC</TimeZone></ENVIRONMENT_CONTEXT>","system":"<environment_context><timezone>UTC</timezone></environment_context>"}`
	out := mustRewrite(t, body)
	for _, p := range []string{"instructions", "system"} {
		if got := gjson.Get(out, p).String(); !strings.Contains(got, "<timezone>Asia/Tokyo</timezone>") && !strings.Contains(got, ">Asia/Tokyo</") {
			t.Fatalf("%s not rewritten: %q", p, got)
		}
	}
	two := mustRewrite(t, `{"input":"<environment_context></environment_context> and <environment_context></environment_context>"}`)
	if n := strings.Count(gjson.Get(two, "input").String(), "Asia/Tokyo"); n != 2 {
		t.Fatalf("want both blocks rewritten, got %d: %s", n, two)
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
