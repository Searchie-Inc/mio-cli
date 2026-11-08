package main

import (
	"bytes"
	"encoding/json"
	"io"
	"strconv"
	"strings"
)

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

type jsonMember struct {
	key   []byte
	name  string
	value json.RawMessage
}

type jsonObject []jsonMember

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

func (o jsonObject) lookup(name string) int {
	for i := len(o) - 1; i >= 0; i-- {
		if o[i].name == name {
			return i
		}
	}
	return -1
}

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
		default:
			out = append(out, m)
		}
	}
	return out
}

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

func jsonString(s string) json.RawMessage {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return json.RawMessage(strings.TrimSuffix(buf.String(), "\n"))
}
