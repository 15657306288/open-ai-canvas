import type { ReactNode } from "react";
import { useLocation } from "react-router";

import { CanvasDeleteProjectsDialog } from "@/components/canvas/canvas-delete-projects-dialog";
import { AppWorkspaceShell } from "@/components/layout/app-top-nav";
import { cn } from "@/lib/utils";
import { isSpatialWorkbenchPath } from "@/lib/workspace-routes";

export default function UserLayout({ children }: { children: ReactNode }) {
    const { pathname } = useLocation();
    const spatialWorkbench = isSpatialWorkbenchPath(pathname);
    return (
        <div className={cn("app-user-workspace h-dvh overflow-hidden text-foreground", spatialWorkbench && "app-spatial-workspace")}>
            <AppWorkspaceShell>{children}</AppWorkspaceShell>
            <CanvasDeleteProjectsDialog />
        </div>
    );
}
