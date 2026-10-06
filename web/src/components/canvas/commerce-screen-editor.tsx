import { Button, Input, Select } from "antd";
import { ChevronLeft, ChevronRight, ImagePlus, Sparkles } from "lucide-react";
import { AppModal } from "@/components/ui/product/app-modal";
import { CommercePreviewImage } from "./commerce-preview-image";
import type { CanvasNodeData } from "@/types/canvas";
import type { CommerceScreen } from "@/types/commerce-workflow";

type Props = {
    imagePrice?: string; textPrice?: string;
    screen: CommerceScreen; index: number; total: number; nodes: CanvasNodeData[]; disabled: boolean; generationDisabled: boolean; replica: boolean; noText: boolean;
    onClose: () => void; onNavigate: (direction: number) => void; onChange: (patch: Partial<CommerceScreen>) => void;
    onUpload: (file: File) => void; onReplan: () => void; onGenerate: () => void;
};

export function CommerceScreenEditor({ screen, index, total, nodes, disabled, generationDisabled, replica, noText, imagePrice, textPrice, onClose, onNavigate, onChange, onUpload, onReplan, onGenerate }: Props) {
    return <AppModal title={`编辑第 ${index + 1} 屏 · ${screen.title}`} open width={760} onCancel={onClose} className="commerce-screen-modal" footer={
        <div className="commerce-editor-footer"><span>{imagePrice ? `本屏预计：${imagePrice}；修改参数不收费。` : "修改自动保存；生成前另行确认费用。"}</span><Button onClick={onClose}>仅保存参数</Button><Button type="primary" disabled={generationDisabled} onClick={onGenerate}>生成此屏</Button></div>
    }>
        <div className="commerce-editor-content" data-canvas-no-zoom data-canvas-wheel-scroll>
            <div className="commerce-editor-navigation"><Button icon={<ChevronLeft size={15} />} disabled={index === 0} onClick={() => onNavigate(-1)}>上一屏</Button><span>{index + 1} / {total}</span><Button icon={<ChevronRight size={15} />} disabled={index === total - 1} onClick={() => onNavigate(1)}>下一屏</Button></div>
            <label className="commerce-field">屏主题<Input aria-label={`第 ${index + 1} 屏主题`} disabled={disabled} value={screen.title} onChange={(event) => onChange({ title: event.target.value })} /></label>
            {!replica && <div className="commerce-editor-fields"><label className="commerce-field">场景类型<Input aria-label={`第 ${index + 1} 屏场景类型`} disabled={disabled} value={screen.sceneType || ""} placeholder="产品特写 / 使用场景 / 参数说明" onChange={(event) => onChange({ sceneType: event.target.value })} /></label>
                <label className="commerce-field">单屏比例<Select aria-label={`第 ${index + 1} 屏比例`} disabled={disabled} value={screen.size || ""} options={[{ label: "跟随本组", value: "" }, ...["21:9", "16:9", "3:2", "4:3", "1:1", "3:4", "2:3", "9:16"].map((value) => ({ label: value, value }))]} onChange={(size) => onChange({ size: size || undefined })} /></label></div>}
            {!replica && <><label className="commerce-field">卖点绑定<Input aria-label={`第 ${index + 1} 屏卖点`} disabled={disabled} value={screen.sellingPoints || ""} placeholder="仅填写已确认的产品事实" onChange={(event) => onChange({ sellingPoints: event.target.value })} /></label>
                <label className="commerce-field">画面文案（可选）<Input.TextArea aria-label={`第 ${index + 1} 屏文案`} disabled={disabled || noText} value={screen.copy} rows={3} placeholder="填写需上图的标题、正文与参数；留空按纯图生成" onChange={(event) => onChange({ copy: event.target.value })} /></label>
                <p className="commerce-editor-hint">{noText ? "本组为纯图无字，不添加排版文字。" : "文案留空表示本屏不添加排版文字，不代表画面方案缺失；不会额外调用模型补文案。"}</p></>}
            <label className="commerce-field">画面描述<Input.TextArea aria-label={`第 ${index + 1} 屏画面描述`} disabled={disabled} value={screen.prompt} rows={3} placeholder="具体机位、主体占比、场景道具、光影材质、配色与文案留白" onChange={(event) => onChange({ prompt: event.target.value })} /></label>
            {!replica && <div className="commerce-editor-reference-box"><label className="commerce-upload" aria-disabled={disabled}><ImagePlus size={16} /> 添加仅用于此屏的参考图<input className="sr-only" type="file" accept="image/*" disabled={disabled} onChange={(event) => { const file = event.target.files?.[0]; if (file) onUpload(file); event.target.value = ""; }} /></label>
                <p className="commerce-editor-hint">参考图仅交给 AI 分析风格，不直接参与生图。添加后点击「AI 重新策划此屏」，或手动将视觉要求写入画面描述。</p>
                {!!screen.referenceNodeIds?.length && <div className="commerce-screen-references">{screen.referenceNodeIds.map((id) => { const input = nodes.find((node) => node.id === id); return <div className="commerce-reference" key={id}>{input ? <CommercePreviewImage node={input} alt={input.title} className="size-12" fit="cover" /> : <span>已失效参考</span>}<button type="button" disabled={disabled} aria-label={`移除单屏参考图 ${input?.title || id}`} onClick={() => onChange({ referenceNodeIds: screen.referenceNodeIds?.filter((item) => item !== id) })}>×</button></div>; })}</div>}
            </div>}
            <label className="commerce-field">完整提示词覆盖 <span className="commerce-editor-hint">可选；填写后优先使用，AI 重策划不会覆盖。</span><Input.TextArea aria-label={`第 ${index + 1} 屏完整提示词`} disabled={disabled} value={screen.promptOverride || ""} rows={5} placeholder="留空按上述参数自动组装；填写后直接使用此处完整提示词。" onChange={(event) => onChange({ promptOverride: event.target.value })} /></label>
            <Button disabled={disabled} icon={<Sparkles size={15} />} onClick={onReplan}>{replica ? "AI 重新分析此模板" : "AI 重新策划此屏"}</Button>
            {textPrice && <p className="commerce-editor-hint">{replica ? "重新分析" : "重策划"}费用：{textPrice}，与图片费用分开计算。</p>}
        </div>
    </AppModal>;
}
