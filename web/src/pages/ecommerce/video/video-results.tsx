import { Button, Dropdown } from "antd";
import { Clapperboard, Columns2, Download, Pencil, Play, RefreshCw, Trash2, TriangleAlert } from "lucide-react";
import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";

import { VideoPlayer } from "@/components/video-player";
import { currentLocale, translate } from "@/i18n";
import { resourceFileUrl } from "@/services/api/resources";
import { downloadVideoReplicationComparison, type VideoReplicationProject } from "@/services/api/video-replication";
import { downloadBrowserMedia, startBrowserDownload } from "@/services/browser-download";

import { isActiveProject, type EcommerceVideoState } from "./use-ecommerce-video";
import { VideoGeneratingCard } from "./video-generating";
import type { ReplaceKind } from "./video-kinds";

// 作品卡片上的类型名，中文与原来的「类型名 + 替换」拼接结果逐字一致。
const KIND_LABEL_KEYS: Record<ReplaceKind, string> = {
    model: "model",
    clothing: "clothing",
    face: "face",
    background: "background",
    product: "product",
    "precise-product": "preciseProduct",
    "precise-person": "precisePerson",
    combination: "combination",
};

type Filter = "all" | "running" | "done" | "failed";
type CardStatus = Exclude<Filter, "all"> | "waiting";

// 服务端按创建时间倒序返回成片版本：取最新一版成功的成片（再次生成后展示新成片）。
function outputOf(project: VideoReplicationProject) {
    return project.outputs.find((item) => item.status === "succeeded" && item.resourceId);
}

function statusOf(project: VideoReplicationProject): CardStatus {
    // 后台已标记失败：直接报错，不再显示生成中等待（即使上一版成片还在，也先告诉用户这次失败了）。
    if (project.status === "failed") return "failed";
    if (isActiveProject(project)) return "running";
    if (outputOf(project)) return "done";
    if (project.error) return "failed";
    // 一键生成被暂停（自动续接下一段失败、预算不足等）：后台已经给出原因，按失败展示。
    if (project.parameters.auto && project.paused && project.pauseReason) return "failed";
    // 分析完但没有在跑的任务（旧版两步式项目等待确认、自动提交被中断）：不是生成中，允许用户直接开始生成。
    return "waiting";
}

/** 筛选归类：等待开始的作品算在「进行中」里。 */
function filterOf(project: VideoReplicationProject): Exclude<Filter, "all"> {
    const status = statusOf(project);
    return status === "waiting" ? "running" : status;
}

function kindLabel(project: VideoReplicationProject) {
    const mode = project.parameters.replaceMode;
    if (!mode) return translate("ecommerceVideo.results.kind.hot");
    return translate(`ecommerceVideo.results.kind.${KIND_LABEL_KEYS[mode] || "product"}`);
}

function timeLabel(value: string) {
    const date = new Date(value);
    if (Number.isNaN(date.getTime())) return "";
    const today = new Date();
    const time = date.toLocaleTimeString(currentLocale(), { hour: "2-digit", minute: "2-digit" });
    return date.toDateString() === today.toDateString() ? translate("ecommerceVideo.results.today", { time }) : `${date.getMonth() + 1}/${date.getDate()} ${time}`;
}

