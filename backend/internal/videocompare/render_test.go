package videocompare

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func ffmpeg(t *testing.T, args ...string) []byte {
	t.Helper()
	data, err := exec.Command("ffmpeg", append([]string{"-v", "error", "-y", "-threads", "1"}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("ffmpeg: %v %s", err, data)
	}
	return data
}

func TestAudioSelectionAndBounds(t *testing.T) {
	o := Options{Reference: Media{DurationMs: 4000, HasAudio: true}, Generated: Media{Width: 1080, Height: 1920, DurationMs: 3000, HasAudio: true}}
	for _, tc := range []struct {
		generated, reference bool
		audio                string
	}{{true, true, "[1:a:0]"}, {false, true, "[0:a:0]"}, {false, false, "-an"}} {
		o.Generated.HasAudio, o.Reference.HasAudio = tc.generated, tc.reference
		args, err := Args("left", "right", "out", o)
		if err != nil || !strings.Contains(strings.Join(args, " "), tc.audio) {
			t.Fatalf("audio=%s: %v %v", tc.audio, args, err)
		}
	}
	o.StartMs = 4000
	if _, err := Args("left", "right", "out", o); err == nil {
		t.Fatal("invalid trim accepted")
	}
	o.StartMs = 0
	o.Generated.DurationMs = 121000
	if _, err := Args("left", "right", "out", o); err == nil {
		t.Fatal("unbounded video accepted")
	}
}

func TestRenderRealSideBySideWithTrimAndAudio(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg unavailable")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe unavailable")
	}
	dir := t.TempDir()
	left := filepath.Join(dir, "left.mp4")
	right := filepath.Join(dir, "right.mp4")
	out := filepath.Join(dir, "out.mp4")
	ffmpeg(t, "-f", "lavfi", "-i", "color=c=red:s=160x240:r=25:d=1", "-f", "lavfi", "-i", "color=c=blue:s=160x240:r=25:d=1", "-f", "lavfi", "-i", "sine=f=440:d=2",
		"-filter_complex", "[0:v][1:v]concat=n=2:v=1:a=0[v]", "-map", "[v]", "-map", "2:a", "-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", left)
	ffmpeg(t, "-f", "lavfi", "-i", "color=c=green:s=160x240:r=30:d=1.5", "-f", "lavfi", "-i", "sine=f=880:d=1.5", "-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", right)
	options := Options{Reference: Media{Width: 160, Height: 240, DurationMs: 2000, HasAudio: true}, Generated: Media{Width: 160, Height: 240, DurationMs: 1500, HasAudio: true}, StartMs: 1000, EndMs: 2000}
	if err := Render(context.Background(), "ffmpeg", left, right, out, options); err != nil {
		t.Fatal(err)
	}
	data, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "stream=codec_type,width,height:format=duration", "-of", "json", out).Output()
	if err != nil {
		t.Fatal(err)
	}
	var probe struct {
		Streams []struct {
			CodecType     string `json:"codec_type"`
			Width, Height int
		}
		Format struct{ Duration string }
	}
	if err = json.Unmarshal(data, &probe); err != nil {
		t.Fatal(err)
	}
	video, audio := false, false
	for _, stream := range probe.Streams {
		if stream.CodecType == "video" {
			video = stream.Width == 960 && stream.Height == 720
		}
		if stream.CodecType == "audio" {
			audio = true
		}
	}
	if !video || !audio {
		t.Fatalf("missing video or audio: %s", data)
	}
	// At 1.2s the reference has ended: its last blue frame must still be on the left.
	for _, tc := range []struct {
		x       int
		channel int
	}{{240, 2}, {720, 1}} {
		pixel := ffmpeg(t, "-ss", "1.2", "-i", out, "-vf", "crop=2:2:"+strconv.Itoa(tc.x)+":360,format=rgb24", "-frames:v", "1", "-f", "rawvideo", "pipe:1")
		if len(pixel) != 12 || pixel[tc.channel] < 100 || pixel[0] > 30 {
			t.Fatalf("wrong side/color at %d: %v", tc.x, pixel)
		}
	}
	// Decoding verifies an audible soundtrack and that generated 880Hz audio wins over 440Hz reference audio.
	pcm := ffmpeg(t, "-i", out, "-t", "0.5", "-vn", "-ac", "1", "-ar", "8000", "-f", "s16le", "pipe:1")
	crossings := 0
	previous := int16(0)
	for i := 0; i+1 < len(pcm); i += 2 {
		sample := int16(uint16(pcm[i]) | uint16(pcm[i+1])<<8)
		if sample > 0 && previous <= 0 {
			crossings++
		}
		previous = sample
	}
	if crossings < 420 || crossings > 460 {
		t.Fatalf("unexpected soundtrack: %d positive crossings", crossings)
	}
}
