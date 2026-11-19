import { Clapperboard, Compass, Lightbulb, ListVideo, Replace, Sparkles } from "lucide-react";

import type { WorkbenchTool } from "@/services/api/workbench";

import { findStudioTool, StudioModeSection, StudioOverview, type StudioMode } from "./studio-modes";

export type VideoStudioModeId = "discover" | "quick" | "director" | "prompt" | "remix" | "replace";

export type VideoStudioMode = StudioMode & { id: VideoStudioModeId };

export const VIDEO_STUDIO_MODES: VideoStudioMode[] = [
    {
        id: "discover",
        label: "发现灵感",
        description: "从场景库找一条现成方向，再把要求改成自己的商品。",
        steps: ["浏览场景库", "选中模板", "补充商品与描述"],
        action: "library",
        icon: Compass,
    },
    {
        id: "quick",
        label: "智能快创",
        description: "用商品图和一句要求，快速落到可执行的商品镜头。",
        steps: ["添加商品图", "写一句要求", "选择镜头生成"],
        toolName: "商品广告英雄出场",
        icon: Sparkles,
    },
    {
        id: "director",
        label: "导演创作",
        description: "按主体、空间、动作和机位组织连续镜头，适合精细控制。",
        steps: ["放主体参考", "补场景或动作", "确认镜头方向"],
        toolName: "导演创作",
        icon: Clapperboard,
    },
    {
        id: "prompt",
        label: "提示词成片",
        description: "直接用文字描述成片，参考素材只补充主体、声音或运动。",
        steps: ["写完整描述", "按需放参考", "选择模型生成"],
        toolName: "提示词成片",
        icon: Lightbulb,
    },
    {
        id: "remix",
        label: "脚本框架复刻",
        description: "复用原片的节奏和结构，换成新的商品和表达内容。",
        steps: ["放原视频", "放新商品图", "确认复刻方案"],
        toolName: "脚本框架复刻",
        icon: ListVideo,
    },
    {
        id: "replace",
        label: "主体置换复刻",
        description: "保留原片镜头，只替换指定商品或主体，适合换品重做。",
        steps: ["放原视频", "放替换素材", "写清保留范围"],
        toolName: "主体置换复刻",
        icon: Replace,
    },
];

export function findVideoStudioTool(mode: VideoStudioMode, tools: WorkbenchTool[]) {
    return findStudioTool(mode, tools);
}

export function VideoStudioModeSection({ tools, selectedMode, onModeChange, onOpenLibrary }: { tools: WorkbenchTool[]; selectedMode: VideoStudioModeId; onModeChange: (mode: VideoStudioMode) => void; onOpenLibrary: () => void }) {
    return <StudioModeSection modes={VIDEO_STUDIO_MODES} tools={tools} selectedMode={selectedMode} onModeChange={(mode) => onModeChange(mode as VideoStudioMode)} onOpenLibrary={onOpenLibrary} hint="选择一条视频流程" libraryLabel="全部场景" />;
}

export function VideoStudioOverview({ selectedMode, onModeChange, onOpenLibrary }: { selectedMode: VideoStudioModeId; onModeChange: (mode: VideoStudioMode) => void; onOpenLibrary: () => void }) {
    return (
        <StudioOverview
            modes={VIDEO_STUDIO_MODES}
            selectedMode={selectedMode}
            onModeChange={(mode) => onModeChange(mode as VideoStudioMode)}
            onOpenLibrary={onOpenLibrary}
            icon={Clapperboard}
            title="AI 视频"
            description="从灵感、商品、脚本或原片进入，选一条创作流程。"
            footNote="作品会保存在这里，生成前先在左侧补充素材和描述。"
            ariaLabel="AI 视频创作方式"
            cardAriaLabel="选择视频创作方式"
        />
    );
}
