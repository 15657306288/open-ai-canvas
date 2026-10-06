/**
 * Ephemeral in-memory previews for images produced by a running generation task.
 *
 * The canvas persists finished results on nodes (content / storageKey), but a task
 * that is still running only has a taskId plus a backend preview URL. Commerce
 * result cards need to show that intermediate image without persisting anything,
 * so the preview lives here for the lifetime of the tab.
 *
 * Entries are user-scoped: a preview recorded for one account is never visible to
 * another, and clearing happens on logout or task completion.
 */

type TaskImagePreviewStore = {
    /** taskId -> preview URL */
    byTask: Map<string, string>;
    /** userId -> Set<taskId> */
    byUser: Map<string, Set<string>>;
    listeners: Set<() => void>;
};

const store: TaskImagePreviewStore = {
    byTask: new Map(),
    byUser: new Map(),
    listeners: new Set(),
};

function emit() {
    store.listeners.forEach((listener) => {
        try {
            listener();
        } catch {
            // A broken subscriber must not stop the remaining ones.
        }
    });
}

export function setTaskImagePreview(scope: string | undefined, taskId: string | undefined, url: string | null | undefined) {
    if (!taskId) return;
    const owner = scope || "";
    if (!url) {
        const owned = store.byUser.get(owner);
        if (owned?.delete(taskId)) {
            if (!owned.size) store.byUser.delete(owner);
        }
        if (store.byTask.delete(taskId)) emit();
        return;
    }
    const previousOwner = [...store.byUser.entries()].find(([, tasks]) => tasks.has(taskId))?.[0];
    if (previousOwner !== undefined && previousOwner !== owner) {
        const previous = store.byUser.get(previousOwner);
        previous?.delete(taskId);
        if (previous && !previous.size) store.byUser.delete(previousOwner);
    }
    if (store.byTask.get(taskId) === url) return;
    store.byTask.set(taskId, url);
    const owned = store.byUser.get(owner) || new Set<string>();
    owned.add(taskId);
    store.byUser.set(owner, owned);
    emit();
}

/** Drop every preview owned by one account; called on logout or user switch. */
export function clearTaskImagePreviews(scope?: string) {
    if (scope === undefined) {
        const changed = store.byTask.size > 0;
        store.byTask.clear();
        store.byUser.clear();
        if (changed) emit();
        return;
    }
    const owned = store.byUser.get(scope);
    if (!owned?.size) return;
    owned.forEach((taskId) => store.byTask.delete(taskId));
    store.byUser.delete(scope);
    emit();
}

export function taskImagePreview(scope: string | undefined, taskId: string | undefined): string | undefined {
    if (!taskId) return undefined;
    const owned = store.byUser.get(scope || "");
    if (!owned?.has(taskId)) return undefined;
    return store.byTask.get(taskId);
}

export function subscribeTaskImagePreviews(listener: () => void) {
    store.listeners.add(listener);
    return () => {
        store.listeners.delete(listener);
    };
}
