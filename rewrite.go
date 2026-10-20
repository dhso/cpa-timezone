package main

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// marker makes injection idempotent: a request that already carries it is left alone.
const marker = "[CODEX_TIMEZONE_V1]"

// hasMarker reports whether a string the model reads as instructions already carries the marker.
// Assistant output and tool results are deliberately not looked at.
func hasMarker(root gjson.Result) bool {
	var found bool
	visit := func(v gjson.Result) {
		found = found || (v.Type == gjson.String && strings.Contains(v.String(), marker))
	}
	var walk func(v gjson.Result)
	walk = func(v gjson.Result) {
		if v.IsArray() {
			for _, p := range v.Array() {
				if t := p.Get("type").String(); t == "text" || t == "input_text" {
					visit(p.Get("text"))
				}
			}
			return
		}
		visit(v)
	}
	for _, k := range []string{"instructions", "system"} {
		walk(root.Get(k))
	}
	for _, k := range []string{"input", "messages"} {
		v := root.Get(k)
		if !v.IsArray() {
			visit(v)
			continue
		}
		for _, m := range v.Array() {
			switch m.Get("role").String() {
			case "system", "developer", "user":
				walk(m.Get("content"))
			}
		}
	}
	return found
}

// rewrite returns the body with the target date, zone and search location applied. changed is false when the
// body is already correct or cannot be handled; callers then keep the original request (fail-open).
func rewrite(body []byte, c tzContext) (out []byte, changed bool) {
	root := gjson.ParseBytes(body)
	if !root.IsObject() {
		return body, false
	}
	out, found, err := alignTimeContexts(body, root, c)
	if err != nil {
		return body, false
	}
	if !found && !hasMarker(root) {
		if injected, ok := inject(out, gjson.ParseBytes(out), c); ok {
			out = injected
		}
	}
	if out, err = alignSearchLocation(out, gjson.ParseBytes(out), c); err != nil {
		return body, false
	}
	return out, !bytes.Equal(out, body)
}

func contextText(c tzContext) string {
	return marker + "\n" +
		"Current date: " + c.date + "\n" +
		"Current timezone: " + c.name + " (" + c.abbr + ", UTC" + c.offset + ").\n" +
		"Treat this as the user's local date and timezone unless they name another timezone."
}

// inject adds the context text when the request has no recognised time context, following the request shape.
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
