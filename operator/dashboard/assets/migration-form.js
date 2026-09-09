(function exposeMigrationForm(root, factory) {
  const api = factory();
  if (typeof module === "object" && module.exports) {
    module.exports = api;
  }
  root.MigrationForm = api;
}(globalThis, () => {
  function selectionForPod(pods, nodes, podName) {
    const pod = pods.find((candidate) => candidate.name === podName) || null;
    if (!pod || !pod.node) {
      return { pod, destinations: [] };
    }
    return {
      pod,
      destinations: nodes.filter((node) => node.name !== pod.node),
    };
  }

  function canSubmit({ pod, sourceNode, targetNode, nodes }) {
    return Boolean(
      pod?.workload
      && pod.node === sourceNode
      && targetNode
      && nodes?.some((node) => node.name === targetNode && node.name !== sourceNode),
    );
  }

  return { canSubmit, selectionForPod };
}));
