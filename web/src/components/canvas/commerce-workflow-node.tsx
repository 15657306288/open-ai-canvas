import { useMemo, useState } from "react";
import { Button, Input, InputNumber, Select } from "antd";
import { Download, ImagePlus, Layers3, LayoutGrid, Settings2, ShieldCheck, Sparkles } from "lucide-react";
import { CommercePreviewImage } from "./commerce-preview-image";
import { ModelPicker } from "@/components/model-picker";
import { ImageSettingsPanel } from "@/components/image-settings-panel";
import { AppModal } from "@/components/ui/product/app-modal";
import { COMMERCE_FONTS, COMMERCE_LANGUAGES, COMMERCE_PLATFORMS, commerceBusy, commerceInputs, commercePlatformDefaults } from "@/lib/canvas/commerce-workflow";
import { CommerceResultBoard } from "./commerce-result-board";
import { CommerceScreenEditor } from "./commerce-screen-editor";
import { commerceResultLayout } from "@/lib/canvas/commerce-result-layout";
import { isCanvasNodeGenerating } from "@/lib/canvas/canvas-node-task-state";
import { isCanvasComposerDragTarget } from "@/lib/canvas/canvas-composer-interaction";
import { batchRunningRowIds } from "@/lib/canvas/canvas-batch-table";
import { CommerceTemplateCopyEditor } from "./commerce-template-copy-editor";
import { defaultImageParamsForModel } from "@/lib/model-selection";
import type { CanvasTheme } from "@/lib/canvas-theme";
import { useEffectiveConfig } from "@/stores/use-config-store";
import { useUserStore } from "@/stores/use-user-store";
import { CreditSymbol, formatCredits } from "@/constant/credits";
import { commerceImagePriceLabel, commerceImageTotal, commercePricing } from "@/lib/canvas/commerce-pricing";
import { CanvasNodeType, type CanvasConnection, type CanvasGenerationBatch, type CanvasNodeData, type CanvasNodeMetadata } from "@/types/canvas";
import type { CommerceScreen, CommerceWorkflow } from "@/types/commerce-workflow";
import type { useCommerceWorkflow } from "@/pages/canvas/use-commerce-workflow";

type Props = {
    node: CanvasNodeData; nodes: CanvasNodeData[]; connections: CanvasConnection[]; theme: CanvasTheme; readOnly: boolean;
    workflow: ReturnType<typeof useCommerceWorkflow>;
    patchMetadata: (id: string, patch: Partial<CanvasNodeMetadata>) => void;
    disconnect: (sourceId: string) => void;
    focus: (id: string) => void;
    batch?: CanvasGenerationBatch;
    stopBatch: (batchId: string) => void;
    retryItem: (batchId: string, itemId: string) => void;
};
const choices = (values: string[]) => values.map((value) => ({ label: value, value }));

