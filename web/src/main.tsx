import "@fontsource-variable/inter";
import "@fontsource-variable/jetbrains-mono";
import { installChunkRecovery } from "@/lib/chunk-recovery";
import { bootstrapAppearance } from "@/services/appearance-bootstrap";
import { isIsolatedPrevisRepro } from "@/lib/dev-repro";

installChunkRecovery();

// The public film entry checks its availability independently of workspace bootstrap.
// When welcomeEnabled is off the welcome app redirects back to the workspace, which
// keeps the retired-brand-homepage behaviour while staying configurable from the admin UI.
if (/^\/welcome\/?$/.test(window.location.pathname)) {
    void bootstrapAppearance().catch(() => undefined);
    void import("./welcome-application");
} else {
    // The backend-free DEV lab must not make requests before AppProviders isolates it.
    const appearanceReady = isIsolatedPrevisRepro(import.meta.env.DEV, window.location.pathname) ? Promise.resolve() : bootstrapAppearance();
    void import("./application");
    void appearanceReady.catch(() => undefined);
}
