package main

import (
	"encoding/xml"
	"io"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// timeContexts binds each role to the one block it may carry, and to the content_item_kinds label Codex gives that block.
var timeContexts = map[string]struct{ tag, kind string }{
	"user":      {"environment_context", "environments.environment_context"},
	"developer": {"codex_apps_client_time_context", "additional_content.codex_apps_client_time_context"},
}

// child is a direct child element of a time context block, as a byte range of the block text.
type child struct {
	name       string
	start, end int
}

// blockChildren returns the direct children of text, which must be exactly one well-formed <tag>…</tag> block.
// Surrounding text, a second root, bad XML, another tag name or another tag case all fail, so quoted examples stay untouched.
func blockChildren(text, tag string) ([]child, bool) {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "<"+tag+">") || !strings.HasSuffix(trimmed, "</"+tag+">") {
		return nil, false
	}
	var (
		dec    = xml.NewDecoder(strings.NewReader(text))
		kids   []child
		depth  int
		start  int
		rooted bool
	)
	for {
		before := int(dec.InputOffset())
		tok, err := dec.Token()
		if err == io.EOF {
			return kids, rooted
		}
		if err != nil {
			return nil, false
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if depth == 0 && (rooted || t.Name.Local != tag) {
				return nil, false
			}
			rooted = true
			if depth == 1 {
				start = before
			}
			depth++
		case xml.EndElement:
			depth--
			if depth == 1 {
				kids = append(kids, child{t.Name.Local, start, int(dec.InputOffset())})
			}
		}
	}
}

// alignBlock replaces the direct <timezone> and <current_date> children of a time context block with c's values.
// ok is false when text is not such a block or has neither child; nothing is appended to a block that lacks them.
func alignBlock(text, tag string, c tzContext) (out string, ok bool) {
	kids, valid := blockChildren(text, tag)
	if !valid {
		return "", false
	}
	out = text
	for i := len(kids) - 1; i >= 0; i-- { // back to front keeps the earlier byte ranges valid
		var value string
		switch kids[i].name {
		case "timezone":
			value = c.name
		case "current_date":
			value = c.date
		default:
			continue
		}
		ok = true
		out = out[:kids[i].start] + "<" + kids[i].name + ">" + value + "</" + kids[i].name + ">" + out[kids[i].end:]
	}
	return out, ok
}

// alignTimeContexts rewrites the time context blocks in the input items. found reports whether any block was
// recognised, including one that was already correct. A content_item_kinds list, when present, is authoritative.
func alignTimeContexts(body []byte, root gjson.Result, c tzContext) (out []byte, found bool, err error) {
	out = body
	input := root.Get("input")
	if !input.IsArray() {
		return out, false, nil
	}
	for i, item := range input.Array() {
		tc, ok := timeContexts[item.Get("role").String()]
		content := item.Get("content")
		if !ok || !content.IsArray() {
			continue
		}
		kinds := item.Get("internal_chat_message_metadata_passthrough.content_item_kinds")
		for j, part := range content.Array() {
			if kinds.IsArray() && kinds.Get(strconv.Itoa(j)).String() != tc.kind {
				continue
			}
			if part.Get("type").String() != "input_text" || part.Get("text").Type != gjson.String {
				continue
			}
			text := part.Get("text").String()
			aligned, ok := alignBlock(text, tc.tag, c)
			if !ok {
				continue
			}
			found = true
			if aligned == text {
				continue
			}
			path := "input." + strconv.Itoa(i) + ".content." + strconv.Itoa(j) + ".text"
			if out, err = sjson.SetBytes(out, path, aligned); err != nil {
				return body, false, err
			}
		}
	}
	return out, found, nil
}

// alignSearchLocation points web_search tools at the configured location; other tools are untouched.
func alignSearchLocation(body []byte, root gjson.Result, c tzContext) (out []byte, err error) {
	out = body
	tools := root.Get("tools")
	if c.userLocation == "" || !tools.IsArray() {
		return out, nil
	}
	for i, tool := range tools.Array() {
		if t := tool.Get("type").String(); t != "web_search" && !strings.HasPrefix(t, "web_search_") {
			continue
		}
		if tool.Get("user_location").Raw == c.userLocation {
			continue
		}
		if out, err = sjson.SetRawBytes(out, "tools."+strconv.Itoa(i)+".user_location", []byte(c.userLocation)); err != nil {
			return body, err
		}
	}
	return out, nil
}
