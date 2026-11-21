import { layout, type LEdge, type LNode } from "./layout";

// Lays the picture out off the page's thread, so a graph of several hundred nodes does not freeze it.
self.onmessage = (e: MessageEvent<{ id: number; nodes: LNode[]; edges: LEdge[]; focus: string }>) => {
  const { id, nodes, edges, focus } = e.data;
  (self as unknown as { postMessage: (m: unknown) => void }).postMessage({ id, layout: layout(nodes, edges, focus) });
};
