# 红鸟高清图

## 鉴权与入口

- 鉴权：`Authorization: Bearer <apiKey>`。
- 默认 Base URL：`https://api.openai.com`。
- 生成：`POST /v1/images/generations`，`application/json`。
- 编辑：`POST /v1/images/edits`，`multipart/form-data`。

## 配置字段

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `apiKey` | secret | 是 | OpenAI API Key，由宿主写入 Bearer 鉴权头。 |
| `baseUrl` | string | 否 | 上游根地址，默认 `https://api.openai.com`。 |

## 请求字段

| 上游字段 | 类型 | 必填 | 统一来源 | 说明 |
| --- | --- | --- | --- | --- |
| `model` | string | 是 | `request.model` | 图片模型 ID。 |
| `prompt` | string | 是 | `request.prompt` | 生成/编辑提示词。 |
| `image` | file 或 file[] | 编辑必填 | `request.images[role!=mask]` | 编辑源图；重复 multipart part 保持输入顺序。 |
| `mask` | file | 否 | `request.images[role=mask]` | 蒙版；由业务层显式标记，插件不按下标猜测。 |
| `n` | integer | 否 | `request.imageCount` | 输出数量；具体上限由模型 profile 决定。 |
| `size` | string | 否 | `request.aspectRatio` | OpenAI 使用尺寸枚举，不等同于任意比例。 |
| `quality` | string | 否 | `request.quality` | 如 `auto/high/medium/low/hd/standard`，以模型为准。 |
| `background` | string | 否 | `providerOptions.hongniao-image-res.background` | 如 `transparent/opaque/auto`。 |
| `output_format` | string | 否 | `providerOptions.hongniao-image-res.output_format` | 如 `png/jpeg/webp`。 |
| `output_compression` | integer | 否 | `providerOptions.hongniao-image-res.output_compression` | 压缩质量百分比，支持范围由模型决定。 |
| `moderation` | string | 否 | `providerOptions.hongniao-image-res.moderation` | 内容审核强度。 |
| `response_format` | string | 否 | `providerOptions.hongniao-image-res.response_format` | 兼容模型可选 `url` 或 `b64_json`。 |
| `style` | string | 否 | `providerOptions.hongniao-image-res.style` | DALL·E 系列兼容字段。 |
| `user` | string | 否 | `providerOptions.hongniao-image-res.user` | 终端用户追踪标识。 |

## 响应

- `data[].url` 转为统一图片 URL。
- `data[].b64_json` 转为 `data:image/png;base64,...`。
- `usage` 原样进入统一结果 usage。
- `error.code`/`error.message` 进入失败状态与用户可读错误。

## 场景保证

- 单图编辑、多图编辑、蒙版编辑均使用显式 `role`，不使用 `0/1` 推断语义。
- multipart 文件由后端读取资源或下载 URL 后上传，上游不会收到浏览器 blob URL。
- 模型不支持的字段由模型 capability profile 拦截；插件不静默降级成整图生成。

<!-- YINGCE_MANIFEST_CONTRACT_START -->
## Manifest 完整接口定义

以下 JSON 与插件包内实际 `manifest.json` 逐字段一致，覆盖插件身份、权限、配置、鉴权、参数、校验、创建、Agent、查询、取消、结果下载、响应和 Agent 响应映射。`documentation` 字段的值就是当前完整文档；为避免文档在自身内部无限递归，JSON 中仅用等义占位文本表示正文。

