package protocol

import "testing"

func TestGeminiMessagePartsMapsTextAndDataURL(t *testing.T) {
	textParts := geminiMessageParts("inspect this")
	if len(textParts) != 1 || textParts[0].(map[string]any)["text"] != "inspect this" {
		t.Fatalf("text parts = %#v", textParts)
	}

	dataParts := geminiMessageParts("data:image/png;base64,aGVsbG8=")
	if len(dataParts) != 1 {
		t.Fatalf("data parts = %#v", dataParts)
	}
	inline, ok := dataParts[0].(map[string]any)["inlineData"].(map[string]any)
	if !ok || inline["mimeType"] != "image/png" || inline["data"] != "aGVsbG8=" {
		t.Fatalf("inline data = %#v", dataParts)
	}
}

func TestGeminiMessagePartsMapsOpenAIStyleRemoteMedia(t *testing.T) {
	parts := geminiMessageParts([]any{
		map[string]any{"type": "text", "text": "look"},
		map[string]any{"type": "image_url", "image_url": map[string]any{"url": "https://cdn.example/image.png", "mimeType": "image/png"}},
		map[string]any{"type": "video_url", "video_url": map[string]any{"url": "https://cdn.example/video.mp4"}},
	})
	if len(parts) != 3 {
		t.Fatalf("parts = %#v", parts)
	}
	if got := parts[0].(map[string]any)["text"]; got != "look" {
		t.Fatalf("text part = %#v", got)
	}
	image, ok := parts[1].(map[string]any)["fileData"].(map[string]any)
	if !ok || image["mimeType"] != "image/png" || image["fileUri"] != "https://cdn.example/image.png" {
		t.Fatalf("image fileData = %#v", image)
	}
	video, ok := parts[2].(map[string]any)["fileData"].(map[string]any)
	if !ok || video["mimeType"] != "video/mp4" || video["fileUri"] != "https://cdn.example/video.mp4" {
		t.Fatalf("video fileData = %#v", video)
	}
}
