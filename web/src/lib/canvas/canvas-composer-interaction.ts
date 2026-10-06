const COMPOSER_CONTROL_SELECTOR = [
    "button", "input", "textarea", "select", "a", "label",
    '[role="button"]', '[role="combobox"]', '[role="slider"]', '[role="menuitem"]', '[role="listbox"]',
    '[contenteditable]:not([contenteditable="false"])', "[data-canvas-no-drag]",
    ".canvas-node-composer-editor", ".ant-popover", ".ant-dropdown", ".ant-select-dropdown", ".ant-modal", '[role="dialog"]',
].join(",");

export function isCanvasComposerDragTarget(target: Element | null, container: Element): boolean {
    // Portal events bubble through React but must never drag the underlying node.
    return Boolean(target && container.contains(target) && !target.closest(COMPOSER_CONTROL_SELECTOR));
}
