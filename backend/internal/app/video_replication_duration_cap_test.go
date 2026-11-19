package app

import (
	"reflect"
	"testing"
)

// 上游 yuxibox seedance2.5-flash-官转 声明 5–30 整秒，但单独拒绝 12 秒。
// 后台把该模型改成 enum 排除 12 后，服务端必须从最新能力配置重写 ShotSeconds，
// 让旧项目「再次生成」也不会吸附到 12 秒。
func TestVideoReplicationDurationSecondsFromProfile(t *testing.T) {
	t.Run("enum excludes upstream-rejected 12s", func(t *testing.T) {
		profile := &VideoCapabilityConfig{Duration: VideoDurationConfig{
			Selection: "enum",
			Values:    []int{5, 6, 7, 8, 9, 10, 11, 13, 14, 15, 20, 30},
			Default:   5,
		}}
		got := videoReplicationDurationSecondsFromProfile(profile)
		want := []int{5, 6, 7, 8, 9, 10, 11, 13, 14, 15, 20, 30}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("enum seconds = %v, want %v", got, want)
		}
		for _, v := range got {
			if v == 12 {
				t.Fatal("12s must be excluded")
			}
		}
	})

	t.Run("range expands by step", func(t *testing.T) {
		profile := &VideoCapabilityConfig{Duration: VideoDurationConfig{
			Selection: "range", Min: 5, Max: 15, Step: 5, Default: 5,
		}}
		got := videoReplicationDurationSecondsFromProfile(profile)
		want := []int{5, 10, 15}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("range seconds = %v, want %v", got, want)
		}
	})

	t.Run("enum is sorted and deduplicated", func(t *testing.T) {
		profile := &VideoCapabilityConfig{Duration: VideoDurationConfig{
			Selection: "enum", Values: []int{10, 5, 10, 8}, Default: 5,
		}}
		got := videoReplicationDurationSecondsFromProfile(profile)
		want := []int{5, 8, 10}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("sorted seconds = %v, want %v", got, want)
		}
	})

	t.Run("durationSupported false does not rewrite", func(t *testing.T) {
		unsupported := false
		profile := &VideoCapabilityConfig{
			DurationSupported: &unsupported,
			Duration:          VideoDurationConfig{Selection: "enum", Values: []int{5, 10}, Default: 5},
		}
		if got := videoReplicationDurationSecondsFromProfile(profile); got != nil {
			t.Fatalf("durationSupported=false should not rewrite, got %v", got)
		}
	})

	t.Run("nil profile does not rewrite", func(t *testing.T) {
		if got := videoReplicationDurationSecondsFromProfile(nil); got != nil {
			t.Fatalf("nil profile should not rewrite, got %v", got)
		}
	})
}

// 验证吸附逻辑： ShotSeconds 去掉 12 后，原本落到 12 秒的时长会吸附到合法的 11 或 13。
func TestSnapVideoReplicationSecondsSkipsExcluded12(t *testing.T) {
	allowed := []int{5, 6, 7, 8, 9, 10, 11, 13, 14, 15}
	got := snapVideoReplicationSeconds(12, allowed)
	if got == 12 {
		t.Fatal("snapped to excluded 12s")
	}
	if got != 11 && got != 13 {
		t.Fatalf("12s should snap to 11 or 13, got %d", got)
	}
}
