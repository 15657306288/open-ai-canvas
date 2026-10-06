export type CommerceScreen = {
    id: string;
    title: string;
    copy: string;
    prompt: string;
    templateNodeId?: string;
    /** Explicit full prompt; replanning must never overwrite this field. */
    promptOverride?: string;
    referenceNodeIds?: string[];
    size?: string;
    sceneType?: string;
    sellingPoints?: string;
};

export type CommerceCopyPair = { id: string; original: string; replacement: string; manual?: boolean };
export type CommerceTemplateCopy = { identity: string; recognized: boolean; pairs: CommerceCopyPair[] };
export type CommercePending = {
    operationId: string;
    kind: "plan" | "replan" | "ocr";
    fingerprint?: string;
    inputSnapshot: string;
    screens: CommerceScreen[];
    /** New detail requests require product/style analysis and concrete visual decisions. */
    detailPlanVersion?: 2;
    /** New replica requests require per-template analysis before image submission. */
    replicaPlanVersion?: 1;
    templateNodeId?: string;
    templateIdentity?: string;
};

export type CommerceWorkflow = {
    productName: string;
    brief: string;
    platform: string;
    screenCount: number;
    language: string;
    textMode: "typeset" | "none";
    copyMode: "keep" | "rewrite";
    modelMode: string;
    font: string;
    colorRhythm: string;
    textModel?: string;
    followTemplate?: boolean;
    /** Unassigned connected images are products; secondary images are style references/templates. */
    secondaryNodeIds: string[];
    screens: CommerceScreen[];
    templateCopies?: Record<string, CommerceTemplateCopy>;
    /** Each run owns its configuration, input identities and results. */
    role?: "source" | "result";
    sourceNodeId?: string;
    runMode?: "plan" | "manual" | "direct" | "replica";
    inputNodeIds?: string[];
    inputIdentities?: Record<string, string>;
    extraReferenceNodeIds?: string[];
    extraReferenceIdentities?: Record<string, string>;
    autoGenerate?: "awaiting-plan" | "ready";
    rawOutput?: string;
    pending?: CommercePending;
};
