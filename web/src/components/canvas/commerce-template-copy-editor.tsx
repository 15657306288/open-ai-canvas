import { Button, Input } from "antd";
import { nanoid } from "nanoid";
import { currentTemplateCopy } from "@/lib/canvas/commerce-workflow";
import type { CanvasNodeData } from "@/types/canvas";
import type { CommerceTemplateCopy, CommerceWorkflow } from "@/types/commerce-workflow";

export function CommerceTemplateCopyEditor({ data, templates, disabled, onChange, onRecognize }: {
    data: CommerceWorkflow; templates: CanvasNodeData[]; disabled: boolean;
    onChange: (templateId: string, copy: CommerceTemplateCopy) => void;
    onRecognize: (templateId: string) => void;
}) {
    return <div className="space-y-3">{templates.map((template, index) => {
        const copy = currentTemplateCopy(data, template);
        return <section className="commerce-copy-section" key={template.id}>
            <div className="flex items-center justify-between gap-2"><strong>版式 {index + 1} · 文案替换</strong>
                <Button size="small" disabled={disabled || copy.recognized} onClick={() => onRecognize(template.id)}>{copy.recognized ? "已识别 · 免费复用" : "识别模板文字"}</Button></div>
            <p className="text-xs opacity-65">已填 {copy.pairs.filter((pair) => pair.replacement.trim()).length} / {copy.pairs.length}；留空保持原文。也可以不识别，直接添加替换规则。</p>
            {copy.pairs.map((pair) => <div className="commerce-copy-pair" key={pair.id}>
                <Input.TextArea aria-label={`版式 ${index + 1} 原文`} autoSize={{ minRows: 1, maxRows: 3 }} value={pair.original} readOnly={!pair.manual} disabled={disabled}
                    onChange={(event) => onChange(template.id, { ...copy, pairs: copy.pairs.map((item) => item.id === pair.id ? { ...item, original: event.target.value } : item) })} />
                <span aria-hidden>→</span>
                <Input.TextArea aria-label={`版式 ${index + 1} 替换文案`} placeholder="留空保持原文" autoSize={{ minRows: 1, maxRows: 3 }} value={pair.replacement} disabled={disabled}
                    onChange={(event) => onChange(template.id, { ...copy, pairs: copy.pairs.map((item) => item.id === pair.id ? { ...item, replacement: event.target.value } : item) })} />
                <button type="button" aria-label="删除替换规则" disabled={disabled} onClick={() => onChange(template.id, { ...copy, pairs: copy.pairs.filter((item) => item.id !== pair.id) })}>×</button>
            </div>)}
            <Button size="small" disabled={disabled || copy.pairs.length >= 150} onClick={() => onChange(template.id, { ...copy, pairs: [...copy.pairs, { id: nanoid(), original: "", replacement: "", manual: true }] })}>＋ 添加一条</Button>
        </section>;
    })}</div>;
}