function ResultCard({ project, state }: { project: VideoReplicationProject; state: EcommerceVideoState }) {
    const { t } = useTranslation();
    const output = outputOf(project);
    const [downloadMode, setDownloadMode] = useState<"video" | "comparison" | null>(null);
    const [downloadError, setDownloadError] = useState("");
    const downloadRequest = useRef<AbortController | null>(null);
    useEffect(() => () => downloadRequest.current?.abort(), []);
    const download = async (mode: "video" | "comparison") => {
        if (!output || downloadRequest.current) return;
        const controller = new AbortController();
        downloadRequest.current = controller;
        setDownloadMode(mode);
        setDownloadError("");
        try {
            const title = (project.title || t("ecommerceVideo.results.untitled")).replace(/[\\/:*?"<>|]/g, "-");
            if (mode === "video") {
                await downloadBrowserMedia({ storageKey: `resource:${output.resourceId}`, fileName: `${title}.mp4` });
            } else {
                const blob = await downloadVideoReplicationComparison(project.id, output.id, controller.signal);
                const url = URL.createObjectURL(blob);
                try {
                    startBrowserDownload(url, `${title}-${t("ecommerceVideo.results.comparisonSuffix")}.mp4`);
                } finally {
                    window.setTimeout(() => URL.revokeObjectURL(url), 60_000);
                }
            }
        } catch (error) {
            if (!controller.signal.aborted) setDownloadError(error instanceof Error ? error.message : t("ecommerceVideo.results.downloadFailed"));
        } finally {
            downloadRequest.current = null;
            setDownloadMode(null);
        }
    };
    const reference = project.assets.find((asset) => asset.category === "reference_video");
    const status = statusOf(project);
    const portrait = (project.parameters.ratio || "9:16").split(":").map(Number);
    const aspect = portrait[0] && portrait[1] ? `${portrait[0]} / ${portrait[1]}` : "9 / 16";
    return (
        <article className={`ecv-result is-${status}`} data-landscape={portrait[0] / portrait[1] >= 1.5}>
            <header className="ecv-result-head">
                <span className="ecv-result-kind">{kindLabel(project)}</span>
                {project.spentCredits > 0 ? <span className="ecv-result-cost">{t("ecommerceVideo.results.cost", { credits: project.spentCredits.toLocaleString(currentLocale(), { maximumFractionDigits: 3 }) })}</span> : null}
                <time>{timeLabel(project.createdAt)}</time>
            </header>
            <div className="ecv-result-media" style={{ aspectRatio: aspect }}>
                {status === "running" ? (
                    // 生成中只显示生视频动画与真实阶段文案，不展示用户上传的参考素材。
                    <VideoGeneratingCard project={project} />
                ) : output ? (
                    // 统一播放器的紧凑控件：不出现浏览器原生的下载 / 倍速 / 画中画菜单，下载走卡片底栏。
                    <VideoPlayer src={resourceFileUrl(output.resourceId)} title={project.title || t("ecommerceVideo.results.untitled")} className="ecv-result-player" compactControls />
                ) : reference ? (
                    <VideoPlayer src={resourceFileUrl(reference.resourceId)} title={t("ecommerceVideo.results.referenceVideo")} className="ecv-result-player" compactControls />
                ) : (
                    <Clapperboard size={28} />
                )}
                {status === "failed" ? (
                    <div className="ecv-result-banner is-failed">
                        <TriangleAlert size={14} />
                        <span>
                            <strong>{t("ecommerceVideo.results.failed")}</strong>
                            {project.error || project.stage ? ` · ${project.error || project.stage}` : ""}
                        </span>
                    </div>
                ) : null}
                {status === "waiting" ? (
                    <div className="ecv-result-banner">
                        <span>
                            <strong>{t("ecommerceVideo.results.notStarted")}</strong>
                            {project.stage ? ` · ${project.stage}` : ""}
                        </span>
                        <Button size="small" type="primary" icon={<Play size={12} />} onClick={() => void state.regenerate(project)}>
                            {t("ecommerceVideo.results.start")}
                        </Button>
                    </div>
                ) : null}
            </div>
            <footer className="ecv-result-actions">
                <Button size="small" type="text" icon={<Pencil size={13} />} onClick={() => state.edit(project)}>
                    {t("ecommerceVideo.results.edit")}
                </Button>
                <Button size="small" type="text" icon={<RefreshCw size={13} />} disabled={status === "running"} onClick={() => void state.regenerate(project)}>
                    {t("ecommerceVideo.results.regenerate")}
                </Button>
                <span className="ecv-result-spacer" />
                {output ? (
                    <Dropdown
                        trigger={["click"]}
                        menu={{
                            items: [
                                { key: "video", label: t("ecommerceVideo.results.download"), icon: <Download size={14} /> },
                                { key: "comparison", label: t("ecommerceVideo.results.downloadComparison"), icon: <Columns2 size={14} />, disabled: !reference },
                            ],
                            onClick: ({ key }) => void download(key as "video" | "comparison"),
                        }}
                    >
                        <Button size="small" type="text" aria-label={t("ecommerceVideo.results.download")} icon={<Download size={14} />} loading={!!downloadMode} disabled={!!downloadMode} />
                    </Dropdown>
                ) : null}
                <Button size="small" type="text" danger aria-label={t("common.delete")} icon={<Trash2 size={14} />} disabled={status === "running"} onClick={() => state.remove(project)} />
            </footer>
            {downloadMode ? (
                <p role="status" className="ecv-hint">
                    {t(downloadMode === "comparison" ? "ecommerceVideo.results.creatingComparison" : "ecommerceVideo.results.preparingDownload")}
                </p>
            ) : null}
            {downloadError ? (
                <p role="alert" className="ecv-hint">
                    {downloadError}
                </p>
            ) : null}
        </article>
    );
}

function VideoResultsGrid({ projects, state }: { projects: VideoReplicationProject[]; state: EcommerceVideoState }) {
    const grid = useRef<HTMLDivElement>(null);
    useLayoutEffect(() => {
        const element = grid.current;
        if (!element) return;
        const cards = Array.from(element.querySelectorAll<HTMLElement>(".ecv-result"));
        let frame = 0;
        const measure = () => {
            const styles = getComputedStyle(element);
            const row = parseFloat(styles.gridAutoRows);
            const gap = parseFloat(styles.rowGap);
            const sizes = cards.map((card) => Math.ceil((card.getBoundingClientRect().height + gap) / (row + gap)));
            cards.forEach((card, index) => {
                const cell = card.parentElement!;
                const span = `span ${sizes[index]}`;
                if (cell.style.gridRowEnd !== span) cell.style.gridRowEnd = span;
            });
        };
        const observer = new ResizeObserver(() => {
            cancelAnimationFrame(frame);
            frame = requestAnimationFrame(measure);
        });
        cards.forEach((card) => observer.observe(card));
        measure();
        return () => {
            observer.disconnect();
            cancelAnimationFrame(frame);
        };
    }, [projects]);
    return (
        <div className="ecv-results-grid" ref={grid}>
            {projects.map((project) => (
                <div className="ecv-result-cell" key={project.id}>
                    <ResultCard project={project} state={state} />
                </div>
            ))}
        </div>
    );
}

export function EcommerceVideoResults({ state }: { state: EcommerceVideoState }) {
    const { t } = useTranslation();
    const [filter, setFilter] = useState<Filter>("all");
    const counts = useMemo(() => {
        const result = { all: state.projects.length, running: 0, done: 0, failed: 0 };
        for (const project of state.projects) result[filterOf(project)] += 1;
        return result;
    }, [state.projects]);
    const visible = filter === "all" ? state.projects : state.projects.filter((project) => filterOf(project) === filter);
    const tabs: Array<{ value: Filter; label: string }> = [
        { value: "all", label: t("ecommerceVideo.results.filters.all") },
        { value: "running", label: t("ecommerceVideo.results.filters.running") },
        { value: "done", label: counts.done ? t("ecommerceVideo.results.filters.doneCount", { count: counts.done }) : t("ecommerceVideo.results.filters.done") },
        { value: "failed", label: t("ecommerceVideo.results.filters.failed") },
    ];
    return (
        <section className="commerce-output-gallery ecv-results" aria-label={t("ecommerceVideo.results.title")}>
            <header className="ecv-results-head">
                <h2>
                    {t("ecommerceVideo.results.title")} <small>{t("ecommerceVideo.results.completedCount", { count: counts.done })}</small>
                </h2>
                <nav className="ecv-filter" aria-label={t("ecommerceVideo.results.filterLabel")}>
                    {tabs.map((tab) => (
                        <button key={tab.value} type="button" aria-pressed={filter === tab.value} onClick={() => setFilter(tab.value)}>
                            {tab.label}
                        </button>
                    ))}
                </nav>
            </header>
            <div className="ecv-results-scroll" data-canvas-wheel-scroll>
                {state.loadingProjects ? (
                    <p className="ecv-empty">{t("ecommerceVideo.results.loading")}</p>
                ) : visible.length ? (
                    <VideoResultsGrid projects={visible} state={state} />
                ) : (
                    <div className="ecv-empty">
                        <Clapperboard size={28} />
                        <strong>{filter === "all" ? t("ecommerceVideo.results.empty") : t("ecommerceVideo.results.noMatch")}</strong>
                        <span>{t("ecommerceVideo.results.emptyHint")}</span>
                    </div>
                )}
                {visible.length ? <p className="ecv-end">{t("ecommerceVideo.results.end")}</p> : null}
            </div>
        </section>
    );
}
