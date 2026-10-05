// 任务面板：首页任务卡点开后直接可用的参数面板，用户全程不接触提示词。
//
// 复刻的是成熟电商 AI 平台工具箱的表单契约（素材槽位 / 模块勾选 / 下拉 / 一键生成），
// 但提交不新开接口：内部组装提示词后复用 create 页已有的 submit() 链路。

import { useMemo } from "react";
import { Button } from "antd";
import { LoaderCircle, Minus, Plus, X } from "lucide-react";

import { Tooltip } from "@/components/ui/base/tooltip";
import { ModelPicker } from "@/components/model-picker";
import { CreditSymbol, requestCreditCost } from "@/constant/credits";
import { modelOptionName, resolveModelChannel, type AiConfig } from "@/stores/use-config-store";
import { useUserStore } from "@/stores/use-user-store";
import type { ModelRequirements } from "@/lib/model-selection";
import type { CreationMode } from "./creation-assets";
import { modeLabels } from "./creation-types";
import { defaultModuleSelection, selectedModuleSummary, type CreationTask, type CreationTaskModuleSelection } from "./creation-task-catalog";

export type CreationTaskPanelProps = {
    task: CreationTask;
    busy: boolean;
    /** 每个槽位当前占用的素材数量，键为 task.slots[].key。 */
    slotCounts: Record<string, number>;
    maxImages: number;
    maxVideos: number;
    maxAudios: number;
    moduleSelection: CreationTaskModuleSelection;
    onModuleChange: (moduleKey: string, count: number) => void;
    choiceValues: Record<string, string>;
    onChoiceChange: (key: string, value: string) => void;
    notes: string;
    onNotesChange: (value: string) => void;
    onPickSlot: (slotKey: string) => void;
    ratio: string;
    ratioOptions: Array<{ value: string; label: string }>;
    onRatioChange: (value: string) => void;
    seconds: string;
    secondsOptions: Array<{ value: string; label: string }>;
    onSecondsChange: (value: string) => void;
    model: string;
    onModelChange: (value: string) => void;
    config: AiConfig;
    modelRequirements: ModelRequirements;
    onSubmit: () => void;
    onClose: () => void;
};

function slotAccept(task: CreationTask, slotKey: string) {
    return task.slots.find((slot) => slot.key === slotKey)?.media || [];
}

