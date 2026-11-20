import { App, Button } from "antd";
import { Eraser, Paintbrush, Undo2 } from "lucide-react";
import { useEffect, useRef, useState, type PointerEvent } from "react";
import { AppModal } from "@/components/ui/product/app-modal/app-modal";
import { exportEditMask, paintMaskStrokes, type MaskPoint, type MaskStroke } from "@/lib/canvas/image-mask-document";
import { dataUrlToFile } from "@/lib/image-utils";
import { resourceFileUrl, uploadResourceFile } from "@/services/api/resources";

export function StudioMaskEditor({ source, close, save }: { source: string; close: () => void; save: (id: string) => void }) {
    const { message } = App.useApp();
    const canvas = useRef<HTMLCanvasElement>(null);
    const active = useRef<number | null>(null);
    const [size, setSize] = useState({ width: 0, height: 0 });
    const [diameter, setDiameter] = useState(4);
    const [erase, setErase] = useState(false);
    const [busy, setBusy] = useState(false);
    const [strokes, setStrokes] = useState<MaskStroke[]>([]);
    useEffect(() => {
        if (canvas.current && size.width) paintMaskStrokes(canvas.current, strokes, "#f04863");
    }, [size, strokes]);
    const point = (e: PointerEvent<HTMLCanvasElement>): MaskPoint => {
        const box = e.currentTarget.getBoundingClientRect();
        return [((e.clientX - box.left) * size.width) / box.width, ((e.clientY - box.top) * size.height) / box.height];
    };
    const finish = async () => {
        setBusy(true);
        try {
            const dataUrl = exportEditMask(size.width, size.height, strokes);
            const mask = await uploadResourceFile(dataUrlToFile({ id: "mask", name: "mask.png", type: "image/png", dataUrl }), "image", size);
            save(mask.id);
        } catch (e) {
            message.error(e instanceof Error ? e.message : "蒙版保存失败");
        } finally {
            setBusy(false);
        }
    };
    return (
        <AppModal
            open
            title="涂抹修改区域"
            onCancel={busy ? undefined : close}
            closable={!busy}
            maskClosable={!busy}
            keyboard={!busy}
            confirmLoading={busy}
            okText="使用蒙版"
            cancelText="取消"
            okButtonProps={{ disabled: !size.width || !strokes.length }}
            onOk={finish}
        >
            <div style={{ position: "relative", lineHeight: 0 }}>
                <img alt="编辑原图" src={resourceFileUrl(source)} style={{ width: "100%" }} onError={() => message.error("原图读取失败")} onLoad={(e) => setSize({ width: e.currentTarget.naturalWidth, height: e.currentTarget.naturalHeight })} />
                <canvas
                    aria-label="蒙版画布"
                    ref={canvas}
                    width={size.width}
                    height={size.height}
                    style={{ position: "absolute", inset: 0, width: "100%", height: "100%", touchAction: "none", opacity: 0.6 }}
                    onPointerDown={(e) => {
                        if (!size.width || busy || active.current !== null) return;
                        e.currentTarget.setPointerCapture(e.pointerId);
                        active.current = e.pointerId;
                        const p = point(e);
                        setStrokes((s) => [...s, { diameter: Math.max(1, (Math.min(size.width, size.height) * diameter) / 100), erase, points: [p] }]);
                    }}
                    onPointerMove={(e) => {
                        if (active.current !== e.pointerId) return;
                        const p = point(e);
                        setStrokes((s) => [...s.slice(0, -1), { ...s[s.length - 1], points: [...s[s.length - 1].points, p] }]);
                    }}
                    onLostPointerCapture={() => {
                        active.current = null;
                    }}
                />
            </div>
            <Button aria-label="画笔" aria-pressed={!erase} icon={<Paintbrush />} onClick={() => setErase(false)} />
            <Button aria-label="橡皮擦" aria-pressed={erase} icon={<Eraser />} onClick={() => setErase(true)} />
            <Button aria-label="撤销一笔" icon={<Undo2 />} disabled={!strokes.length} onClick={() => setStrokes((s) => s.slice(0, -1))} />
            <label>
                笔刷大小 <input type="range" min="1" max="20" value={diameter} onChange={(e) => setDiameter(Number(e.target.value))} />
            </label>
        </AppModal>
    );
}
