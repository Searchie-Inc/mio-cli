package main

import (
	"bytes"
	"encoding/json"
	"io"
	"strconv"
	"strings"
)

// apiErrorEnvelope renders the stderr error envelope for a failure that carries
// the API's own JSON:API error document (MIO-3912). Before this, the envelope
// was rebuilt from CLI-side state — status, detail, meta.exit_code — which
// dropped every other member the API sent: `code` (the stable machine token an
// agent branches on), `title`, `source`, and `meta.request_id` (the one id that
// correlates a failure with the backend's log line; the CLI overwrote it with
// meta.exit_code). --raw did not help, because it only ever applied to stdout.
//
// Both modes carry EVERY member of EVERY API error object, and the CLI's
// meta.exit_code is merged INTO the API's meta (created when the API sent none,
// set when it sent one) instead of replacing it:
//
//   - default: `{"errors":[…]}`, one entry per API error object. errors[0]'s
//     `status` and `detail` keep their pre-MIO-3912 values — the transport
//     status (statusForEnvelope, MIO-2656) and the CLI's own message, which
//     names the command's context and any hint and joins every API error — so
//     nothing an agent reads today moves. `status` is added to any later error
//     object that arrived without one.
//   - raw: the API's document itself — every top-level member, member order,
//     number text and string escapes as they arrived. The CLI's only additions
//     are meta.exit_code on every error object and `status` (the transport
//     status) on an error object that arrived without one. `detail` is the
//     API's, so the CLI's context and hints are not in it.
//
// ok is false when doc cannot be carried faithfully: not a JSON object, no
// non-empty `errors` array, or an error object (or its meta) that is not an
// object. The caller then writes the CLI-built envelope, as it does for every
// failure that has no API document at all.
func apiErrorEnvelope(doc []byte, status, detail string, code int, raw bool) ([]byte, bool) {
	top, ok := parseJSONObject(doc)
	if !ok {
		return nil, false
	}
	errorsAt := top.lookup("errors")
	if errorsAt < 0 {
		return nil, false
	}
	var items []json.RawMessage
	if json.Unmarshal(top[errorsAt].value, &items) != nil || len(items) == 0 {
		return nil, false
	}

	exitCode := json.RawMessage(strconv.Itoa(code))
	out := make([]json.RawMessage, 0, len(items))
	for i, item := range items {
		obj, ok := parseJSONObject(item)
		if !ok {
			return nil, false
		}
		switch {
		case !raw && i == 0:
			obj = obj.set("status", jsonString(status))
			obj = obj.set("detail", jsonString(detail))
		case obj.lookup("status") < 0:
			obj = obj.set("status", jsonString(status))
		}
		if obj, ok = obj.withMetaMember("exit_code", exitCode); !ok {
			return nil, false
		}
		out = append(out, obj.marshal())
	}
	errorsValue := marshalJSONArray(out)

	var result []byte
	if raw {
		top[errorsAt].value = errorsValue
		result = top.marshal()
	} else {
		result = jsonObject{{key: jsonString("errors"), name: "errors", value: errorsValue}}.marshal()
	}

	var buf bytes.Buffer
	if json.Indent(&buf, result, "", "  ") != nil {
		return nil, false
	}
	buf.WriteByte('\n')
	return buf.Bytes(), true
}

// jsonMember is one member of a JSON object, kept as the bytes that arrived:
// key is the raw, still-escaped key token and value the raw value, so a
// document passed through untouched is byte-identical to its input apart from
// whitespace. name is the decoded key, for lookups.
type jsonMember struct {
	key   []byte
	name  string
	value json.RawMessage
}

// jsonObject is an ORDERED JSON object. encoding/json's map[string]any would
// sort members, turn every number into a float64 (a 20-digit id prints as
// 1.2345678901234567e+19) and re-escape strings (`<` is written back as a backslash-u escape) — none of
// which is "the API's document".
type jsonObject []jsonMember

// parseJSONObject parses b as a single JSON object, keeping each member's raw
// bytes. ok is false for anything else (an array, a scalar, trailing data).
func parseJSONObject(b []byte) (jsonObject, bool) {
	dec := json.NewDecoder(bytes.NewReader(b))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, false
	}
	obj := jsonObject{}
	for dec.More() {
		start := dec.InputOffset()
		tok, err := dec.Token()
		if err != nil {
			return nil, false
		}
		name, isKey := tok.(string)
		if !isKey {
			return nil, false
		}
		// Between the previous token's end and this key's end there is only
		// whitespace, the separating comma and the key's own raw token.
		rawKey := bytes.TrimLeft(b[start:dec.InputOffset()], " \t\r\n,")
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return nil, false
		}
		obj = append(obj, jsonMember{key: append([]byte(nil), rawKey...), name: name, value: value})
	}
	if tok, err := dec.Token(); err != nil || tok != json.Delim('}') {
		return nil, false
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, false
	}
	return obj, true
}

// lookup returns the index of the member named name, or -1. With duplicate
// names it returns the LAST one, which is the one encoding/json — and so the
// client's decode of the same body — honours.
func (o jsonObject) lookup(name string) int {
	for i := len(o) - 1; i >= 0; i-- {
		if o[i].name == name {
			return i
		}
	}
	return -1
}

// set gives the member named name the value v: in place when it exists (any
// duplicate of it is dropped, so exactly one remains), appended when it does
// not.
func (o jsonObject) set(name string, v json.RawMessage) jsonObject {
	at := o.lookup(name)
	if at < 0 {
		return append(o, jsonMember{key: jsonString(name), name: name, value: v})
	}
	out := make(jsonObject, 0, len(o))
	for i, m := range o {
		switch {
		case i == at:
			m.value = v
			out = append(out, m)
		case m.name == name:
			// an earlier duplicate — drop it
		default:
			out = append(out, m)
		}
	}
	return out
}

// withMetaMember sets meta.<name> = v, merging into the object's own meta: an
// absent or null meta becomes {name: v}; an object meta keeps every member it
// had. ok is false when meta is some other JSON type, which the CLI cannot add
// to without destroying it.
func (o jsonObject) withMetaMember(name string, v json.RawMessage) (jsonObject, bool) {
	at := o.lookup("meta")
	if at < 0 || string(bytes.TrimSpace(o[at].value)) == "null" {
		meta := jsonObject{{key: jsonString(name), name: name, value: v}}
		return o.set("meta", meta.marshal()), true
	}
	meta, ok := parseJSONObject(o[at].value)
	if !ok {
		return nil, false
	}
	return o.set("meta", meta.set(name, v).marshal()), true
}

func (o jsonObject) marshal() json.RawMessage {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, m := range o {
		if i > 0 {
			buf.WriteByte(',')
		}
		buf.Write(m.key)
		buf.WriteByte(':')
		buf.Write(m.value)
	}
	buf.WriteByte('}')
	return buf.Bytes()
}

func marshalJSONArray(items []json.RawMessage) json.RawMessage {
	var buf bytes.Buffer
	buf.WriteByte('[')
	for i, it := range items {
		if i > 0 {
			buf.WriteByte(',')
		}
		buf.Write(it)
	}
	buf.WriteByte(']')
	return buf.Bytes()
}

// jsonString encodes s as a JSON string without HTML escaping, matching the
// CLI-built envelope's encoder (SetEscapeHTML(false)).
func jsonString(s string) json.RawMessage {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s) // a Go string always encodes
	return json.RawMessage(strings.TrimSuffix(buf.String(), "\n"))
}
