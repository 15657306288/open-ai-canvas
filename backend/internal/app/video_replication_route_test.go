package app

import (
	"reflect"
	"testing"
)

func routeTestModels() (short, long, script videoReplicationRouteModel) {
	seconds := func(low, high int) []int {
		values := []int{}
		for value := low; value <= high; value++ {
			values = append(values, value)
		}
		return values
	}
	refOps := []string{"text_to_video", "image_to_video", "reference_to_video"}
	short = videoReplicationRouteModel{Ref: EcommerceAgentModelRef{ChannelID: "c", ModelKey: "ref-15s"}, Label: "15s", Durations: seconds(4, 15), MaxImages: 9, Operations: refOps, MaxRefVideos: 3, MaxRefVideoSeconds: 15}
	long = videoReplicationRouteModel{Ref: EcommerceAgentModelRef{ChannelID: "c", ModelKey: "ref-30s"}, Label: "30s", Durations: seconds(4, 30), MaxImages: 9, Operations: refOps, MaxRefVideos: 3, MaxRefVideoSeconds: 15}
	script = videoReplicationRouteModel{Ref: EcommerceAgentModelRef{ChannelID: "c", ModelKey: "i2v-15s"}, Label: "i2v", Durations: seconds(4, 15), MaxImages: 1, Operations: []string{"image_to_video"}}
	return
}

// 价格：15 秒模型 1 积分/秒，30 秒模型 2 积分/秒，脚本模型 0.5 积分/秒。
func routeTestPrice(candidate videoReplicationRouteModel, seconds int, _ []int64) (int64, bool) {
	rate := map[string]int64{"ref-15s": 1_000_000, "ref-30s": 2_000_000, "i2v-15s": 500_000}[candidate.Ref.ModelKey]
	return rate * int64(seconds), true
}

func routeTestPick(t *testing.T, sourceMs int64, needsFaceSafe bool, preference string, models ...videoReplicationRouteModel) VideoReplicationRouteOption {
	t.Helper()
	options := []VideoReplicationRouteOption{}
	for _, item := range models {
		options = append(options, planVideoReplicationRouteOption(item, sourceMs, routeTestPrice))
	}
	best, reason := rankVideoReplicationRoute(options, needsFaceSafe, preference)
	if best == nil {
		t.Fatalf("no route: %s", reason)
	}
	return *best
}

func TestVideoReplicationRoutePicksModelBySourceDuration(t *testing.T) {
	short, long, script := routeTestModels()
	// 15 秒内：两个模型都一次完成，选便宜的 15 秒模型。
	if best := routeTestPick(t, 12_000, false, "", short, long, script); best.Model.ModelKey != "ref-15s" || best.Tasks != 1 || best.ClipsPerTask != 1 {
		t.Fatalf("12s source should use the 15s model once: %+v", best)
	}
	// 15–30 秒：30 秒模型一次完成（原片切成 2 个参考视频），优先于 15 秒模型的两段拼接。
	best := routeTestPick(t, 25_000, false, "", short, long, script)
	if best.Model.ModelKey != "ref-30s" || best.Tasks != 1 || best.ClipsPerTask != 2 || best.TaskSeconds[0] != 25 {
		t.Fatalf("25s source should use the 30s model once with two reference clips: %+v", best)
	}
	// 价格优先时同一条视频改用两段 15 秒模型。
	if best := routeTestPick(t, 25_000, false, VideoReplicationRouteCost, short, long, script); best.Model.ModelKey != "ref-15s" || best.Tasks != 2 {
		t.Fatalf("cost preference should pick the cheaper 2×15s route: %+v", best)
	}
	// 超过 30 秒：30 秒模型 2 段少于 15 秒模型 3 段。
	if best := routeTestPick(t, 45_000, false, "", short, long, script); best.Model.ModelKey != "ref-30s" || best.Tasks != 2 {
		t.Fatalf("45s source should use the 30s model in two tasks: %+v", best)
	}
}

