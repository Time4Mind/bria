package tooltext

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

type Tool struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	Output    string `json:"output"`
	Status    string `json:"status"`
	Encoding  string `json:"encoding,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

// Encode keeps one event in one history record (16 KiB). At most 4039 scalars
// are stored across both fields, plus bounded identity/status/name metadata.
func Encode(tool Tool) string {
	if tool.Encoding == "rune21-v1" {
		raw, _ := json.Marshal(tool)
		decoded, ok := Decode(string(raw))
		if !ok {
			return string(raw)
		}
		tool = decoded
	}
	tool.ID = boundedID(tool.ID)
	tool.Name, tool.Status = prefix(tool.Name, 64), prefix(tool.Status, 64)
	arguments, output := tool.Arguments, tool.Output
	aCut, oCut := false, false
	if tool.Encoding != "text-v1" {
		arguments, aCut = Read(arguments)
		output, oCut = Read(output)
	}
	arguments, cut := Bound(arguments, 40)
	output, outputCut := Bound(output, 40)
	// Preserve unused retention within forty lines, guaranteeing twenty to each
	// nonempty field on overflow. Presentation separators are not content.
	if arguments != "" && output != "" && strings.Count(arguments, "\n")+strings.Count(output, "\n")+2 > 40 {
		var aBound, oBound bool
		arguments, aBound = Bound(arguments, 20)
		output, oBound = Bound(output, 20)
		cut, outputCut = cut || aBound, outputCut || oBound
	}
	tool.Arguments, tool.Output = arguments, output
	tool.Truncated = tool.Truncated || aCut || oCut || cut || outputCut
	tool.Encoding = "text-v1"
	encoded, _ := json.Marshal(tool)
	if len(encoded) > 16384 {
		tool.Arguments, tool.Output = pack(tool.Arguments), pack(tool.Output)
		tool.Encoding = "rune21-v1"
		encoded, _ = json.Marshal(tool)
	}
	return string(encoded)
}

// Decode accepts old JSON and new bounded text/compact envelopes. Invalid
// compact data fails closed instead of being mistaken for readable content.
func Decode(text string) (Tool, bool) {
	var tool Tool
	if json.Unmarshal([]byte(text), &tool) != nil {
		return tool, false
	}
	switch tool.Encoding {
	case "rune21-v1":
		var ok bool
		tool.Arguments, ok = unpack(tool.Arguments)
		if !ok {
			return Tool{}, false
		}
		tool.Output, ok = unpack(tool.Output)
		if !ok {
			return Tool{}, false
		}
	case "":
		tool.Arguments = legacyText(tool.Arguments)
		tool.Output = legacyText(tool.Output)
	case "text-v1":
	default:
		return Tool{}, false
	}
	tool.Encoding = "text-v1"
	tool.ID = boundedID(tool.ID)
	return tool, true
}

func boundedID(id string) string {
	encoded, _ := json.Marshal(id)
	if len(encoded) > 1024 {
		return fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(id)))
	}
	return id
}

func legacyText(raw string) string {
	text, _ := Read(raw)
	if text != raw {
		return text
	}
	text = strings.NewReplacer(`\\n`, "\n", `\\r`, "\r", `\\t`, "\t", `\\/`, "/", `\\\\`, `\\`).Replace(text)
	return strings.NewReplacer(`\n`, "\n", `\r`, "\r", `\t`, "\t", `\/`, "/", `\\`, `\`).Replace(text)
}

func prefix(text string, count int) string {
	for i := range text {
		if count == 0 {
			return text[:i]
		}
		count--
	}
	return text
}

// Fixed 21-bit Unicode scalars bound worst-case size without relying on
// compressibility: 4039 scalars occupy at most 14140 base64 bytes.
func pack(text string) string {
	var data []byte
	var value uint64
	bits := 0
	for _, r := range text {
		value, bits = value<<21|uint64(r), bits+21
		for bits >= 8 {
			bits -= 8
			data = append(data, byte(value>>bits))
		}
	}
	if bits != 0 {
		data = append(data, byte(value<<(8-bits)))
	}
	return base64.RawStdEncoding.EncodeToString(data)
}

func unpack(text string) (string, bool) {
	if len(text) > 14140 {
		return "", false
	}
	data, err := base64.RawStdEncoding.Strict().DecodeString(text)
	if err != nil {
		return "", false
	}
	var result strings.Builder
	var value uint64
	bits := 0
	for _, b := range data {
		value, bits = value<<8|uint64(b), bits+8
		if bits >= 21 {
			bits -= 21
			r := rune(value >> bits & ((1 << 21) - 1))
			if !utf8.ValidRune(r) {
				return "", false
			}
			result.WriteRune(r)
		}
	}
	return result.String(), bits < 8 && value&((1<<bits)-1) == 0
}