export function CreationTaskPanel(props: CreationTaskPanelProps) {
    const { task } = props;
    const creditsEnabled = useUserStore((state) => state.features.creditsEnabled);
    const summary = useMemo(() => selectedModuleSummary(task, props.moduleSelection), [props.moduleSelection, task]);

    // 必填槽位没素材就禁用生成：写路径必须强校验，不能让用户提交一个必然编造主体的任务。
    const missingRequiredSlot = task.slots.find((slot) => slot.required && !props.slotCounts[slot.key]);
    const mediaLimitReached = task.slots.some((slot) => {
        const count = props.slotCounts[slot.key] || 0;
        const accept = slotAccept(task, slot.key);
        if (count >= 6) return true;
        if (accept.includes("image") && count >= props.maxImages && props.maxImages > 0) return true;
        if (accept.includes("video") && count >= props.maxVideos && props.maxVideos > 0) return true;
        if (accept.includes("audio") && count >= props.maxAudios && props.maxAudios > 0) return true;
        return false;
    });
    const canSubmit = !props.busy && !missingRequiredSlot && !mediaLimitReached;

    const outputCount = task.mode === "image" ? Math.max(1, Math.min(summary.images, 4)) : 1;
    const priceChannel = resolveModelChannel(props.config, props.model);
    const credits = requestCreditCost({
        channelMode: priceChannel.scope === "system" ? "remote" : "local",
        modelCosts: priceChannel.modelCosts,
        model: modelOptionName(props.model),
        count: outputCount,
        seconds: task.mode === "video" ? Number(props.seconds) : 1,
        capability: task.mode,
        config: props.config,
        requirements: props.modelRequirements,
    });
    const showCost = creditsEnabled && credits !== null && credits !== undefined;

    const disabledReason = props.busy
        ? "正在生成"
        : missingRequiredSlot
            ? `请先添加${missingRequiredSlot.name.replace(/^添加/, "")}`
            : mediaLimitReached
                ? "参考素材已达到当前模型上限"
                : "";

    return (
        <section className="creation-task-panel" aria-label={`${task.name} 参数面板`}>
            <header className="creation-task-panel-head">
                <div>
                    <h3>{task.name}</h3>
                    <p>{task.detail}</p>
                </div>
                <Tooltip title="收起面板">
                    <button type="button" className="creation-task-panel-close" onClick={props.onClose} aria-label="收起任务面板"><X aria-hidden="true" /></button>
                </Tooltip>
            </header>

            <div className="creation-task-panel-body">
                <section className="creation-task-section" aria-labelledby="creation-task-slots-title">
                    <h4 id="creation-task-slots-title">添加素材</h4>
                    <div className="creation-task-slots">
                        {task.slots.map((slot) => {
                            const count = props.slotCounts[slot.key] || 0;
                            return (
                                <button
                                    key={slot.key}
                                    type="button"
                                    className={`creation-task-slot${count ? " has-items" : ""}`}
                                    onClick={() => props.onPickSlot(slot.key)}
                                    disabled={props.busy}
                                    aria-label={`${slot.name}，${slot.required ? "必填" : "可选"}，已添加 ${count} 个`}
                                >
                                    <span className="creation-task-slot-mark" aria-hidden>{count ? <Plus /> : <span>+</span>}</span>
                                    <span className="creation-task-slot-copy">
                                        <strong>{slot.name}{slot.required ? <em aria-hidden> *</em> : null}</strong>
                                        <span>{slot.hint}</span>
                                    </span>
                                    {count ? <span className="creation-task-slot-count">{count}</span> : null}
                                </button>
                            );
                        })}
                    </div>
                </section>

                {task.modules?.length ? (
                    <section className="creation-task-section" aria-labelledby="creation-task-modules-title">
                        <div className="creation-task-section-head">
                            <h4 id="creation-task-modules-title">选择模块</h4>
                            <p aria-live="polite">已选 {summary.kinds} 类，共 {summary.images} 张</p>
                        </div>
                        <ul className="creation-task-modules">
                            {task.modules.map((module) => {
                                const checked = (props.moduleSelection[module.key] || 0) > 0;
                                return (
                                    <li key={module.key}>
                                        <label className="creation-task-module">
                                            <input
                                                type="checkbox"
                                                checked={checked}
                                                disabled={props.busy}
                                                onChange={() => props.onModuleChange(module.key, checked ? 0 : module.count)}
                                            />
                                            <span className="creation-task-module-copy">
                                                <strong>{module.name}</strong>
                                                <Tooltip title={module.spec}>
                                                    <span className="creation-task-module-spec">{module.spec}</span>
                                                </Tooltip>
                                            </span>
                                            {checked ? (
                                                <span className="creation-task-module-count">
                                                    <button type="button" onClick={() => props.onModuleChange(module.key, Math.max(0, (props.moduleSelection[module.key] || 0) - 1))} disabled={props.busy || (props.moduleSelection[module.key] || 0) <= 1} aria-label={`减少${module.name}数量`}><Minus aria-hidden="true" /></button>
                                                    <input
                                                        type="number"
                                                        min={1}
                                                        max={4}
                                                        value={props.moduleSelection[module.key] || 1}
                                                        disabled={props.busy}
                                                        onChange={(event) => props.onModuleChange(module.key, Math.max(1, Math.min(4, Number(event.target.value) || 1)))}
                                                        aria-label={`${module.name}出图数量`}
                                                    />
                                                </span>
                                            ) : null}
                                        </label>
                                    </li>
                                );
                            })}
                        </ul>
                    </section>
                ) : null}

                <section className="creation-task-section" aria-labelledby="creation-task-settings-title">
                    <h4 id="creation-task-settings-title">生成设置</h4>
                    <div className="creation-task-fields">
                        {task.choices?.map((choice) => (
                            <label key={choice.key} className="creation-task-field">
                                <span>{choice.label}</span>
                                <select
                                    value={props.choiceValues[choice.key] || choice.options[0]?.value}
                                    disabled={props.busy}
                                    onChange={(event) => props.onChoiceChange(choice.key, event.target.value)}
                                >
                                    {choice.options.map((option) => <option key={option.value} value={option.value}>{option.label}</option>)}
                                </select>
                            </label>
                        ))}
                        {task.mode === "image" ? (
                            <label className="creation-task-field">
                                <span>图片比例</span>
                                <select value={props.ratio} disabled={props.busy} onChange={(event) => props.onRatioChange(event.target.value)}>
                                    {props.ratioOptions.map((option) => <option key={option.value} value={option.value}>{option.label}</option>)}
                                </select>
                            </label>
                        ) : (
                            <label className="creation-task-field">
                                <span>视频时长</span>
                                <select value={props.seconds} disabled={props.busy} onChange={(event) => props.onSecondsChange(event.target.value)}>
                                    {props.secondsOptions.map((option) => <option key={option.value} value={option.value}>{option.label}</option>)}
                                </select>
                            </label>
                        )}
                        {task.notesHint ? (
                            <label className="creation-task-field is-notes">
                                <span>补充说明（可选）</span>
                                <textarea
                                    rows={3}
                                    value={props.notes}
                                    disabled={props.busy}
                                    placeholder={task.notesHint}
                                    onChange={(event) => props.onNotesChange(event.target.value)}
                                />
                            </label>
                        ) : null}
                    </div>
                    <div className="creation-task-model">
                        <span>模型</span>
                        <ModelPicker
                            config={props.config}
                            value={props.model}
                            onChange={props.onModelChange}
                            capability={task.mode as CreationMode}
                            requirements={props.modelRequirements}
                            className="creation-model-picker"
                            placeholder={`选择${modeLabels[task.mode]}模型`}
                            showSelectedPrice={false}
                            showOptionPrices
                            variant="creation"
                        />
                    </div>
                </section>
            </div>

            <footer className="creation-task-panel-foot">
                <span className="creation-task-panel-status" aria-live="polite">{disabledReason || (task.modules?.length ? `将生成 ${summary.images} 张图` : "")}</span>
                <Button
                    type="text"
                    className="creation-task-submit"
                    disabled={!canSubmit}
                    onClick={props.onSubmit}
                    title={disabledReason || undefined}
                    aria-label={disabledReason || `立即生成${task.name}`}
                >
                    {showCost ? <span className="creation-task-submit-cost"><CreditSymbol />{credits?.toLocaleString("zh-CN", { maximumFractionDigits: 6 })}</span> : null}
                    <span className="creation-task-submit-action" aria-hidden>
                        {props.busy ? <LoaderCircle className="size-4 animate-spin" /> : null}
                        {props.busy ? "生成中" : `立即生成${task.name}`}
                    </span>
                </Button>
            </footer>
        </section>
    );
}