func TestVideoReplicationRouteRespectsReferenceCoverage(t *testing.T) {
	short, long, _ := routeTestModels()
	// 只能传 1 个参考视频时，30 秒模型一次最多覆盖 15 秒原片，和 15 秒模型一样要分两段，于是选便宜的。
	single := long
	single.MaxRefVideos = 1
	if single.taskSeconds() != 15 {
		t.Fatalf("one 15s reference video caps the task at 15s, got %d", single.taskSeconds())
	}
	if best := routeTestPick(t, 25_000, false, "", short, single); best.Model.ModelKey != "ref-15s" {
		t.Fatalf("with single reference video the cheaper model should win: %+v", best)
	}
	// 参考视频合计 15 秒上限（如 Seedance 2.0）同样把单次任务限制在 15 秒。
	capped := long
	capped.RefTotalSeconds = 15
	if capped.taskSeconds() != 15 {
		t.Fatalf("total reference cap should limit task seconds, got %d", capped.taskSeconds())
	}
}

func TestVideoReplicationRouteFaceSafeAndScriptTrack(t *testing.T) {
	short, long, script := routeTestModels()
	script.FaceSafe = true
	// 换人且只有脚本模型不卡人脸：选脚本复刻。
	if best := routeTestPick(t, 25_000, true, "", short, long, script); best.Model.ModelKey != "i2v-15s" || best.Track != VideoReplicationTrackScript || best.ClipsPerTask != 0 {
		t.Fatalf("face-safe filter should pick the script model: %+v", best)
	}
	// 不需要换人时仍优先参考视频复刻。
	if best := routeTestPick(t, 25_000, false, "", short, long, script); best.Track != VideoReplicationTrackReference {
		t.Fatalf("reference track should win when faces are not replaced: %+v", best)
	}
	// 后台把参考视频模型显式标为脚本复刻时不再上传参考视频。
	forced := long
	forced.Track = VideoReplicationTrackScript
	if forced.track() != VideoReplicationTrackScript || forced.taskSeconds() != 30 {
		t.Fatalf("explicit script track must not use reference clips: %s %d", forced.track(), forced.taskSeconds())
	}
}

func TestVideoReplicationClipParts(t *testing.T) {
	cases := []struct {
		length, perClip int64
		maxClips, total int
		want            []int64
	}{
		{12_000, 15_000, 3, 0, []int64{12_000}},
		{25_000, 15_000, 3, 0, []int64{12_500, 12_500}},
		{30_000, 15_000, 3, 0, []int64{15_000, 15_000}},
		{40_000, 15_000, 2, 0, []int64{15_000, 15_000}},
		{25_000, 15_000, 3, 15, []int64{15_000}},
		{25_000, 0, 0, 0, []int64{25_000}},
	}
	for _, item := range cases {
		if got := videoReplicationClipParts(item.length, item.perClip, item.maxClips, item.total); !reflect.DeepEqual(got, item.want) {
			t.Fatalf("clipParts(%d,%d,%d,%d) = %v, want %v", item.length, item.perClip, item.maxClips, item.total, got, item.want)
		}
	}
	if prefix := videoReplicationMultiClipPrefix([]int64{12_500, 12_500}); prefix == "" {
		t.Fatal("multi clip prefix missing")
	}
	if prefix := videoReplicationMultiClipPrefix([]int64{12_500}); prefix != "" {
		t.Fatalf("single clip must keep @视频1 wording: %q", prefix)
	}
}

func TestVideoReplicationRouteTaskSeconds(t *testing.T) {
	if got := videoReplicationRouteTaskSeconds(30_800, 30, nil); !reflect.DeepEqual(got, []int{30}) {
		t.Fatalf("under 1s overflow folds into one task: %v", got)
	}
	if got := videoReplicationRouteTaskSeconds(45_000, 30, nil); !reflect.DeepEqual(got, []int{23, 22}) {
		t.Fatalf("45s over 30s tasks should split evenly: %v", got)
	}
	if got := videoReplicationRouteTaskSeconds(33_000, 15, []int{5, 10, 15}); len(got) != 3 {
		t.Fatalf("33s over 15s tasks should be 3 tasks: %v", got)
	}
}
