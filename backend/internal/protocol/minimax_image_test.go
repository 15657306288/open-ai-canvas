package protocol

import (
	"context"
	"testing"
)

// MiniMax 原生图片接口把结果放在 data.image_urls，与多数网关直接返回 data 数组不同。
// 这条回归用例锁定原生响应形态，避免同步图片结果再次被解析成空结果。
func TestMiniMaxImageParsesNativeImageURLs(t *testing.T) {
	adapter := officialPackageAdapter(t, "minimax-image.yingce-plugin", "minimax-image")
	payload := []byte(`{"id":"0702dfc1620e392110f59997b3a87573","data":{"image_urls":["https://cdn.example/a.jpg"]},"metadata":{"failed_count":"0","success_count":"1"},"base_resp":{"status_code":0,"status_msg":"success"}}`)
	result, err := adapter.ParseCreate(context.Background(), payload)
	if err != nil {
		t.Fatal(err)
	}
	if result.Result == nil || len(result.Result.Images) != 1 || result.Result.Images[0].URL != "https://cdn.example/a.jpg" {
		t.Fatalf("images = %#v", result.Result)
	}
}

func TestMiniMaxImageKeepsGatewayArrayResponse(t *testing.T) {
	adapter := officialPackageAdapter(t, "minimax-image.yingce-plugin", "minimax-image")
	result, err := adapter.ParseCreate(context.Background(), []byte(`{"data":[{"url":"https://cdn.example/gateway.png"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if result.Result == nil || len(result.Result.Images) != 1 || result.Result.Images[0].URL != "https://cdn.example/gateway.png" {
		t.Fatalf("images = %#v", result.Result)
	}
}
