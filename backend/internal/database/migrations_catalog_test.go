package database

import "testing"

func TestModelPresentationFamily(t *testing.T) {
	tests := []struct {
		key    string
		family string
		icon   string
		label  string
	}{
		{key: "grok-4.7", family: "Grok", icon: "Grok", label: "Grok 4.7"},
		{key: "sh-deepseek-v4-pro", family: "DeepSeek", icon: "DeepSeek", label: "DeepSeek v4 pro"},
		{key: "gemini-3.8-flash", family: "Gemini", icon: "Gemini", label: "Gemini 3.8 flash"},
		{key: "gpt-5.6", family: "OpenAI", icon: "OpenAI", label: "OpenAI gpt 5.6"},
	}
	for _, tt := range tests {
		family, icon := modelPresentationFamily(tt.key)
		if family != tt.family || icon != tt.icon {
			t.Fatalf("modelPresentationFamily(%q) = %q, %q; want %q, %q", tt.key, family, icon, tt.family, tt.icon)
		}
		if label := modelPresentationLabel(tt.key, family); label != tt.label {
			t.Fatalf("modelPresentationLabel(%q, %q) = %q; want %q", tt.key, family, label, tt.label)
		}
	}
}
