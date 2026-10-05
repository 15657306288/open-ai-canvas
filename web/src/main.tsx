import "@fontsource-variable/inter";
import "@fontsource-variable/jetbrains-mono";
import { installChunkRecovery } from "@/lib/chunk-recovery";
import { bootstrapAppearance } from "@/services/appearance-bootstrap";
import { isIsolatedDirectorRepro } from "@/lib/dev-repro";

installChunkRecovery();

// The standalone brand homepage has been retired; keep old bookmarks on the workspace.
if (/^\/welcome\/?$/.test(window.location.pathname)) window.location.replace("/");
else {
    // The backend-free DEV lab must not make requests before AppProviders isolates it.
    const appearanceReady = isIsolatedDirectorRepro(import.meta.env.DEV, window.location.pathname) ? Promise.resolve() : bootstrapAppearance();
    void import("./application");
    void appearanceReady.catch(() => undefined);
}
