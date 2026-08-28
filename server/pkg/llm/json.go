package llm

import "strings"

// StripJSONFence removes one complete Markdown code fence around a JSON
// response. It deliberately does not search for an arbitrary JSON fragment:
// a response with prose, a missing closing fence, or trailing text remains
// invalid and must be handled by the caller instead of being silently
// accepted.
func StripJSONFence(raw string) string {
	value := strings.TrimSpace(strings.ReplaceAll(raw, "\r\n", "\n"))
	if !strings.HasPrefix(value, "```") {
		return value
	}

	lines := strings.Split(value, "\n")
	if len(lines) < 3 || !strings.HasPrefix(strings.TrimSpace(lines[0]), "```") {
		return value
	}
	last := len(lines) - 1
	if strings.TrimSpace(lines[last]) != "```" {
		return value
	}
	return strings.TrimSpace(strings.Join(lines[1:last], "\n"))
}