```json
{
  "apiVersion": "yingce.plugin/v2",
  "id": "hongniao-image-res",
  "name": "OpenAI Images",
  "version": "2.0.0",
  "author": "OpenAI / 影策",
  "description": "OpenAI Images generations 与 multipart edits 协议插件。",
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
      },
      {
        "name": "baseUrl",
        "type": "string",
        "label": "Base URL",
        "default": "https://api.openai.com"
      }
    ]
  },
  "contributes": {
    "providers": [
      {
        "id": "hongniao-image-res",
        "label": "OpenAI Images",
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
        "baseUrl": "https://api.openai.com",
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
            "description": "图片模型 ID。"
          },
          {
            "name": "prompt",
            "type": "string",
            "required": true,
            "mapping": "prompt",
            "description": "生成或编辑提示词。"
          },
          {
            "name": "images",
            "type": "media[]",
            "mapping": "image multipart parts",
            "description": "role=edit_source 的源图；支持多图时按 order 重复 image part。"
          },
          {
            "name": "mask",
            "type": "media",
            "mapping": "mask multipart part",
            "description": "role=mask 的蒙版。"
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
            "description": "模型支持的尺寸枚举。"
          },
          {
            "name": "quality",
            "type": "string",
            "mapping": "quality",
            "description": "模型支持的质量枚举。"
          },
          {
            "name": "background",
            "type": "string",
            "mapping": "background",
            "description": "来自 providerOptions.hongniao-image-res.background。"
          },
          {
            "name": "output_format",
            "type": "string",
            "mapping": "output_format",
            "description": "来自 providerOptions.hongniao-image-res.output_format。"
          },
          {
            "name": "output_compression",
            "type": "integer",
            "mapping": "output_compression",
            "description": "来自 providerOptions.hongniao-image-res.output_compression。"
          },
          {
            "name": "moderation",
            "type": "string",
            "mapping": "moderation",
            "description": "来自 providerOptions.hongniao-image-res.moderation。"
          },
          {
            "name": "response_format",
            "type": "string",
            "mapping": "response_format",
            "description": "兼容模型返回 URL 或 b64_json。"
          },
          {
            "name": "style",
            "type": "string",
            "mapping": "style",
            "description": "DALL·E 系列兼容字段。"
          },
          {
            "name": "user",
            "type": "string",
            "mapping": "user",
            "description": "终端用户追踪标识。"
          }
        ],
        "create": {
          "method": "POST",
          "path": "/v1/images/generations",
          "pathTemplate": {
            "$if": {
              "condition": {
                "$gt": [
                  {
                    "$len": {
                      "$ref": "request.images"
                    }
                  },
                  0
                ]
              },
              "then": "/v1/images/edits",
              "else": "/v1/images/generations"
            }
          },
          "contentType": "application/json",
          "contentTypeTemplate": {
            "$if": {
              "condition": {
                "$gt": [
                  {
                    "$len": {
                      "$ref": "request.images"
                    }
                  },
                  0
                ]
              },
              "then": "multipart/form-data",
              "else": "application/json"
            }
          },
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
            },
            "background": {
              "$omitEmpty": {
                "$ref": "request.providerOptions.hongniao-image-res.background"
              }
            },
            "output_format": {
              "$omitEmpty": {
                "$ref": "request.providerOptions.hongniao-image-res.output_format"
              }
            },
            "output_compression": {
              "$omitEmpty": {
                "$ref": "request.providerOptions.hongniao-image-res.output_compression"
              }
            },
            "moderation": {
              "$omitEmpty": {
                "$ref": "request.providerOptions.hongniao-image-res.moderation"
              }
            },
            "response_format": {
              "$omitEmpty": {
                "$ref": "request.providerOptions.hongniao-image-res.response_format"
              }
            },
            "style": {
              "$omitEmpty": {
                "$ref": "request.providerOptions.hongniao-image-res.style"
              }
            },
            "user": {
              "$omitEmpty": {
                "$ref": "request.providerOptions.hongniao-image-res.user"
              }
            }
          },
          "files": [
            {
              "name": "image",
              "source": {
                "$filter": {
                  "from": {
                    "$ref": "request.images"
                  },
                  "as": "media",
                  "where": {
                    "$ne": [
                      {
                        "$ref": "media.role"
                      },
                      "mask"
                    ]
                  }
                }
              },
              "filename": "source.png"
            },
            {
              "name": "mask",
              "source": {
                "$filter": {
                  "from": {
                    "$ref": "request.images"
                  },
                  "as": "media",
                  "where": {
                    "$eq": [
                      {
                        "$ref": "media.role"
                      },
                      "mask"
                    ]
                  }
                }
              },
              "filename": "mask.png"
            }
          ]
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
