import { useEffect, useState, type ReactNode } from "react";
import { useLocation, useNavigate } from "react-router";
import { useTranslation } from "react-i18next";

import { SegmentedControl } from "@/components/ui/base/segmented-control/segmented-control";

import { useEcommerceVideo } from "./use-ecommerce-video";
import { EcommerceVideoComposer } from "./video-composer";
import { EcommerceVideoResults } from "./video-results";
import "./ecommerce-video.css";

/** 电商 Agent「商品视频」：左侧参数面板，右侧视频作品；顶部的「商品图片 / 商品视频」切换由页面传入。 */
export default function EcommerceVideoStudio({ kindSwitch, hidden = false }: { kindSwitch: ReactNode; hidden?: boolean }) {
    const { t } = useTranslation();
    const state = useEcommerceVideo();
    const location = useLocation();
    const navigate = useNavigate();
    useEffect(() => {
        const incoming = (location.state as { workPrompt?: unknown } | null)?.workPrompt;
        if (typeof incoming !== "string" || !incoming.trim()) return;
        state.setNotes(incoming.slice(0, 6000));
        navigate(location.pathname + location.search, { replace: true, state: null });
    }, [location.key]);
    const [mobileView, setMobileView] = useState<"create" | "works">("create");
    const running = state.projects.filter((project) => ["analyzing", "generating", "composing"].includes(project.status)).length;
    return (
        <div className="ecs-shell ecv-shell" data-mobile-view={mobileView} style={hidden ? { display: "none" } : undefined} aria-hidden={hidden || undefined}>
            <div className="ecs-mobile-tabs">
                <SegmentedControl<"create" | "works">
                    block
                    ariaLabel={t("ecommerceVideo.studio.switchLabel")}
                    value={mobileView}
                    onChange={setMobileView}
                    options={[
                        { value: "create", label: t("ecommerceVideo.studio.create") },
                        { value: "works", label: running ? t("ecommerceVideo.studio.worksRunning", { count: running }) : t("ecommerceVideo.studio.works") },
                    ]}
                />
            </div>
            <aside className="ecs-composer" aria-label={t("ecommerceVideo.studio.composerLabel")}>
                <header className="ecs-composer-head">{kindSwitch}</header>
                <EcommerceVideoComposer state={state} />
            </aside>
            <EcommerceVideoResults state={state} />
        </div>
    );
}
