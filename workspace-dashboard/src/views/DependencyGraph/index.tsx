// workspace-dashboard/src/views/DependencyGraph/index.tsx
import { useState, useEffect, useCallback, useRef } from 'react';
import { useNavigate } from 'react-router-dom';
import { GitBranch, ZoomIn, ZoomOut, Maximize2 } from 'lucide-react';
import { API_URL, SDK_TOKEN } from '../../config.js';

interface GraphNode { id: string; name: string; blast_radius?: string; flag_type?: string; val?: number; }
interface GraphLink { source: string; target: string; }
interface GraphData { nodes: GraphNode[]; links: GraphLink[]; }

const BLAST_COLOR: Record<string, string> = {
  HIGH:    '#f87171',
  MEDIUM:  '#fbbf24',
  LOW:     '#4ade80',
  BLOCKED: '#a78bfa',
};

export default function DependencyGraph() {
  const [graphData, setGraphData] = useState<GraphData>({ nodes: [], links: [] });
  const [loading, setLoading] = useState(true);
  const [hovered, setHovered] = useState<GraphNode | null>(null);
  const [FG, setFG] = useState<React.ComponentType<unknown> | null>(null);
  const graphRef = useRef<unknown>(null);
  const navigate = useNavigate();

  // Dynamic import react-force-graph (heavy lib)
  useEffect(() => {
    import('react-force-graph').then((mod: Record<string, unknown>) => {
      setFG(() => mod.ForceGraph2D as React.ComponentType<unknown>);
    });
  }, []);

  useEffect(() => {
    const hdrs = { Authorization: `Bearer ${SDK_TOKEN}` };
    fetch(`${API_URL}/api/v1/flags`, { headers: hdrs })
      .then(r => r.json())
      .then((data: { flags?: Array<{ key: string; name: string; flag_type: string; prerequisite_flags?: string[] }> }) => {
        const flags = data.flags ?? [];
        const nodes: GraphNode[] = flags.map(f => ({
          id: f.key,
          name: f.name || f.key,
          flag_type: f.flag_type,
          val: 4,
        }));
        const links: GraphLink[] = [];
        for (const f of flags) {
          for (const dep of (f.prerequisite_flags ?? [])) {
            links.push({ source: f.key, target: dep });
          }
        }
        setGraphData({ nodes, links });
      })
      .catch(() => {
        // Demo data when API unreachable
        setGraphData({
          nodes: [
            { id: 'auth-v2', name: 'Auth V2', blast_radius: 'HIGH', val: 8 },
            { id: 'new-checkout', name: 'New Checkout', blast_radius: 'MEDIUM', val: 6 },
            { id: 'dark-mode', name: 'Dark Mode', blast_radius: 'LOW', val: 4 },
            { id: 'feature-x', name: 'Feature X', blast_radius: 'LOW', val: 4 },
          ],
          links: [
            { source: 'new-checkout', target: 'auth-v2' },
            { source: 'feature-x', target: 'new-checkout' },
          ],
        });
      })
      .finally(() => setLoading(false));
  }, []);

  const handleNodeClick = useCallback((node: GraphNode) => {
    navigate(`/flags/${node.id}`);
  }, [navigate]);

  const handleNodeHover = useCallback((node: GraphNode | null) => {
    setHovered(node);
    if (document.body) document.body.style.cursor = node ? 'pointer' : 'default';
  }, []);

  if (loading || !FG) {
    return (
      <div style={{ padding: '32px 40px' }}>
        <div style={{ height: 'calc(100vh - 180px)', display: 'flex', alignItems: 'center', justifyContent: 'center' }}>
          <div style={{ textAlign: 'center', color: 'var(--color-fg-subtle)' }}>
            <GitBranch size={40} style={{ marginBottom: 16, opacity: 0.3 }} />
            <div>Loading dependency graph…</div>
          </div>
        </div>
      </div>
    );
  }

  const Graph = FG as React.ComponentType<{
    ref: React.Ref<unknown>;
    graphData: GraphData;
    nodeId: string;
    nodeLabel: string;
    nodeColor: (n: GraphNode) => string;
    nodeVal: (n: GraphNode) => number;
    linkColor: () => string;
    linkWidth: number;
    backgroundColor: string;
    onNodeClick: (n: GraphNode) => void;
    onNodeHover: (n: GraphNode | null) => void;
    dagMode: string;
    dagLevelDistance: number;
    width: number;
    height: number;
    nodeCanvasObject: (n: GraphNode, ctx: CanvasRenderingContext2D, scale: number) => void;
  }>;

  return (
    <div style={{ padding: '24px 32px', display: 'flex', flexDirection: 'column', height: 'calc(100vh - 60px)', gap: 16 }}>
      {/* Header */}
      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
        <div>
          <h1 style={{ fontSize: 24, fontWeight: 700, color: 'var(--color-fg)', margin: '0 0 4px' }}>Causal Graph</h1>
          <p style={{ fontSize: 13, color: 'var(--color-fg-subtle)', margin: 0 }}>
            {graphData.nodes.length} flags · {graphData.links.length} dependencies
          </p>
        </div>
        <div style={{ display: 'flex', gap: 8 }}>
          {[
            { icon: ZoomIn,    label: 'Zoom in',  fn: () => { const g = graphRef.current as { zoom: (v: number) => void } | null; g?.zoom(2); } },
            { icon: ZoomOut,   label: 'Zoom out', fn: () => { const g = graphRef.current as { zoom: (v: number) => void } | null; g?.zoom(0.5); } },
            { icon: Maximize2, label: 'Fit',      fn: () => { const g = graphRef.current as { zoomToFit: (ms: number) => void } | null; g?.zoomToFit(400); } },
          ].map(({ icon: Icon, label, fn }) => (
            <button key={label} onClick={fn} title={label} style={{
              width: 36, height: 36, borderRadius: 8, display: 'flex', alignItems: 'center', justifyContent: 'center',
              background: 'var(--color-bg-elevated)', border: '1px solid var(--color-border)',
              color: 'var(--color-fg-muted)', cursor: 'pointer',
            }}>
              <Icon size={14} />
            </button>
          ))}
        </div>
      </div>

      {/* Legend */}
      <div style={{ display: 'flex', gap: 16 }}>
        {Object.entries(BLAST_COLOR).map(([label, color]) => (
          <div key={label} style={{ display: 'flex', alignItems: 'center', gap: 6, fontSize: 11, color: 'var(--color-fg-subtle)' }}>
            <div style={{ width: 8, height: 8, borderRadius: '50%', background: color }} />
            {label}
          </div>
        ))}
      </div>

      {/* Graph */}
      <div style={{
        flex: 1, borderRadius: 12,
        border: '1px solid var(--color-border)',
        overflow: 'hidden',
        background: 'var(--color-bg-base)',
      }}>
        <Graph
          ref={graphRef as React.Ref<unknown>}
          graphData={graphData}
          nodeId="id"
          nodeLabel="name"
          nodeColor={(n: GraphNode) => BLAST_COLOR[n.blast_radius ?? 'LOW'] ?? '#4ade80'}
          nodeVal={(n: GraphNode) => n.val ?? 4}
          linkColor={() => 'rgba(255,255,255,0.1)'}
          linkWidth={1}
          backgroundColor="transparent"
          onNodeClick={handleNodeClick}
          onNodeHover={handleNodeHover}
          dagMode="td"
          dagLevelDistance={80}
          width={window.innerWidth - 320}
          height={window.innerHeight - 300}
          nodeCanvasObject={(node: GraphNode, ctx: CanvasRenderingContext2D, scale: number) => {
            const r = Math.sqrt(node.val ?? 4) * 4;
            const color = BLAST_COLOR[node.blast_radius ?? 'LOW'] ?? '#4ade80';
            ctx.beginPath();
            ctx.arc(0, 0, r, 0, 2 * Math.PI);
            ctx.fillStyle = `${color}33`;
            ctx.fill();
            ctx.strokeStyle = color;
            ctx.lineWidth = 1.5 / scale;
            ctx.stroke();
            if (scale > 1.5) {
              ctx.font = `${11 / scale}px "JetBrains Mono", monospace`;
              ctx.fillStyle = color;
              ctx.textAlign = 'center';
              ctx.textBaseline = 'middle';
              ctx.fillText(node.id, 0, r + 10 / scale);
            }
          }}
        />
      </div>

      {/* Tooltip */}
      {hovered && (
        <div style={{
          position: 'fixed', bottom: 80, left: '50%', transform: 'translateX(-50%)',
          background: 'var(--color-bg-elevated)',
          border: '1px solid var(--color-border-strong)',
          borderRadius: 10, padding: '10px 16px',
          boxShadow: 'var(--glow-accent)',
          fontSize: 13, color: 'var(--color-fg)',
          display: 'flex', gap: 12, alignItems: 'center',
          pointerEvents: 'none',
          zIndex: 100,
        }}>
          <code style={{ color: 'var(--color-accent)', fontFamily: 'var(--font-mono)' }}>{hovered.id}</code>
          {hovered.blast_radius && <span className={`badge badge-risk-${hovered.blast_radius.toLowerCase()}`}>{hovered.blast_radius}</span>}
          <span style={{ color: 'var(--color-fg-subtle)', fontSize: 11 }}>Click to view details</span>
        </div>
      )}
    </div>
  );
}
