import { Armchair, Brush, Copy, FileImage, Image as ImageIcon, Layers, Package } from "lucide-react";

import type { WorkbenchTool } from "@/services/api/workbench";

import { findStudioTool, StudioModeSection, StudioOverview, type StudioMode } from "./studio-modes";

export type ImageStudioModeId = "main" | "sku" | "detail" | "edit" | "suite" | "fission" | "interior" | "interiorTools";

export type ImageStudioMode = StudioMode & { id: ImageStudioModeId };

export const IMAGE_STUDIO_MODES: ImageStudioMode[] = [
    {
        id: "main",
        label: "主图生成",
        description: "商品图加主图要求，直接生成平台商品主图；换品和爆款复刻用参考图。",
        steps: ["上传商品图", "写主图要求", "选择尺寸生成"],
        toolName: "AI 主图生成",
        icon: ImageIcon,
    },
    {
        id: "sku",
        label: "SKU 母版",
        description: "先生成一张可复用的视觉母版，规格文字留给后续替换。",
        steps: ["上传商品图", "写母版要求", "生成母版"],
        toolName: "AI SKU 母版",
        icon: Layers,
    },
    {
        id: "detail",
        label: "详情页",
        description: "按详情页尺寸和文案语言生成首屏与核心卖点画面。",
        steps: ["上传商品图", "写卖点与版式", "生成详情画面"],
        toolName: "AI 详情页",
        icon: FileImage,
    },
    {
        id: "edit",
        label: "图片修改",
        description: "在原图上涂抹修改区域，再填写修改要求。",
        steps: ["上传原图", "写修改说明", "生成修改图"],
        toolName: "AI 图片修改",
        icon: Brush,
    },
    {
        id: "suite",
        label: "整套商品",
        description: "围绕同一商品生成成套画面，统一商品、背景、光线和版式。",
        steps: ["上传商品图", "写套图方向", "分批生成"],
        toolName: "整套商品出图",
        icon: Package,
    },
    {
        id: "fission",
        label: "商品裂变",
        description: "用一张母版扩展成多张变体，保持商品身份和卖点不变。",
        steps: ["上传母版", "写变体方向", "生成多张变体"],
        toolName: "整套商品裂变",
        icon: Copy,
    },
    {
        id: "interior",
        label: "装修效果图",
        description: "上传空间照片，指定房型和风格，生成装修效果图。",
        steps: ["上传空间照片", "写房型与风格", "生成效果图"],
        toolName: "装修效果图",
        icon: Armchair,
    },
    {
        id: "interiorTools",
        label: "装修工具",
        description: "设计 COPY、替换家具、换材质、旧房美化共用一条编辑流程。",
        steps: ["上传空间照片", "说明工具目标", "生成修改图"],
        toolName: "装修工具",
        icon: Brush,
    },
];

export function findImageStudioTool(mode: ImageStudioMode, tools: WorkbenchTool[]) {
    return findStudioTool(mode, tools);
}

export function ImageStudioModeSection({ tools, selectedMode, onModeChange, onOpenLibrary }: { tools: WorkbenchTool[]; selectedMode: ImageStudioModeId; onModeChange: (mode: ImageStudioMode) => void; onOpenLibrary: () => void }) {
    return <StudioModeSection modes={IMAGE_STUDIO_MODES} tools={tools} selectedMode={selectedMode} onModeChange={(mode) => onModeChange(mode as ImageStudioMode)} onOpenLibrary={onOpenLibrary} hint="选择一条生图流程" libraryLabel="全部场景" />;
}

export function ImageStudioOverview({ selectedMode, onModeChange, onOpenLibrary }: { selectedMode: ImageStudioModeId; onModeChange: (mode: ImageStudioMode) => void; onOpenLibrary: () => void }) {
    return (
        <StudioOverview
            modes={IMAGE_STUDIO_MODES}
            selectedMode={selectedMode}
            onModeChange={(mode) => onModeChange(mode as ImageStudioMode)}
            onOpenLibrary={onOpenLibrary}
            icon={ImageIcon}
            title="AI 生图"
            description="从主图、SKU、详情、修改、整套商品或装修进入，选一条生图流程。"
            footNote="作品会保存在这里，生成前先在左侧补充商品图和描述。"
            ariaLabel="AI 生图创作方式"
            cardAriaLabel="选择生图创作方式"
        />
    );
}