export function CommerceWorkflowNode({ node, nodes, connections, theme, readOnly, workflow, patchMetadata, disconnect, focus, batch, stopBatch, retryItem }: Props) {
    const effective = useEffectiveConfig();
    const creditsEnabled = useUserStore((state) => state.features.creditsEnabled);
    const pricing = useMemo(() => creditsEnabled ? commercePricing(node, nodes, connections, effective) : undefined, [creditsEnabled, node, nodes, connections, effective]);
    const data = node.metadata!.commerceWorkflow!;
    const replica = node.type === CanvasNodeType.ProductReplica;
    const resultGroup = data.role === "result";
    const inputs = commerceInputs(node, nodes, connections);
    const active = workflow.activeIds.has(node.id) || commerceBusy(node);
    const disabled = readOnly || Boolean(node.metadata?.locked) || active || Boolean(data.pending);
    const generationDisabled = readOnly || Boolean(node.metadata?.locked) || workflow.activeIds.has(node.id) || isCanvasNodeGenerating(node) || Boolean(data.pending);
    const runningRows = batchRunningRowIds(node, nodes);
    const activeBatches = (node.metadata?.generationBatches || (batch ? [batch] : [])).filter((item) => ["running", "queued"].includes(item.status));
    const [checklist, setChecklist] = useState(false);
    const [rawOpen, setRawOpen] = useState(false);
    const [settingsOpen, setSettingsOpen] = useState(false);
    const [editingScreenId, setEditingScreenId] = useState<string>();
    const editingIndex = data.screens.findIndex((screen) => screen.id === editingScreenId);
    const editingScreen = data.screens[editingIndex];
    const completed = data.screens.filter((screen) => nodes.some((item) => item.id === node.metadata?.batchTable?.rows.find((row) => row.id === screen.id)?.outputNodeId && item.metadata?.status === "success")).length;
    const layout = commerceResultLayout(node);
    const patch = (values: Partial<CommerceWorkflow>) => workflow.patch(node.id, values);
    const updateScreen = (id: string, values: Partial<CommerceScreen>) => patch({ screens: data.screens.map((screen) => screen.id === id ? { ...screen, ...values } : screen) });
    const model = node.metadata?.model || effective.imageModel;
    const imageConfig = { ...effective, model, imageModel: model, size: node.metadata?.size || effective.size, quality: node.metadata?.quality || effective.quality };
    const imageTotal = pricing ? commerceImageTotal(pricing.remaining) : null;
    const priceSummary = pricing && <div className="commerce-cost-summary" aria-label="生成费用预估">
        <div className="commerce-cost-total"><CreditSymbol /><strong>预计图片费用</strong><span>{commerceImagePriceLabel(pricing.remaining)}</span></div>
        {resultGroup ? <small>按未完成且未排队的图片计算；单屏重新生成按 1 张另计。</small>
            : replica ? <><small>产品 / 模板分析：{pricing.text.description}；图片费用另计。{imageTotal !== null && pricing.text.microcredits !== null ? `一键复刻预计合计 ${formatCredits(imageTotal + pricing.text.microcredits)} 积分。` : "图片合计不含分析费用。"}</small>{data.copyMode === "rewrite" && <small>可选文字识别：{pricing.ocr.description}，仅点击识别时另计。</small>}</>
            : <small>AI 策划：{pricing.text.description}；自定义策划免费建组。{imageTotal !== null && pricing.text.microcredits !== null ? `直接生成预计合计 ${formatCredits(imageTotal + pricing.text.microcredits)} 积分。` : "图片合计不含策划费用。"}</small>}
        <small>按当前模型标价计算，实际扣费以任务结算为准。</small>
    </div>;
    const move = (index: number, direction: number) => {
        const screens = [...data.screens]; const next = index + direction;
        if (next < 0 || next >= screens.length) return;
        [screens[index], screens[next]] = [screens[next], screens[index]]; patch({ screens });
    };
    const imageGroup = (secondary: boolean) => {
        const images = secondary ? inputs.references : inputs.products;
        const label = secondary ? replica ? "版式模板" : "风格参考图" : "产品图";
        const limit = replica ? secondary ? 12 : 7 : 20;
        return <section className="commerce-input-group">
            <div className="flex items-center justify-between gap-2"><strong>{label} <span className="font-normal opacity-60">{images.length} / {limit}</span></strong>
                {!resultGroup && <label className="commerce-upload" aria-disabled={disabled || images.length >= limit}>
                    <ImagePlus size={15} /> 上传
                    <input type="file" accept="image/*" className="sr-only" disabled={disabled || images.length >= limit} onChange={(event) => { const file = event.target.files?.[0]; if (file) void workflow.upload(node.id, file, secondary); event.target.value = ""; }} />
                </label>}
            </div>
            <div className="commerce-inputs">
                {images.map((image) => <div className="commerce-reference" key={image.id}>
                    <CommercePreviewImage node={image} alt={image.title} className="size-12 shrink-0 rounded" fit="cover" />
                    <span className="min-w-0 flex-1 truncate" title={image.title}>{image.title}</span>
                    {!resultGroup && <><button type="button" disabled={disabled} title={`移到${secondary ? "产品图" : replica ? "版式模板" : "风格参考图"}`} onClick={() => patch({ secondaryNodeIds: secondary ? data.secondaryNodeIds.filter((id) => id !== image.id) : [...data.secondaryNodeIds, image.id] })}>{secondary ? "设为产品" : replica ? "设为模板" : "设为参考"}</button>
                    <button type="button" disabled={disabled} aria-label={`断开 ${image.title}`} onClick={() => disconnect(image.id)}>×</button></>}
                </div>)}
                {!images.length && <p className="text-xs opacity-60">{secondary ? replica ? "接几张模板就生成几张。连入图片后点击“设为模板”。" : "可选：交给 AI 分析布局、配色与光影，不作为生图参考图片。" : "上传图片，或从图片节点连线到本节点。"}</p>}
            </div>
            {secondary && !replica && !!images.length && <p className="text-xs opacity-60">仅用于 AI 策划分析，不连入生图结果。自定义策划需手写视觉方案或点击 AI 策划。</p>}
        </section>;
    };
    const configuration = <div className="commerce-configuration"><div className="grid grid-cols-2 gap-3">{imageGroup(false)}{imageGroup(true)}</div>
            <fieldset disabled={disabled} className="contents">
                <label className="commerce-field">产品名称<Input aria-label="产品名称" value={data.productName} onChange={(event) => patch({ productName: event.target.value })} /></label>
                <label className="commerce-field">{replica ? "产品信息 / 复刻要求" : "产品卖点与附加要求"}<Input.TextArea aria-label="产品要求" autoSize={{ minRows: 2, maxRows: 4 }} value={data.brief} onChange={(event) => patch({ brief: event.target.value })} placeholder={replica ? "交给 AI 分析并落实到各模板替换方案；支持连入文本节点，文案替换请填写原文映射" : "交给 AI 理解并分配到各屏，不直接作为全局生图提示词；也支持连入文本节点"} /></label>
                {!!inputs.texts.length && <div className="text-xs opacity-65">已引用 {inputs.texts.length} 个文本节点：{inputs.texts.map((input) => input.title).join("、")}</div>}
                <div className="grid grid-cols-2 gap-3">
                    {replica ? <label className="commerce-field">文案处理<Select aria-label="文案处理" disabled={disabled} value={data.copyMode} options={[{ label: "保持原文案", value: "keep" }, { label: "识别并修改", value: "rewrite" }]} onChange={(copyMode) => patch({ copyMode })} /></label>
                        : <><label className="commerce-field">平台<Select aria-label="平台" disabled={disabled} value={data.platform} options={choices(COMMERCE_PLATFORMS)} onChange={(platform) => { const next = commercePlatformDefaults(platform); patch({ platform, ...(next.language ? { language: next.language } : {}) }); if (next.size && !disabled) patchMetadata(node.id, { size: next.size }); }} /></label>
                        <label className="commerce-field">屏数（1–30）<InputNumber aria-label="屏数" disabled={disabled || resultGroup} value={resultGroup ? data.screens.length : data.screenCount} min={1} max={30} precision={0} onChange={(value) => { if (value) patch({ screenCount: value }); }} /></label></>}
                    {replica && <label className="commerce-field">输出比例<Select disabled={disabled} value={data.followTemplate !== false ? "template" : "custom"} options={[{ label: "跟随各自模板", value: "template" }, { label: "使用下方统一尺寸", value: "custom" }]} onChange={(value) => patch({ followTemplate: value === "template" })} /></label>}
                    {!replica && <><label className="commerce-field">图中文字<Select disabled={disabled} value={data.textMode} options={[{ label: "按需排版", value: "typeset" }, { label: "纯图无字", value: "none" }]} onChange={(textMode) => patch({ textMode })} /></label>
                    <label className="commerce-field">语言<Select disabled={disabled} value={data.language} options={choices(COMMERCE_LANGUAGES)} onChange={(language) => patch({ language })} /></label>
                    <label className="commerce-field">模特<Select disabled={disabled} value={data.modelMode} options={choices(["自动判断", "不使用模特", "使用模特"])} onChange={(modelMode) => patch({ modelMode })} /></label>
                    <label className="commerce-field">字体风格<Select disabled={disabled} value={data.font} options={choices(COMMERCE_FONTS)} onChange={(font) => patch({ font })} /></label>
                    <label className="commerce-field">色彩节奏<Select disabled={disabled} value={data.colorRhythm} options={choices(["统一", "错落"])} onChange={(colorRhythm) => patch({ colorRhythm })} /></label></>}
                </div>
                <label className="commerce-field">{replica ? "分析 / 文字识别模型" : "策划模型"}<ModelPicker config={effective} value={data.textModel || effective.textModel} capability="text" fullWidth requirements={{ capability: "text", input: { imageCount: inputs.products.length + inputs.references.length, textCount: inputs.texts.length, videoCount: 0, audioCount: 0, characterCount: 0 } }} onChange={(textModel) => patch({ textModel })} /></label>
                <label className="commerce-field">图片生成模型<ModelPicker config={imageConfig} value={model} capability="image" fullWidth onChange={(value) => { if (!disabled) patchMetadata(node.id, { model: value, ...defaultImageParamsForModel(effective, value) }); }} /></label>
                <ImageSettingsPanel config={imageConfig} theme={theme} showTitle={false} showCount={false} showTransparent={false} className="w-full space-y-2" onConfigChange={(key, value) => { if (!disabled) patchMetadata(node.id, { [key]: value }); }} />
            </fieldset>
        {replica && data.copyMode === "rewrite" && <CommerceTemplateCopyEditor data={data} templates={inputs.references} disabled={disabled}
            onRecognize={(templateId) => void workflow.run(node.id, "ocr", templateId)}
            onChange={(templateId, copy) => patch({ templateCopies: { ...data.templateCopies, [templateId]: copy } })} />}
    </div>;
    return <div className={`commerce-workflow-node ${resultGroup ? "commerce-result-node" : ""}`} style={{ color: theme.node.text, background: theme.node.fill }} data-canvas-no-zoom data-canvas-wheel-scroll
        onPointerDown={(event) => { if (!isCanvasComposerDragTarget(event.target as Element, event.currentTarget)) event.stopPropagation(); }}
        onMouseDown={(event) => { if (!isCanvasComposerDragTarget(event.target as Element, event.currentTarget)) event.stopPropagation(); }} onWheel={(event) => event.stopPropagation()}>
        {resultGroup && <header className="commerce-result-head">
            <div className="commerce-result-heading"><Layers3 size={19} /><strong>{replica ? "复刻结果" : "详情页"} · {data.productName || "产品"}</strong><span>{active ? "运行中" : completed === data.screens.length && completed ? "已完成" : "待确认"} · {completed}/{data.screens.length}</span></div>
            <div className="commerce-result-toolbar">
                <Button type="primary" disabled={disabled || !data.screens.length || completed === data.screens.length} onClick={() => void workflow.run(node.id, "generate")}>{active ? "运行中…" : completed ? "生成未完成项" : "确认生成"}</Button>
                <Button icon={<Settings2 size={15} />} onClick={() => setSettingsOpen(true)}>编辑本组</Button>
                {!replica && <><Button icon={<ShieldCheck size={15} />} onClick={() => setChecklist(true)}>平台自检</Button>
                <Button icon={<Layers3 size={15} />} title="按当前卡片顺序引用原图片，创建等宽无间隙拼合组；不合成或上传新图片" disabled={disabled || !completed || completed !== data.screens.length} onClick={() => void workflow.merge(node.id, false)}>一键合并</Button>
                <Button icon={<Download size={15} />} title="下载时才读取原图并合成 PNG，不自动上传" disabled={disabled || !completed || completed !== data.screens.length} onClick={() => void workflow.merge(node.id, true)}>下载长图</Button></>}
            </div>
            {priceSummary}
        </header>}
        <div className="commerce-scroll">
            {!resultGroup && <><p className="text-xs opacity-60">{replica ? "多张产品作为共同参考，每张版式模板生成一张图" : "产品要求与风格偏好交给 AI 策划，再按单卡方案出图。自定义策划需自己填写各屏；直接生成会自动完成策划、出图两步。"}</p>{configuration}</>}

            {data.pending && <div className="flex flex-wrap gap-2"><Button disabled={workflow.activeIds.has(node.id) || readOnly || Boolean(node.metadata?.locked)} onClick={() => void workflow.run(node.id, "resume")}>恢复 / 核对当前请求</Button><Button disabled={workflow.activeIds.has(node.id) || readOnly || Boolean(node.metadata?.locked)} onClick={() => void workflow.run(node.id, "release")}>解锁已结束任务</Button></div>}
            {active && node.metadata?.commerceWorkflow?.pending && <Button disabled={readOnly} onClick={() => void workflow.run(node.id, "stop")}>停止策划</Button>}
            {node.metadata?.errorDetails && <p role="alert" className="text-sm text-[var(--destructive)]">{node.metadata.errorDetails}</p>}
            {!!data.rawOutput && <Button size="small" onClick={() => setRawOpen(!rawOpen)}>查看 / 修正原始响应</Button>}
            {rawOpen && <div className="space-y-2"><Input.TextArea aria-label="原始策划响应" rows={6} value={data.rawOutput} disabled={active || readOnly || !data.pending} onChange={(event) => workflow.patch(node.id, { rawOutput: event.target.value })} /><Button disabled={active || readOnly || !data.pending} onClick={() => void workflow.run(node.id, "apply")}>采用修正结果</Button></div>}
            {resultGroup && <CommerceResultBoard node={node} nodes={nodes} batch={batch} imagePrices={pricing?.images} disabled={disabled} generationDisabled={generationDisabled} planning={active && Boolean(data.pending)} onEdit={setEditingScreenId} onMove={move}
                onRemove={(id) => patch({ screens: data.screens.filter((screen) => screen.id !== id) })} onGenerate={(id) => void workflow.run(node.id, "generate", id)} onFocus={focus} onRetry={retryItem} />}
            <p className="text-xs opacity-60">{replica ? "先分析产品与模板，再按逐张替换方案出图；分析与图片分别计费。保持原文案不额外调用 OCR。每张模板独立生成，不做产品与模板的笛卡尔组合。" : "策划与图片任务分别计费；自定义策划免费建组，不会暗中调用模型补文案。"} {resultGroup && "中断后先核对已完成任务，再手动继续未完成部分。"}</p>
        </div>
        <div className="commerce-footer">
            {!resultGroup && priceSummary}
            {resultGroup ? <><LayoutGrid size={16} /><span>{layout.columns} 列 × {layout.rows} 行</span><Select data-canvas-no-drag aria-label="卡片组列数" size="small" value={layout.columns} disabled={readOnly || Boolean(node.metadata?.locked)} options={[1, 2, 3, 4, 5, 6].map((value) => ({ label: `${value} 列`, value }))} onChange={(columns) => workflow.layout(node.id, columns)} /><span className="commerce-footer-hint">点击卡片或铅笔编辑 · 拖动外框调整组大小</span></>
                : <><Button disabled={disabled} icon={<Sparkles size={14} />} onClick={() => void workflow.run(node.id, "plan")}>{replica ? "AI 分析模板" : "AI 一键策划"}</Button>{!replica && <Button disabled={disabled} onClick={() => void workflow.run(node.id, "manual")}>自定义策划</Button>}
                <Button type="primary" disabled={disabled} onClick={() => void workflow.run(node.id, "direct")}>{active ? "运行中…" : replica ? `一键复刻（${inputs.references.length} 张）` : "直接生成详情页"}</Button></>}
            {activeBatches.map((item, index) => <Button key={item.id} disabled={readOnly || Boolean(node.metadata?.locked)} onClick={() => stopBatch(item.id)}>{activeBatches.length === 1 ? "停止剩余任务" : `停止批次 ${index + 1} 排队项`}</Button>)}
        </div>
        {resultGroup && <AppModal title="本组设置 / 需求解读" open={settingsOpen} width={760} onCancel={() => setSettingsOpen(false)} footer={<Button onClick={() => setSettingsOpen(false)}>保存并返回卡片组</Button>}>
            <div className="commerce-settings-modal" data-canvas-no-zoom data-canvas-wheel-scroll>{configuration}</div>
        </AppModal>}
        {editingScreen && <CommerceScreenEditor screen={editingScreen} index={editingIndex} total={data.screens.length} nodes={nodes} disabled={disabled} generationDisabled={generationDisabled || runningRows.has(editingScreen.id)} replica={replica} noText={data.textMode === "none"}
            imagePrice={pricing?.images.find((image) => image.id === editingScreen.id)?.description} textPrice={creditsEnabled ? commercePricing(node, nodes, connections, effective, editingScreen.id).text.description : undefined}
            onClose={() => setEditingScreenId(undefined)} onNavigate={(direction) => setEditingScreenId(data.screens[editingIndex + direction]?.id)}
            onChange={(values) => updateScreen(editingScreen.id, values)} onUpload={(file) => void workflow.upload(node.id, file, true, editingScreen.id)}
            onReplan={() => void workflow.run(node.id, "replan", editingScreen.id)} onGenerate={() => void workflow.run(node.id, "generate", editingScreen.id)} />}
        <AppModal title={`${data.platform} · 发布前人工自检`} open={checklist} onCancel={() => setChecklist(false)} footer={null}>
            <p>这是人工检查清单，不调用模型，也不代表平台合规认证。请以平台当前要求为准。</p>
            <ul className="list-disc space-y-2 pl-5"><li>核对商品外观、包装、商标、规格与实际产品是否一致。</li><li>检查文案语言、拼写、文字排版和阅读顺序。</li><li>不得使用无依据的功效、对比数据、销量、认证或价格。</li><li>确认图片和字体授权，以及人物肖像授权。</li><li>{data.platform.includes("主图") ? "确认主图背景、文字、边框和附加物是否符合平台规则。" : "确认图片宽高、文件大小、模块数量与上传位置。"}</li></ul>
        </AppModal>
    </div>;
}
