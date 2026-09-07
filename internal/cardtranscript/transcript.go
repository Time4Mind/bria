// Package cardtranscript renders typed provider history without transport or
// persistence dependencies. Plain legacy entries remain ordinary Markdown.
package cardtranscript

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"html"
	"strings"
	"unicode/utf8"
)

const Separator = "\n\n\u00a0\n\n"

type Block struct {
	Kind string
	Text string
}

// Tool updates share an ID; a result enriches its call, never another tool.
type Tool struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	Output    string `json:"output"`
	Status    string `json:"status"`
}

// EncodeTool retains bounded metadata within the existing persisted history
// entry limit. Long IDs are hashed, never prefix-truncated into collisions.
func EncodeTool(tool Tool) string {
	if len(tool.ID) > 1024 {
		tool.ID = fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(tool.ID)))
	}
	tool.Name = limitBytes(tool.Name, 512)
	tool.Status = limitBytes(tool.Status, 64)
	tool.Arguments = limitBytes(tool.Arguments, 6000)
	tool.Output = limitBytes(tool.Output, 6000)
	for {
		encoded, _ := json.Marshal(tool)
		if len(encoded) <= 16384 {
			return string(encoded)
		}
		if len(tool.Arguments) >= len(tool.Output) {
			tool.Arguments = limitBytes(tool.Arguments, len(tool.Arguments)/2)
		} else {
			tool.Output = limitBytes(tool.Output, len(tool.Output)/2)
		}
	}
}

func Render(blocks []Block) []string {
	blocks = mergeTools(blocks)
	result := make([]string, 0, len(blocks))
	for _, block := range blocks {
		text := strings.TrimSpace(block.Text)
		if text == "" {
			continue
		}
		switch block.Kind {
		case "prompt":
			text = html.EscapeString(limit(text, 200))
		case "thinking":
			text = details("∴ thinking", html.EscapeString(text))
		case "tool":
			text = renderTool(text)
		default:
			text = NormalizeMarkdown(text)
		}
		result = append(result, text)
	}
	return result
}

func mergeTools(blocks []Block) []Block {
	result := make([]Block, 0, len(blocks))
	indexes := map[string]int{}
	for _, block := range blocks {
		var tool Tool
		if block.Kind != "tool" || json.Unmarshal([]byte(block.Text), &tool) != nil || tool.ID == "" {
			result = append(result, block)
			continue
		}
		if index, ok := indexes[tool.ID]; ok {
			var previous Tool
			_ = json.Unmarshal([]byte(result[index].Text), &previous)
			if tool.Name == "" {
				tool.Name = previous.Name
			}
			if tool.Arguments == "" {
				tool.Arguments = previous.Arguments
			}
			if tool.Output == "" {
				tool.Output = previous.Output
			}
			if tool.Status == "" {
				tool.Status = previous.Status
			}
			encoded, _ := json.Marshal(tool)
			result[index].Text = string(encoded)
			continue
		}
		indexes[tool.ID] = len(result)
		result = append(result, block)
	}
	return result
}

func renderTool(text string) string {
	var tool Tool
	if json.Unmarshal([]byte(text), &tool) != nil || tool.Name == "" {
		return html.EscapeString(limit(text, 64))
	}
	status := "✓"
	if tool.Status == "running" || tool.Status == "in_progress" || tool.Status == "started" {
		status = "…"
	}
	if tool.Status == "failed" || tool.Status == "error" {
		status = "✗"
	}
	summary := html.EscapeString(limit(status+" "+tool.Name, 64))
	body := strings.TrimSpace(decodeTransportEscapes(tool.Arguments))
	if tool.Output != "" {
		lines := strings.Split(decodeTransportEscapes(tool.Output), "\n")
		if len(lines) > 10 {
			lines = append(lines[:10], "…")
		}
		if body != "" {
			body += "\n\n---\n\n"
		}
		body += strings.Join(lines, "\n")
	}
	if body == "" {
		return summary
	}
	// Keep an individual spoiler atomic inside a page; full text stays stored.
	body = limitBytes(body, 1900)
	return details(summary, html.EscapeString(body))
}

// decodeTransportEscapes converts escaped control characters produced by CLI
// transports into their display form before HTML escaping. JSON decoding does
// not help when the provider has already escaped the payload once more.
func decodeTransportEscapes(text string) string {
	if !strings.Contains(text, `\`) {
		return text
	}
	// Some CLI bridges escape an already escaped payload (for example
	// `\\n`). Collapse those sequences before handling single escapes.
	text = strings.NewReplacer(
		`\\n`, "\n",
		`\\r`, "\r",
		`\\t`, "\t",
		`\\/`, "/",
		`\\\\`, `\\`,
	).Replace(text)
	var b strings.Builder
	b.Grow(len(text))
	for i := 0; i < len(text); i++ {
		if text[i] != '\\' || i+1 >= len(text) {
			b.WriteByte(text[i])
			continue
		}
		i++
		switch text[i] {
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 't':
			b.WriteByte('\t')
		case '/', '\\':
			b.WriteByte(text[i])
		default:
			// Unknown escapes are shown without the transport slash.
			b.WriteByte(text[i])
		}
	}
	return b.String()
}

func details(summary, body string) string {
	return "<details><summary>" + summary + "</summary>\n\n" + body + "\n\n</details>"
}

func limit(text string, count int) string {
	runes := []rune(text)
	if len(runes) <= count {
		return text
	}
	return string(runes[:count-1]) + "…"
}

func limitBytes(text string, count int) string {
	if len(text) <= count {
		return text
	}
	end := count - 3
	for !utf8.RuneStart(text[end]) {
		end--
	}
	return text[:end] + "…"
}

// NormalizeMarkdown closes unmatched fences and isolates tables. HTML outside
// fences is escaped; provider text cannot manufacture structural card blocks.
func NormalizeMarkdown(text string) string {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	result := make([]string, 0, len(lines)+2)
	inFence := false
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
			result = append(result, strings.TrimSpace(line))
			continue
		}
		if !inFence {
			if strings.HasPrefix(strings.TrimSpace(line), "|") && i > 0 && strings.TrimSpace(lines[i-1]) != "" && !strings.HasPrefix(strings.TrimSpace(lines[i-1]), "|") {
				result = append(result, "")
			}
			line = html.EscapeString(line)
		}
		result = append(result, line)
	}
	if inFence {
		result = append(result, "```")
	}
	return strings.Join(result, "\n")
}
