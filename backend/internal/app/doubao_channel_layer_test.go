package app

import "testing"

// 账号池模型键到能力的映射：图层拆分模型必须是 image 能力，且不能影响既有豆包 / Dola 视频键。
func TestDoubaoPoolModelCapabilityCoversLayerDecomposition(t *testing.T) {
	cases := []struct {
		model string
		want  string
		suppo bool
	}{
		{model: doubaoPoolImageModel, want: "image", suppo: true},
		{model: doubaoPoolLayerModel, want: "image", suppo: true},
		{model: "doubao-seedream-layer-decomposition", want: "image", suppo: true},
		{model: "seedream-v5.0-pro-layer-decomposition", want: "image", suppo: true},
		{model: "doubao-seedream-5-pro-layerize", want: "image", suppo: true},
		{model: "doubao-seedance-video", want: "video", suppo: true},
		{model: dolaPoolVideoModel25, want: "video", suppo: true},
		{model: "doubao-seedream-4-0-250828", want: "", suppo: false},
	}
	for _, tc := range cases {
		got, ok := doubaoPoolModelCapability(tc.model)
		if ok != tc.suppo || got != tc.want {
			t.Fatalf("doubaoPoolModelCapability(%q) = (%q,%v), want (%q,%v)", tc.model, got, ok, tc.want, tc.suppo)
		}
	}
}

func TestIsDoubaoPoolLayerModel(t *testing.T) {
	if !isDoubaoPoolLayerModel(doubaoPoolLayerModel) {
		t.Fatal("账号池图层拆分模型键必须被识别")
	}
	for _, model := range []string{"", "doubao-seedream-image", "dola-seedance-video-2.5"} {
		if isDoubaoPoolLayerModel(model) {
			t.Fatalf("%q 不应被识别为图层拆分模型", model)
		}
	}
}

// 层数来源优先级：metadata.layerCount > config.count > 默认 3 层，并统一收敛到上限。
func TestDoubaoPoolLayerCountResolution(t *testing.T) {
	cases := []struct {
		name  string
		input canvasGenerationInput
		want  int
	}{
		{name: "metadata 优先于 config", input: canvasGenerationInput{Config: providerConfig{Count: "1"}, Metadata: map[string]interface{}{"layerCount": float64(4)}}, want: 4},
		{name: "回落 config.count", input: canvasGenerationInput{Config: providerConfig{Count: "2"}}, want: 2},
		{name: "缺省默认层数", input: canvasGenerationInput{}, want: doubaoPoolDefaultLayers},
		{name: "超过上限收敛", input: canvasGenerationInput{Metadata: map[string]interface{}{"layerCount": float64(99)}}, want: doubaoPoolMaxLayers},
		{name: "非法值回落默认", input: canvasGenerationInput{Config: providerConfig{Count: "x"}, Metadata: map[string]interface{}{"layerCount": "abc"}}, want: doubaoPoolDefaultLayers},
		{name: "负数回落默认", input: canvasGenerationInput{Metadata: map[string]interface{}{"layerCount": float64(-3)}}, want: doubaoPoolDefaultLayers},
	}
	for _, tc := range cases {
		if got := doubaoPoolLayerCount(tc.input); got != tc.want {
			t.Fatalf("%s: doubaoPoolLayerCount = %d, want %d", tc.name, got, tc.want)
		}
	}
}

// 选区解析：坐标收敛到 0-1000，非法项跳过而不是让整批选区失效。
func TestDoubaoPoolLayerRegionsParseAndClamp(t *testing.T) {
	input := canvasGenerationInput{Metadata: map[string]interface{}{"layerRegions": []interface{}{
		[]interface{}{float64(0), float64(10), float64(20), float64(30)},
		[]interface{}{float64(-5.4), float64(2000), float64(500.6), float64(600)},
		[]interface{}{float64(1), float64(2), float64(3)},
		"bad",
	}}}
	regions := doubaoPoolLayerRegions(input)
	want := []struct{ x1, y1, x2, y2 int }{{0, 10, 20, 30}, {0, 1000, 501, 600}}
	if len(regions) != len(want) {
		t.Fatalf("regions = %#v, want %d 项", regions, len(want))
	}
	for index, expected := range want {
		got := regions[index]
		if got.X1 != expected.x1 || got.Y1 != expected.y1 || got.X2 != expected.x2 || got.Y2 != expected.y2 {
			t.Fatalf("regions[%d] = %#v, want %#v", index, got, expected)
		}
	}

	// 兼容旧字段名 metadata.regions，且空/缺失时返回 nil（提示词退化为无选区）。
	legacy := canvasGenerationInput{Metadata: map[string]interface{}{"regions": []interface{}{[]interface{}{float64(1), float64(2), float64(3), float64(4)}}}}
	if got := doubaoPoolLayerRegions(legacy); len(got) != 1 || got[0].X2 != 3 {
		t.Fatalf("legacy regions = %#v", got)
	}
	if got := doubaoPoolLayerRegions(canvasGenerationInput{}); got != nil {
		t.Fatalf("空 metadata 应返回 nil，得到 %#v", got)
	}
}
