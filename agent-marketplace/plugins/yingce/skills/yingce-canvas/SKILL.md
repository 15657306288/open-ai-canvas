---
name: yingce-canvas
description: 通过标准 MCP 读写影策（Yingce）可视化画布，并调用多渠道文本/图片/视频模型。当需要理解画布现状、创建节点、搭建工作流、生成或轮询图片/视频/音频、查询可用渠道与模型时使用。
---

# 影策画布（Yingce Canvas）使用指南

你通过 MCP 连接到一块**人与你共享的可视化画布**。你的职责是基于**真实画布状态**产出可验证结果，而不是只输出 JSON 或凭空描述。

## 一、工具地图（50 个工具，5 组）

- **canvas_*（32）画布读写**
  - 读：`canvas_get_context`（首选，返回 stateHash/语义节点/连接/资源就绪状态）、`canvas_get_state`、`canvas_find_nodes`、`canvas_get_node`、`canvas_get_connection`、`canvas_get_selection`、`canvas_get_resources`、`canvas_get_generation_tasks`、`canvas_export_snapshot`、`canvas_get_media`
  - 写：`canvas_validate_ops`（复杂批量写前先校验）、`canvas_apply_ops`（批量事务）、`canvas_create_workflow`（**搭工作流首选**）、`canvas_create_node`/`canvas_create_text_node`/`canvas_create_text_nodes`、`canvas_create_image_prompt_flow`、`canvas_create_generation_flow`、`canvas_create_storyboard_shots`
  - 生成：`canvas_generate_text`/`canvas_generate_image`/`canvas_generate_video`/`canvas_generate_audio`、`canvas_run_generation`
  - 改：`canvas_update_node`/`canvas_update_node_text`/`canvas_move_nodes`/`canvas_resize_node`/`canvas_delete_nodes`/`canvas_connect_nodes`/`canvas_select_nodes`/`canvas_set_viewport`
- **project_*（10）项目/资产管理**：`project_get_context`、`project_list_units`、`project_extract_asset_candidates`、`project_confirm_asset_candidate`、`project_create_or_update_shots`、`project_link_shot_asset`、`project_start_workflow_step`、`project_link_asset`、`project_upsert_asset_version`、`project_register_task_output`
- **channel_*（5）多渠道模型**：`channel_list`（渠道）、`channel_list_models`（某渠道模型）、`channel_catalog_version`、`channel_generate`（发起生成）、`channel_get_task`（轮询结果）
- **model_*（2）逻辑模型**：`model_list_logical`、`model_get_capability`
- **dreamina_cli（1）**：底层即梦 CLI，**一般不要直接调用**，图片/视频/音频统一走 `canvas_generate_*` 共享 GenerationTask。

## 二、核心协议：先读后写、写完验证

1. **任何写操作前先读上下文**：涉及“这个/当前/已有/选中”对象时先 `canvas_get_context`；用户明确指选中对象再补 `canvas_get_selection`。
2. **不猜 id**：需要找节点用 `canvas_find_nodes`；已知真实 id 才用 `canvas_get_node`/`canvas_get_connection` 精确复核。
3. **识别不可用资源**：资源 `ready=false`、`status=loading/error` 或只有占位 metadata 时，必须明确说明，不能当成可用素材。
4. **复杂批量写**：先 `canvas_validate_ops` 再 `canvas_apply_ops`；只使用当前上下文里真实存在的 id；新增节点沿现有内容右侧/下方网格布局，避免重叠。
5. **写完检查真实返回**：没有变化、部分失败或仍在生成，要如实报告，不能声称“已完成”。
6. **高影响操作（删除/覆盖/批量移动/触发生成）**：先给简短计划，等待网页侧确认；不要用模拟点击绕过确认。

## 三、典型 Recipe

### A. 理解并概述画布
`canvas_get_context` →（需要细节时）`canvas_find_nodes` / `canvas_get_resources` → 用中文概述节点、连接、就绪资源与正在进行的生成任务。

### B. 搭建一条工作流（首选 canvas_create_workflow）
把业务阶段拆成真实的文本/脚本/图片/视频/音频节点：
- `character_cards` = 角色拆分图片卡片；`character_three_view` = 角色三视图；`storyboard_video` = 分镜剧情视频。
- 媒体节点必须有真实 prompt/content；引用已有素材时先 `canvas_find_nodes`/`canvas_get_resources`，把返回的真实 node id 填入 `referenceNodeIds`。
- 工具会自动布局并建立 edges/referenceRefs 连线；**禁止退化成一堆空文本节点**。

### C. 生成图片 / 视频 / 音频
1. 先查可用模型：`channel_list` → `channel_list_models`，或 `model_list_logical` + `model_get_capability`。
2. 复用画布上合适的参考节点（真实 node id），不要重复上传或造孤立副本。
3. 统一用 `canvas_generate_image` / `canvas_generate_video` / `canvas_generate_audio`（进入共享 GenerationTask；即梦用 `model=local:dreamina-cli:5.0` 这类产品模型值，分辨率 `quality=auto`）。
4. 用 `canvas_get_generation_tasks`（或 `channel_get_task`）轮询，直到成功/失败再汇报；长视频是异步任务，不要假设立即完成。

### D. 直接调用渠道模型（不落到画布时）
`channel_list` → `channel_list_models(channelId)` → `channel_generate` 拿 taskId → `channel_get_task(taskId)` 轮询。

## 四、边界

- 不要求用户手动复制 JSON、URL、token 或节点 id；不编造工具结果。
- 不在回复里粘贴媒体 URL、API Key 或 data URL。
- 页面文案与画布节点内容默认使用中文。
- 若工具返回“当前没有已连接画布”，说明本地 Runtime/画布未连接，应提示用户先在影策网页打开画布并启用 bridge，而不是反复重试。
