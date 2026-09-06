# qkmss 图片

## 鉴权与入口

- 鉴权：`Authorization: Bearer <apiKey>`。
- 默认 Base URL：`https://qkmss.com`。
- 生成：`POST /v1/images/generations`，`application/json`。
- 计费：按次计费（default 分组 ¥0.01/次，vip 分组 ¥0.007/次）；请求成功生成图片后扣费，失败不扣费。

## 配置字段

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `apiKey` | secret | 是 | qkmss 令牌页签发的 API Key，由宿主写入 Bearer 鉴权头。 |

## 请求字段

| 上游字段 | 类型 | 必填 | 统一来源 | 说明 |
| --- | --- | --- | --- | --- |
| `model` | string | 是 | `request.model` | 图片模型 ID（如 `gpt-image-2`）。 |
| `prompt` | string | 是 | `request.prompt` | 生成提示词。 |
| `n` | integer | 否 | `request.imageCount` | 输出数量；具体上限由模型 profile 决定。 |
| `size` | string | 否 | `request.aspectRatio` | OpenAI 尺寸枚举，不等同于任意比例。 |
| `quality` | string | 否 | `request.quality` | 质量枚举，以模型为准。 |

## 响应

- `data[].url` 转为统一图片 URL（宿主下载后持久化）。
- `data[].b64_json` 转为 `data:image/png;base64,...`。
- `usage` 原样进入统一结果 usage。
- `error.code`/`error.message` 进入失败状态与用户可读错误。

## 场景保证

- 该包只覆盖 qkmss 标准 OpenAI `images/generations` 协议；同一品牌的其他 endpoint、云区域或网关包装必须使用独立插件，不能根据模型名猜测。

<!-- YINGCE_MANIFEST_CONTRACT_START -->
## Manifest 完整接口定义

以下 JSON 与插件包内实际 `manifest.json` 逐字段一致，覆盖插件身份、权限、配置、鉴权、参数、校验、创建、Agent、查询、取消、结果下载、响应和 Agent 响应映射。`documentation` 字段的值就是当前完整文档；为避免文档在自身内部无限递归，JSON 中仅用等义占位文本表示正文。

```json
{
  "apiVersion": "yingce.plugin/v2",
  "id": "qkmss-image",
  "name": "qkmss 图片",
  "version": "1.0.0",
  "author": "影策 / yingce-dev",
  "description": "qkmss（qkmss.com）gpt-image-2 图片生成渠道插件；按次计费（default 分组 ¥0.01/次、vip 分组 ¥0.007/次），成功生成后扣费、失败不扣费。",
  "documentation": "<当前插件的完整 documentation，由 README.md 与 docs/interface.md 拼接而成；为避免 JSON 递归，此处不重复展开正文。>",
  "permissions": [
    "generation.run",
    "media.read"
  ],
  "configuration": {
    "fields": [
      {
        "name": "apiKey",
        "type": "secret",
        "label": "API Key",
        "required": true
      }
    ]
  },
  "contributes": {
    "providers": [
      {
        "id": "qkmss-image",
        "label": "qkmss 图片",
        "capabilities": [
          "image"
        ],
        "scopes": [
          "admin.system-channel",
          "user.custom-channel",
          "canvas",
          "creation",
          "agent"
        ],
        "baseUrl": "https://qkmss.com",
        "auth": {
          "type": "bearer",
          "field": "apiKey"
        },
        "parameters": [
          {
            "name": "model",
            "type": "string",
            "required": true,
            "mapping": "model",
            "description": "图片模型 ID（如 gpt-image-2）。"
          },
          {
            "name": "prompt",
            "type": "string",
            "required": true,
            "mapping": "prompt",
            "description": "生成提示词。"
          },
          {
            "name": "imageCount",
            "type": "integer",
            "mapping": "n",
            "description": "输出数量。"
          },
          {
            "name": "aspectRatio",
            "type": "string",
            "mapping": "size",
            "description": "OpenAI 尺寸枚举。"
          },
          {
            "name": "quality",
            "type": "string",
            "mapping": "quality",
            "description": "质量枚举。"
          }
        ],
        "create": {
          "method": "POST",
          "path": "/v1/images/generations",
          "contentType": "application/json",
          "body": {
            "model": {
              "$ref": "request.model"
            },
            "prompt": {
              "$ref": "request.prompt"
            },
            "n": {
              "$omitEmpty": {
                "$ref": "request.imageCount"
              }
            },
            "size": {
              "$omitEmpty": {
                "$ref": "request.aspectRatio"
              }
            },
            "quality": {
              "$omitEmpty": {
                "$ref": "request.quality"
              }
            }
          }
        },
        "response": {
          "status": "succeeded",
          "images": {
            "$map": {
              "from": {
                "$ref": "response.data"
              },
              "as": "item",
              "in": {
                "url": {
                  "$omitEmpty": {
                    "$ref": "item.url"
                  }
                },
                "dataUrl": {
                  "$if": {
                    "condition": {
                      "$ref": "item.b64_json"
                    },
                    "then": {
                      "$concat": [
                        "data:image/png;base64,",
                        {
                          "$ref": "item.b64_json"
                        }
                      ]
                    },
                    "else": null
                  }
                }
              }
            }
          },
          "usage": {
            "$ref": "response.usage"
          },
          "errorPaths": [
            "error.code"
          ],
          "messagePaths": [
            "error.message"
          ]
        }
      }
    ]
  }
}
```
<!-- YINGCE_MANIFEST_CONTRACT_END -->
