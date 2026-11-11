package install

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// leadingSpace returns the run of spaces and tabs at the start of b.
func leadingSpace(b []byte) []byte {
	i := 0
	for i < len(b) && (b[i] == ' ' || b[i] == '\t') {
		i++
	}
	return b[:i]
}

// lineIndent returns the indentation of the line that holds src[pos].
func lineIndent(src []byte, pos int) []byte {
	return leadingSpace(src[bytes.LastIndexByte(src[:pos], '\n')+1:])
}

// spliceInto adds raw to the multi-line object or array src[start:end],
// matching the file's indentation. With key set the container is an object and
// the new member is "key": raw. It reports false when the container is not
// laid out over several lines, so the caller can fall back to sjson.
func spliceInto(src []byte, start, end int, key string, raw []byte) ([]byte, bool) {
	if start < 0 || end > len(src) || end-start < 2 || !bytes.Contains(src[start:end], []byte("\n")) {
		return nil, false
	}
	open, closer := src[start], src[end-1]
	if (open != '{' || closer != '}') && (open != '[' || closer != ']') {
		return nil, false
	}
	base := string(lineIndent(src, start))
	body := src[start+1 : end-1]
	trimmed := bytes.TrimRight(body, " \t\r\n")
	empty := len(bytes.TrimSpace(body)) == 0
	unit, item := "  ", base+"  "
	if !empty {
		first := start + 1 + (len(body) - len(bytes.TrimLeft(body, " \t\r\n")))
		item = string(lineIndent(src, first))
		if len(item) > len(base) && item[:len(base)] == base {
			unit = item[len(base):]
		}
	}
	var val bytes.Buffer
	if err := json.Indent(&val, raw, item, unit); err != nil {
		return nil, false
	}
	// Keep the file's line ends: a file with CRLF gets CRLF in the new lines.
	nl := "\n"
	if bytes.Contains(src, []byte("\r\n")) {
		nl = "\r\n"
	}
	text := strings.ReplaceAll(val.String(), "\n", nl)
	if key != "" {
		k, _ := json.Marshal(key)
		text = string(k) + ": " + text
	}
	var out []byte
	if empty {
		out = append(out, src[:start+1]...)
		out = append(out, nl+item+text+nl+base...)
		out = append(out, src[end-1:]...)
		return out, true
	}
	at := start + 1 + len(trimmed)
	out = append(out, src[:at]...)
	out = append(out, ","+nl+item+text...)
	out = append(out, src[at:]...)
	return out, true
}

// appendToArray appends raw to the array at path (which must exist).
func appendToArray(src []byte, path string, raw []byte) ([]byte, error) {
	arr := gjson.GetBytes(src, path)
	if arr.Index > 0 {
		if out, ok := spliceInto(src, arr.Index, arr.Index+len(arr.Raw), "", raw); ok {
			return out, nil
		}
	}
	return sjson.SetRawBytes(src, path+".-1", raw)
}

// setMember adds key to the object at objPath ("" for the root) with value raw.
// The key must not exist yet.
func setMember(src []byte, objPath, key string, raw []byte) ([]byte, error) {
	if objPath == "" {
		start := len(src) - len(bytes.TrimLeft(src, " \t\r\n"))
		end := len(bytes.TrimRight(src, " \t\r\n"))
		if out, ok := spliceInto(src, start, end, key, raw); ok {
			return out, nil
		}
		return sjson.SetRawBytes(src, key, raw)
	}
	obj := gjson.GetBytes(src, objPath)
	if obj.Index > 0 {
		if out, ok := spliceInto(src, obj.Index, obj.Index+len(obj.Raw), key, raw); ok {
			return out, nil
		}
	}
	return sjson.SetRawBytes(src, objPath+"."+key, raw)
}
