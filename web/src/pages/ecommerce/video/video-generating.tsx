import { Film } from "lucide-react";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";

import { translate } from "@/i18n";
import type { VideoReplicationProject } from "@/services/api/video-replication";

import "./video-generating.css";

/*
 * 作品卡片「生成中」：与首页生视频卡片同一套视觉（光晕漂移 + 扫光 + 阶段标题 + 三段进度），
 * 不展示用户上传的参考素材；标题跟随服务端真实阶段文案，底部按 理解视频 → 逐段生成 → 合成成片 推进。
 */

const STEP_KEYS = ["understand", "generate", "compose"] as const;

// 再次生成时项目创建时间很早：超过 3 小时的按本页第一次看到它在生成的时间计时。
const firstSeen = new Map<string, number>();

function startedAt(project: VideoReplicationProject) {
    const created = new Date(project.createdAt).getTime();
    if (Number.isFinite(created) && Date.now() - created < 3 * 3600 * 1000) return created;
    if (!firstSeen.has(project.id)) firstSeen.set(project.id, Date.now());
    return firstSeen.get(project.id)!;
}

function useElapsed(since: number) {
    const [now, setNow] = useState(() => Date.now());
    useEffect(() => {
        const timer = window.setInterval(() => setNow(Date.now()), 1000);
        return () => window.clearInterval(timer);
    }, []);
    const seconds = Math.max(0, Math.floor((now - since) / 1000));
    const minutes = Math.floor(seconds / 60);
    return minutes >= 60 ? translate("ecommerceVideo.generating.hours", { hours: Math.floor(minutes / 60), minutes: minutes % 60 }) : `${String(minutes).padStart(2, "0")}:${String(seconds % 60).padStart(2, "0")}`;
}

// analyzed / completed 出现在这里时是一键生成分析完、正在提交本轮生成（见 isAutoRunPending）：理解已完成，进入逐段生成。
const pendingSubmit = (project: VideoReplicationProject) => project.status === "analyzed" || project.status === "completed";

function stepOf(project: VideoReplicationProject) {
    if (project.status === "composing") return 2;
    if (project.status === "generating" || pendingSubmit(project)) return 1;
    return 0;
}

function hintOf(step: number) {
    if (step === 0) return translate("ecommerceVideo.generating.hintUnderstand");
    if (step === 1) return translate("ecommerceVideo.generating.hintGenerate");
    return translate("ecommerceVideo.generating.hintCompose");
}

export function VideoGeneratingCard({ project }: { project: VideoReplicationProject }) {
    const { t } = useTranslation();
    const steps = STEP_KEYS.map((key) => t(`ecommerceVideo.generating.steps.${key}`));
    const step = stepOf(project);
    const elapsed = useElapsed(startedAt(project));
    // 提交中的项目进度字段还是分析阶段的 100（或上一版成片的 100），不显示百分比。
    const progress = pendingSubmit(project) ? 0 : Math.max(0, Math.min(99, Math.round(project.progress || 0)));
    const segments = new Set((project.plan?.segments || []).map((segment) => segment.shotId));
    const total = segments.size;
    const done = new Set(project.runs.filter((run) => run.status === "succeeded" && segments.has(run.shotId)).map((run) => run.shotId)).size;
    const params = project.parameters;
    const spec = [params.ratio, params.resolution?.toUpperCase(), params.modelLabel].filter(Boolean).join(" · ");
    const meta = [progress ? `${progress}%` : "", step === 1 && total > 1 ? t("ecommerceVideo.generating.segmentsDone", { done: Math.min(done, total), total }) : "", t("ecommerceVideo.generating.elapsed", { elapsed })].filter(Boolean).join(" · ");
    return (
        <div className="ecv-gen" data-step={step} aria-busy="true">
            <span className="ecv-gen-surface" aria-hidden="true">
                <i />
                <i />
                <i />
            </span>
            {spec ? (
                <span className="ecv-gen-spec">
                    <Film aria-hidden="true" />
                    {spec}
                </span>
            ) : null}
            <span className="ecv-gen-body">
                <span className="ecv-gen-reel" aria-hidden="true">
                    <span />
                    <span />
                    <span />
                </span>
                <span key={project.stage} className="ecv-gen-title" role="status">
                    {/* 再次生成提交前，阶段文案还是上一版的「完整视频已完成」，不能显示在生成中卡片上。 */}
                    {(project.status !== "completed" && project.stage) || (project.status === "analyzing" ? t("ecommerceVideo.generating.queued") : t("ecommerceVideo.generating.running"))}
                </span>
                <span className="ecv-gen-meta">{meta}</span>
                <span key={step} className="ecv-gen-hint">
                    {hintOf(step)}
                </span>
            </span>
            <ol className="ecv-gen-steps" aria-label={t("ecommerceVideo.generating.progress", { step: steps[step] })}>
                {steps.map((label, index) => (
                    <li key={label} data-state={index < step ? "done" : index === step ? "active" : "todo"} aria-current={index === step ? "step" : undefined}>
                        <span>
                            <i />
                        </span>
                        <small>{label}</small>
                    </li>
                ))}
            </ol>
        </div>
    );
}
