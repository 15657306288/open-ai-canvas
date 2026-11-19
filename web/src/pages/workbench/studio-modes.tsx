import { Button } from "antd";
import { ArrowUpRight, type LucideIcon } from "lucide-react";

import type { WorkbenchTool } from "@/services/api/workbench";

import { ComposerSection } from "./workbench-composer";
import "./studio-modes.css";

export type StudioMode = {
    id: string;
    label: string;
    description: string;
    steps: string[];
    toolName?: string;
    action?: "library";
    icon: LucideIcon;
};

export function findStudioTool(mode: StudioMode, tools: WorkbenchTool[]) {
    if (!mode.toolName) return null;
    return tools.find((tool) => tool.name === mode.toolName) || null;
}

export function StudioModeSection({
    modes,
    tools,
    selectedMode,
    onModeChange,
    onOpenLibrary,
    title = "创作方式",
    hint = "选择一条创作流程",
    libraryLabel = "全部场景",
}: {
    modes: StudioMode[];
    tools: WorkbenchTool[];
    selectedMode: string;
    onModeChange: (mode: StudioMode) => void;
    onOpenLibrary: () => void;
    title?: string;
    hint?: string;
    libraryLabel?: string;
}) {
    const mode = modes.find((item) => item.id === selectedMode) || modes[0];
    const tool = findStudioTool(mode, tools);
    const isLibraryAction = mode.action === "library";
    return (
        <ComposerSection
            title={title}
            hint={hint}
            aside={
                <Button size="small" type="text" className="wb-text-action" onClick={onOpenLibrary}>
                    {libraryLabel}
                    <ArrowUpRight className="size-3.5" />
                </Button>
            }
        >
            <div className="wbv-mode-grid" role="group" aria-label="创作方式">
                {modes.map((item) => {
                    const Icon = item.icon;
                    return (
                        <button key={item.id} type="button" className="wbv-mode-card" aria-pressed={item.id === mode.id} onClick={() => onModeChange(item)}>
                            <span className="wbv-mode-icon" aria-hidden>
                                <Icon />
                            </span>
                            <span>{item.label}</span>
                        </button>
                    );
                })}
            </div>
            <div className="wbv-mode-detail">
                <div className="wbv-mode-copy">
                    <strong>{mode.label}</strong>
                    <p>{mode.description}</p>
                </div>
                <ol className="wbv-mode-steps">
                    {mode.steps.map((step, index) => (
                        <li key={step}>
                            <span>{index + 1}</span>
                            {step}
                        </li>
                    ))}
                </ol>
                <div className="wbv-mode-action">
                    <Button onClick={() => (isLibraryAction ? onOpenLibrary() : onModeChange(mode))}>{isLibraryAction ? "打开场景库" : `使用${mode.label}`}</Button>
                    <small>{isLibraryAction ? "选中模板后进入对应流程" : mode.toolName ? (tool ? `当前模板：${tool.name}` : "模板加载后可用") : "选择模板后继续"}</small>
                </div>
            </div>
        </ComposerSection>
    );
}

export function StudioOverview({
    modes,
    selectedMode,
    onModeChange,
    onOpenLibrary,
    icon: Icon,
    title,
    description,
    footNote,
    ariaLabel,
    cardAriaLabel = "选择创作方式",
}: {
    modes: StudioMode[];
    selectedMode: string;
    onModeChange: (mode: StudioMode) => void;
    onOpenLibrary: () => void;
    icon: LucideIcon;
    title: string;
    description: string;
    footNote: string;
    ariaLabel: string;
    cardAriaLabel?: string;
}) {
    return (
        <section className="wbv-overview" aria-label={ariaLabel}>
            <header className="wbv-overview-head">
                <span className="wbv-overview-icon" aria-hidden>
                    <Icon />
                </span>
                <div>
                    <h2>{title}</h2>
                    <p>{description}</p>
                </div>
            </header>
            <div className="wbv-overview-grid" role="group" aria-label={cardAriaLabel}>
                {modes.map((mode) => {
                    const ModeIcon = mode.icon;
                    return (
                        <button key={mode.id} type="button" className="wbv-overview-card" aria-pressed={mode.id === selectedMode} onClick={() => onModeChange(mode)}>
                            <span className="wbv-overview-card-icon" aria-hidden>
                                <ModeIcon />
                            </span>
                            <span className="wbv-overview-card-copy">
                                <strong>{mode.label}</strong>
                                <small>{mode.description}</small>
                            </span>
                            <ArrowUpRight aria-hidden />
                        </button>
                    );
                })}
            </div>
            <footer className="wbv-overview-foot">
                <span>{footNote}</span>
                <Button size="small" onClick={onOpenLibrary}>
                    打开场景库
                </Button>
            </footer>
        </section>
    );
}
