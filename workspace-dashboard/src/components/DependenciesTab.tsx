import { useState, useEffect } from 'react';
import { DependencyGraph } from './DependencyGraph.js';
import { CriticalFlagsPanel } from './CriticalFlagsPanel.js';

interface Node {
  key: string;
  enabled: boolean;
  rollout_pct: number;
}

interface Edge {
  source: string;
  target: string;
  weight: number;
}

interface DependenciesTabProps {
  flagKey: string;
  apiUrl: string;
  token: string;
}

export function DependenciesTab({ flagKey, apiUrl, token }: DependenciesTabProps) {
  const [nodes, setNodes] = useState<Node[]>([]);
  const [edges, setEdges] = useState<Edge[]>([]);
  const [depth, setDepth] = useState(1);
  const [selectedNode, setSelectedNode] = useState<string | null>(null);
  const [highlightedSubgraph, setHighlightedSubgraph] = useState<Set<string> | undefined>(undefined);
  const [disableSimulation, setDisableSimulation] = useState(false);
  const [loading, setLoading] = useState(true);

  const intelUrl = apiUrl.replace(':8081', ':8083').replace('8081', '8083');

  useEffect(() => {
    setLoading(true);
    fetch(`${intelUrl}/api/v1/graph/dependencies?flag_key=${flagKey}&depth=${depth}`, {
      headers: { Authorization: `Bearer ${token}` },
    })
      .then(r => r.json())
      .then(data => {
        setNodes(data.nodes || []);
        setEdges(data.edges || []);
      })
      .catch(console.error)
      .finally(() => setLoading(false));
  }, [flagKey, depth, intelUrl, token]);

  const handleNodeClick = (key: string) => {
    setSelectedNode(key);
    // Compute reachable subgraph from selected node (for "disable simulation" mode)
    const reachable = new Set<string>([key]);
    const queue = [key];
    while (queue.length > 0) {
      const current = queue.shift()!;
      edges.forEach(edge => {
        if (edge.source === current && !reachable.has(edge.target)) {
          reachable.add(edge.target);
          queue.push(edge.target);
        }
      });
    }
    setHighlightedSubgraph(reachable);
  };

  const handleDisableSimulation = () => {
    setDisableSimulation(!disableSimulation);
  };

  if (loading) return <div className="text-gray-400">Loading dependencies...</div>;

  return (
    <div className="space-y-4">
      <div className="flex items-center gap-4">
        <label className="text-sm text-gray-300">
          Depth:
          <select
            value={depth}
            onChange={e => setDepth(Number(e.target.value))}
            className="ml-2 bg-gray-800 border border-gray-700 rounded px-2 py-1"
          >
            <option value={1}>1 hop</option>
            <option value={2}>2 hops</option>
            <option value={3}>3 hops</option>
          </select>
        </label>
        <button
          onClick={handleDisableSimulation}
          className="px-3 py-1 bg-gray-800 border border-gray-700 rounded text-sm hover:bg-gray-700"
        >
          {disableSimulation ? 'Enable Simulation' : 'Disable Simulation'}
        </button>
      </div>

      {selectedNode && (
        <div className="text-sm text-gray-300">
          Selected: <span className="font-mono text-green-400">{selectedNode}</span>
          {disableSimulation && highlightedSubgraph && (
            <span className="ml-2 text-gray-400">
              ({highlightedSubgraph.size - 1} downstream flags affected)
            </span>
          )}
        </div>
      )}

      <DependencyGraph
        nodes={nodes}
        edges={edges}
        width={800}
        height={600}
        onNodeClick={handleNodeClick}
        highlightedSubgraph={highlightedSubgraph}
        disableSimulation={disableSimulation}
      />

      <div className="mt-6">
        <CriticalFlagsPanel apiUrl={intelUrl} token={token} limit={10} />
      </div>
    </div>
  );
}
