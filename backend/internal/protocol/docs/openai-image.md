# OpenAI Images

OpenAI Images 插件实现 JSON 文生图及 multipart 图像编辑，并解析 OpenAI Images 风格的 `data[]` 响应。当前插件包根据参考图自动选择路径；不要仅根据静态 `create.path` 判断实际调用接口，还必须检查 `pathTemplate` 和 `contentTypeTemplate`。

## 接口与鉴权

{{OPERATIONS}}

```http
POST {channel_base_url}/v1/images/generations
Authorization: Bearer <API_KEY>
Content-Type: application/json
```

无参考图使用上述 JSON 接口；有参考图使用 `POST /v1/images/edits`、`multipart/form-data`，源图进入 `image` 文件字段，蒙版进入 `mask`。宿主拒绝缺少源图的蒙版编辑。

## 模型、尺寸与质量

模型、允许尺寸、质量枚举、单次张数和计费会随上游服务变化，插件不维护可能过期的白名单。渠道若是 OpenAI 兼容实现，必须分别验证它是否支持 `n`、`quality`、`background`、`output_format` 和 Base64 响应。

## 参数与字段映射

{{PARAMETERS}}

当前实现：`imageCount -> n`、`aspectRatio -> size`、`quality -> quality`。宿主先把画布比例预设转换为像素尺寸，例如 `1:1 -> 1024x1024`，不能把 `1:1` 原样发给图片接口。非法显式尺寸在提交前拒绝；ddcat 的宽高均不得超过 4096。Provider 扩展字段位于 `providerOptions.openai-image`，具体映射以安装的 manifest 为准。参考图通过 multipart 文件发送；`resolution` 不直接映射为输出像素尺寸。

## 文生图请求

```bash
curl "{channel_base_url}/v1/images/generations" \
  -H "Authorization: Bearer <API_KEY>" \
  -H "Content-Type: application/json" \
  -d '{
    "model":"YOUR_IMAGE_MODEL",
    "prompt":"雨夜便利店门口，青绿色霓虹，电影剧照",
    "size":"1536x1024",
    "quality":"high",
    "n":1
  }'
```

URL 响应：

```json
{"created":1780000000,"data":[{"url":"https://cdn.example/generated.png"}]}
```

Base64 响应：

```json
{"created":1780000000,"data":[{"b64_json":"iVBORw0KGgo..."}]}
```

解析器遍历 `data[]`，优先识别 `url`，也接受 `b64_json` 或 `data`。URL 应立即下载；裸 Base64 会转为 `data:image/png;base64,...`，已带 data URL 前缀的值保持不变。若渠道使用 WebP/JPEG Base64 且响应不返回 MIME，当前协议无法可靠推断格式，应优先请求 URL 或补充带 MIME 的响应合同。

## 图像编辑参考请求

```bash
curl "{channel_base_url}/v1/images/edits" \
  -H "Authorization: Bearer <API_KEY>" \
  -F "model=YOUR_IMAGE_MODEL" \
  -F "image=@reference.png" \
  -F "prompt=保持人物一致，改成雨夜街道"
```

该路径已经由插件包及宿主 multipart 传输实现。回归测试覆盖比例转换、编辑请求和 Base64 返回；`TestDDCatLiveImageRoundTrip` 仅在显式设置 `DDCAT_SMOKE_API_KEY` 时发起两次真实收费请求，默认测试会跳过。连接 EOF 表示结果未完整收到，不能据此确认上游没有生成或扣费；不得盲目自动重新提交。

## 官方资料

- [OpenAI Image generation guide](https://developers.openai.com/api/docs/guides/image-generation)
- [OpenAI Images API reference](https://developers.openai.com/api/reference/resources/images)

{{CONTRACT}}
