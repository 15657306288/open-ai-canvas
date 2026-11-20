import { expect, test } from "bun:test";
import { planWorkbenchBatch } from "../src/pages/workbench/workbench-batches";

test("fission and SKU can retry one output without rerunning siblings", () => {
    for (const mode of ["fission", "sku"] as const) expect(planWorkbenchBatch("image", 4, 4, mode)).toEqual([1, 1, 1, 1]);
    expect(planWorkbenchBatch("image", 4, 4)).toEqual([4]);
});
