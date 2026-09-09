const assert = require("node:assert/strict");
const test = require("node:test");

const { canSubmit, selectionForPod } = require("./assets/migration-form.js");

test("selectionForPod uses the live pod node and excludes it from destinations", () => {
  const selection = selectionForPod(
    [
      { name: "payments-0", workload: "payments", node: "worker-a" },
      { name: "catalog-0", workload: "catalog", node: "worker-b" },
    ],
    [{ name: "worker-a" }, { name: "worker-b" }, { name: "worker-c" }],
    "payments-0",
  );

  assert.deepEqual(selection, {
    pod: { name: "payments-0", workload: "payments", node: "worker-a" },
    destinations: [{ name: "worker-b" }, { name: "worker-c" }],
  });
});

test("canSubmit rejects a missing or ineligible destination", () => {
  const pod = { name: "payments-0", workload: "payments", node: "worker-a" };
  const nodes = [{ name: "worker-a" }, { name: "worker-b" }];

  assert.equal(canSubmit({ pod, sourceNode: "worker-a", targetNode: "", nodes }), false);
  assert.equal(canSubmit({ pod, sourceNode: "worker-a", targetNode: "worker-a", nodes }), false);
  assert.equal(canSubmit({ pod, sourceNode: "worker-a", targetNode: "worker-b", nodes }), true);
});
