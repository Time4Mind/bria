// Package cardtranscript renders typed provider history without transport or
// persistence dependencies. Plain legacy entries remain ordinary Markdown.
package cardtranscript

import (
	"encoding/json"
	"html"
	"strings"

	"bria/internal/tooltext"
)

const Separator = "\n\n\u00a0\n\n"

type Block struct {
	Kind              string
	Text              string
	FinalContinuation bool   // Exact persisted identity proves this is a later chunk of one final.
	FinalOperationID  string `json:",omitempty"` // Exact final operation, supplied by trusted history metadata.
	CommandLines      int    // Zero and unsupported values select ten command lines.
	ToolLines         int    // Zero and unsupported values select ten output lines.
}

// Tool updates share an ID; a result enriches its call, never another tool.
type Tool = tooltext.Tool

// EncodeTool retains bounded metadata within the existing persisted history
// entry limit. Long IDs are hashed, never prefix-truncated into collisions.
func EncodeTool(tool Tool) string {
	return tooltext.Encode(tool)
}

func Render(blocks []Block) []string {
	rendered := RenderBlocks(blocks)
	result := make([]string, len(rendered))
	for i, block := range rendered {
		result[i] = block.Text
	}
	return result
}

// RenderBlocks retains event identity for pagination; final boundaries must
// never be inferred from provider-authored text.
func RenderBlocks(blocks []Block) []Block {
	blocks = mergeTools(blocks)
	result := make([]Block, 0, len(blocks))
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
			text = renderTool(text, block.CommandLines, block.ToolLines)
		default:
			text = NormalizeMarkdown(text)
		}
		result = append(result, Block{Kind: block.Kind, Text: text, FinalContinuation: block.FinalContinuation, FinalOperationID: block.FinalOperationID})
	}
	return result
}

func mergeTools(blocks []Block) []Block {
	result := make([]Block, 0, len(blocks))
	indexes := map[string]int{}
	for _, block := range blocks {
		tool, valid := tooltext.Decode(block.Text)
		if block.Kind != "tool" || !valid || tool.ID == "" {
			result = append(result, block)
			continue
		}
		if index, ok := indexes[tool.ID]; ok {
			previous, _ := tooltext.Decode(result[index].Text)
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
			tool.Truncated = tool.Truncated || previous.Truncated
			encoded, _ := json.Marshal(tool)
			result[index].Text = string(encoded)
			continue
		}
		indexes[tool.ID] = len(result)
		result = append(result, block)
	}
	return result
}

func renderTool(text string, commandLines, outputLines int) string {
	tool, valid := tooltext.Decode(text)
	if !valid || tool.Name == "" {
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
	body, commandCut := tooltext.Bound(tool.Arguments, displayBudget(commandLines))
	output, outputCut := tooltext.Bound(tool.Output, displayBudget(outputLines))
	if output != "" {
		if body != "" {
			body += tooltext.Separator
		}
		body += output
	}
	if body == "" && !tool.Truncated {
		return summary
	}
	if commandCut || outputCut || tool.Truncated {
		body += "\n" + tooltext.Notice
	}
	return details(summary, html.EscapeString(body))
}

func displayBudget(lines int) int {
	if lines == 3 || lines == 5 || lines == 20 {
		return lines
	}
	return 10
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
