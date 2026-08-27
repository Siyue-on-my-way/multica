package agentconfig

// ProviderPreset represents a predefined configuration for a model provider.
type ProviderPreset struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	WebsiteURL  string   `json:"website_url"`
	Category    string   `json:"category"`
	BaseURL     string   `json:"base_url"`
	DefaultModel string   `json:"default_model"`
	Models      []string `json:"models"`
}

// GetProviderPresets returns a list of built-in provider presets.
func GetProviderPresets() []ProviderPreset {
	return []ProviderPreset{
		{
			ID:          "openai",
			Name:        "OpenAI",
			WebsiteURL:  "https://platform.openai.com",
			Category:    "official",
			BaseURL:     "https://api.openai.com/v1",
			DefaultModel: "gpt-4o",
			Models:      []string{"gpt-4o", "gpt-4-turbo", "gpt-3.5-turbo"},
		},
		{
			ID:          "anthropic",
			Name:        "Anthropic",
			WebsiteURL:  "https://console.anthropic.com",
			Category:    "official",
			BaseURL:     "https://api.anthropic.com",
			DefaultModel: "claude-3-5-sonnet-20240620",
			Models:      []string{"claude-3-5-sonnet-20240620", "claude-3-opus-20240229"},
		},
		{
			ID:          "deepseek",
			Name:        "DeepSeek",
			WebsiteURL:  "https://platform.deepseek.com",
			Category:    "official",
			BaseURL:     "https://api.deepseek.com",
			DefaultModel: "deepseek-coder",
			Models:      []string{"deepseek-coder", "deepseek-chat"},
		},
		{
			ID:          "kimi",
			Name:        "Moonshot (Kimi)",
			WebsiteURL:  "https://platform.moonshot.cn",
			Category:    "official",
			BaseURL:     "https://api.moonshot.cn/v1",
			DefaultModel: "moonshot-v1-8k",
			Models:      []string{"moonshot-v1-8k", "moonshot-v1-32k", "moonshot-v1-128k"},
		},
		{
			ID:          "openrouter",
			Name:        "OpenRouter",
			WebsiteURL:  "https://openrouter.ai",
			Category:    "proxy",
			BaseURL:     "https://openrouter.ai/api/v1",
			DefaultModel: "anthropic/claude-3.5-sonnet",
			Models:      []string{"anthropic/claude-3.5-sonnet", "openai/gpt-4o"},
		},
	}
}
