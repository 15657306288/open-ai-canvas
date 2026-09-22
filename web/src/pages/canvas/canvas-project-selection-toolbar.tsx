import type { RefObject } from "react";

import { CanvasSelectionToolbar } from "@/components/canvas/canvas-workspace-overlays";
import { FloatingDock } from "@/components/ui/aceternity/floating-dock";
import { canvasThemes } from "@/lib/canvas-theme";
import { canvasDockStyle } from "@/lib/canvas/canvas-aceternity-style";
import { defaultToolbarPrefs, readToolbarPrefs, resolveToolbarEntries, type ToolContext, type ToolbarHandlers } from "@/lib/canvas/tool-registry";
import { useCanvasThemeStore } from "@/stores/canvas/use-canvas-theme-store";

type CanvasProjectSelectionToolbarProps = {
    anchorRef: RefObject<HTMLDivElement | null>;
    containerRef: RefObject<HTMLDivElement | null>;
    count: number;
    onDownloadSelection: () => void;
    onAutoArrange: () => void;
    onCreateReferenceGroup: () => void;
    onSendSelectionToAgent: () => void;
    onSaveTemplate: () => void;
};

export function CanvasProjectSelectionToolbar({ anchorRef, containerRef, count, onDownloadSelection, onAutoArrange, onCreateReferenceGroup, onSendSelectionToAgent, onSaveTemplate }: CanvasProjectSelectionToolbarProps) {
    const theme = canvasThemes[useCanvasThemeStore((state) => state.theme)];

    const handlers = {
        onDownloadSelection, onAutoArrange, onCreateReferenceGroup, onSendSelectionToAgent, onSaveTemplate,
    } as Partial<ToolbarHandlers> as ToolbarHandlers;

    const ctx: ToolContext = {
        selectedCount: count,
        selectedNodeTypes: new Set(),
        selectedVideoCount: 0,
        canvasTool: "move",
        workspaceMode: "professional",
        isProjectLinked: false,
        canUndo: false,
        canRedo: false,
        extractingVideoFrames: false,
        extractingAudio: false,
        trimmingVideo: false,
        mergingVideos: false,
        addPanelOpen: false,
        appearancePanelOpen: false,
        settingsPanelOpen: false,
        handlers,
    };

    const prefs = readToolbarPrefs("selection") ?? defaultToolbarPrefs("selection");
    const items = resolveToolbarEntries("selection", ctx, prefs);

    return (
        <CanvasSelectionToolbar anchorRef={anchorRef} containerRef={containerRef} count={count}>
            <FloatingDock items={items} size="compact" className="canvas-floating-dock" style={canvasDockStyle(theme)} ariaLabel="多选节点布局工具" />
        </CanvasSelectionToolbar>
    );
}
