package main

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// marker makes injection idempotent: a request that already carries it is left alone.
const marker = "[CODEX_TIMEZONE_V1]"

var envBlockRe = regexp.MustCompile(`(?is)(<environment_context\b[^>]*>)(.*?)(</environment_context\s*>)`)

// envField is one child element of <environment_context> that the plugin owns.
type envField struct {
	tag   string
	value func(tzContext) string
	elem  *regexp.Regexp // <tag>…</tag>
	empty *regexp.Regexp // <tag/>
}

func newEnvField(tag string, value func(tzContext) string) envField {
	return envField{
		tag:   tag,
		value: value,
		elem:  regexp.MustCompile(`(?is)(<` + tag + `\s*>)(.*?)(</` + tag + `\s*>)`),
		empty: regexp.MustCompile(`(?i)<` + tag + `\s*/>`),
	}
}

var envFields = []envField{
	newEnvField("timezone", func(c tzContext) string { return c.name }),
	newEnvField("current_date", func(c tzContext) string { return c.date }),
}

// set makes the block content carry exactly value for this field: it replaces every
// <tag>…</tag>, else expands <tag/>, else appends a new element.
func (f envField) set(content, value string) (string, bool) {
	changed := false
	switch {
	case f.elem.MatchString(content):
		content = f.elem.ReplaceAllStringFunc(content, func(m string) string {
			sub := f.elem.FindStringSubmatch(m)
			if sub[2] == value {
				return m
			}
			changed = true
			return sub[1] + value + sub[3]
		})
	case f.empty.MatchString(content):
		content = f.empty.ReplaceAllStringFunc(content, func(string) string { return "<" + f.tag + ">" + value + "</" + f.tag + ">" })
		changed = true
	default:
		if content != "" && !strings.HasSuffix(content, "\n") {
			content += "\n"
		}
		content += "<" + f.tag + ">" + value + "</" + f.tag + ">\n"
		changed = true
	}
	return content, changed
}

// rewriteEnv fixes every <environment_context> block in text. found reports whether any block exists.
func rewriteEnv(text string, c tzContext) (out string, found, changed bool) {
	out = envBlockRe.ReplaceAllStringFunc(text, func(m string) string {
		found = true
		sub := envBlockRe.FindStringSubmatch(m)
		content := sub[2]
		for _, f := range envFields {
			var ch bool
			content, ch = f.set(content, f.value(c))
			changed = changed || ch
		}
		return sub[1] + content + sub[3]
	})
	return out, found, changed
}

type candidate struct{ path, text string }

// candidates lists the strings that may hold an environment block: system-ish prompts and
// system/developer/user messages. Assistant output and tool results are deliberately skipped.
func candidates(root gjson.Result) []candidate {
	var out []candidate
	for _, k := range []string{"instructions", "system"} {
		out = appendContent(out, k, root.Get(k))
	}
	for _, k := range []string{"input", "messages"} {
		v := root.Get(k)
		switch {
		case v.Type == gjson.String:
			out = append(out, candidate{k, v.String()})
		case v.IsArray():
			for i, m := range v.Array() {
				switch m.Get("role").String() {
				case "system", "developer", "user":
					out = appendContent(out, k+"."+strconv.Itoa(i)+".content", m.Get("content"))
				}
			}
		}
	}
	return out
}

func appendContent(out []candidate, path string, v gjson.Result) []candidate {
	if v.Type == gjson.String {
		return append(out, candidate{path, v.String()})
	}
	if v.IsArray() {
		for j, p := range v.Array() {
			if t := p.Get("type").String(); (t == "text" || t == "input_text") && p.Get("text").Type == gjson.String {
				out = append(out, candidate{path + "." + strconv.Itoa(j) + ".text", p.Get("text").String()})
			}
		}
	}
	return out
}

// rewrite returns the body with the target date and zone applied. changed is false when the body is
// already correct or cannot be handled; callers then keep the original request (fail-open).
func rewrite(body []byte, c tzContext) (out []byte, changed bool) {
	root := gjson.ParseBytes(body)
	if !root.IsObject() {
		return body, false
	}
	out = body
	envFound, hasMarker := false, false
	for _, cand := range candidates(root) {
		hasMarker = hasMarker || strings.Contains(cand.text, marker)
		text, found, ch := rewriteEnv(cand.text, c)
		envFound = envFound || found
		if !ch {
			continue
		}
		next, err := sjson.SetBytes(out, cand.path, text)
		if err != nil {
			return body, false
		}
		out, changed = next, true
	}
	if envFound {
		return out, changed
	}
	if hasMarker {
		return body, false
	}
	return inject(body, root, c)
}

func contextText(c tzContext) string {
	return marker + "\n" +
		"Current date: " + c.date + "\n" +
		"Current timezone: " + c.name + " (" + c.abbr + ", UTC" + c.offset + ").\n" +
		"Treat this as the user's local date and timezone unless they name another timezone."
}

// inject adds the context text when the request has no environment block, following the request shape.
func inject(body []byte, root gjson.Result, c tzContext) ([]byte, bool) {
	text := contextText(c)
	ins := root.Get("instructions")
	var (
		out []byte
		err error
	)
	switch {
	case ins.Type == gjson.String:
		if s := ins.String(); strings.TrimSpace(s) != "" {
			text = s + "\n\n" + text
		}
		out, err = sjson.SetBytes(body, "instructions", text)
	case ins.Exists() && ins.Type == gjson.Null:
		out, err = sjson.SetBytes(body, "instructions", text)
	case ins.Exists():
		return body, false // unknown shape: leave it, a second instruction source could conflict
	case root.Get("input").IsArray():
		out, err = sjson.SetBytes(body, "instructions", text)
	case root.Get("messages").IsArray():
		msg, _ := json.Marshal(map[string]string{"role": "developer", "content": text})
		raw := "[" + string(msg)
		if rest := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(root.Get("messages").Raw, "["), "]")); rest != "" {
			raw += "," + rest
		}
		out, err = sjson.SetRawBytes(body, "messages", []byte(raw+"]"))
	default:
		return body, false
	}
	if err != nil {
		return body, false
	}
	return out, true
}
