import { AtSign, Download, FolderTree, LayoutTemplate, WandSparkles } from "lucide-react";

import { registerToolbarTools, type ToolDefinition } from "@/lib/canvas/tool-registry";

export const selectionToolbarTools: ToolDefinition[] = [
    { id: "selection-download", toolbar: "selection", category: "resource", label: "下载", icon: <Download />, defaultVisible: true, defaultOrder: 10, disabled: (ctx) => ctx.selectedCount < 1, run: (ctx) => ctx.handlers.onDownloadSelection?.() },
    { id: "selection-auto-arrange", toolbar: "selection", category: "layout", label: "自动对齐", icon: <WandSparkles />, defaultVisible: true, defaultOrder: 20, disabled: (ctx) => ctx.selectedCount < 2, run: (ctx) => ctx.handlers.onAutoArrange?.() },
    { id: "selection-create-group", toolbar: "selection", category: "selection", label: "创建分组", icon: <FolderTree />, defaultVisible: true, defaultOrder: 30, disabled: (ctx) => ctx.selectedCount < 2, run: (ctx) => ctx.handlers.onCreateReferenceGroup() },
    { id: "selection-send-to-agent", toolbar: "selection", category: "selection", label: "发送给 Agent", icon: <AtSign />, defaultVisible: true, defaultOrder: 40, disabled: (ctx) => ctx.selectedCount < 1, run: (ctx) => ctx.handlers.onSendSelectionToAgent() },
    { id: "selection-save-template", toolbar: "selection", category: "resource", label: "保存为模板", icon: <LayoutTemplate />, defaultVisible: true, defaultOrder: 50, disabled: (ctx) => ctx.selectedCount < 1, run: (ctx) => ctx.handlers.onSaveTemplate() },
];

registerToolbarTools(selectionToolbarTools);
