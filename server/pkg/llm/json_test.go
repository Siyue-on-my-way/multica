package llm

import "testing"

func TestStripJSONFence(t *testing.T) {
	const payload = `{"content":"ok"}`
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "unfenced", raw: payload, want: payload},
		{name: "leading whitespace", raw: " \n\t" + payload + " \n", want: payload},
		{name: "json fenced", raw: "```json\n" + payload + "\n```", want: payload},
		{name: "bare fenced", raw: "```\n" + payload + "\n```", want: payload},
		{name: "crlf fenced", raw: "```json\r\n" + payload + "\r\n```", want: payload},
		{name: "unclosed fence remains invalid", raw: "```json\n" + payload, want: "```json\n" + payload},
		{name: "trailing prose remains invalid", raw: "```json\n" + payload + "\n```\nDone", want: "```json\n" + payload + "\n```\nDone"},
		{name: "prose before fence remains invalid", raw: "Here is JSON:\n```json\n" + payload + "\n```", want: "Here is JSON:\n```json\n" + payload + "\n```"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := StripJSONFence(test.raw); got != test.want {
				t.Fatalf("StripJSONFence() = %q, want %q", got, test.want)
			}
		})
	}
}
