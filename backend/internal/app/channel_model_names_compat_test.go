package app

import (
	"reflect"
	"testing"

	"infinite-canvas/backend/internal/model"
)

// 旧导入工具把 models_json 写成对象数组；如果兼容清单解析不出来，
// EnsureSystemChannelModels 会在每次启动时把该渠道全部模型关闭。
func TestChannelModelNamesAcceptsLegacyObjectList(t *testing.T) {
	channel := model.ModelChannel{
		ModelsJSON: `[{"model":"gpt-image-2.5","name":"","caps":["image"],"protocol":""},{"model":"nano-banana2","caps":["image"]}]`,
	}
	got := channelModelNames(channel)
	want := []string{"gpt-image-2.5", "nano-banana2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("channelModelNames() = %#v, want %#v", got, want)
	}
}

func TestChannelModelNamesKeepsStringListBehaviour(t *testing.T) {
	channel := model.ModelChannel{ModelsJSON: `["wan3.0-video", " wan3.0-video ", "grok-imagine-video"]`}
	got := channelModelNames(channel)
	want := []string{"wan3.0-video", "grok-imagine-video"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("channelModelNames() = %#v, want %#v", got, want)
	}
}
