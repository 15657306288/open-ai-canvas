package model

import (
	"reflect"
	"testing"
)

func TestParseChannelModelNames(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want []string
	}{
		{
			name: "当前后台保存的字符串数组",
			raw:  `["gpt-image-2.5","nano-banana-pro"]`,
			want: []string{"gpt-image-2.5", "nano-banana-pro"},
		},
		{
			name: "旧导入工具的对象数组",
			raw:  `[{"model":"gpt-image-2.5","name":"","caps":["image"],"protocol":""},{"model":"nano-banana2","caps":["image"]}]`,
			want: []string{"gpt-image-2.5", "nano-banana2"},
		},
		{
			name: "空数组",
			raw:  `[]`,
			want: []string{},
		},
		{
			name: "空白字符串",
			raw:  "   ",
			want: nil,
		},
		{
			name: "非法 JSON",
			raw:  `{`,
			want: nil,
		},
		{
			name: "对象数组里的空名被丢弃",
			raw:  `[{"model":"keep"},{"model":"  "},{"name":"no-model-field"}]`,
			want: []string{"keep"},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := ParseChannelModelNames(testCase.raw)
			if !reflect.DeepEqual(got, testCase.want) {
				t.Fatalf("ParseChannelModelNames(%q) = %#v, want %#v", testCase.raw, got, testCase.want)
			}
		})
	}
}
