// workspace-dashboard/src/views/DependencyGraph/index.tsx
import { useEffect, useRef, useCallback } from 'react';
import { useNavigate } from 'react-router-dom';
import { GitBranch, ZoomIn, ZoomOut, Maximize2, RefreshCw } from 'lucide-react';
import { useQuery } from '@tanstack/react-query';
import { API_URL, SDK_TOKEN } from '../../config.js';

const BLAST_COLOR: Record<string, string> = {
  HIGH:    '#f87171',
  MEDIUM:  '#fbbf24',
  LOW:     '#4ade80',
  BLOCKED: '#a78bfa',
};
const DEFAULT_COLOR = '#4ade80';

interface FlagNode {
  id: string;
  name: string;
  blast_radius?: string;
}

interface GraphData {
  nodes: FlagNode[];
  links: { source: string; target: string }[];
}

const hdrs = { Authorization: `Bearer ${SDK_TOKEN}` };

// Parse a hex color string like '#f87171' → [r, g, b, a] in 0-1 range
function hexToRgba(hex: string): [number, number, number, number] {
  const clean = hex.replace('#', '');
  const r = parseInt(clean.slice(0, 2), 16) / 255;
  const g = parseInt(clean.slice(2, 4), 16) / 255;
  const b = parseInt(clean.slice(4, 6), 16) / 255;
  return [r, g, b, 1.0];
}

// Build Float32Arrays expected by Cosmos.gl v2 API
function buildCosmosData(nodes: FlagNode[], links: { source: string; target: string }[]) {
  const n = nodes.length;
  const idToIndex = new Map(nodes.map((node, i) => [node.id, i]));

  // Random initial positions spread over a [-1, 1] grid
  const pointPositions = new Float32Array(n * 2);
  for (let i = 0; i < n; i++) {
    pointPositions[i * 2]     = (Math.random() - 0.5) * 2;
    pointPositions[i * 2 + 1] = (Math.random() - 0.5) * 2;
  }

  // RGBA per point
  const pointColors = new Float32Array(n * 4);
  for (let i = 0; i < n; i++) {
    const hex = BLAST_COLOR[nodes[i].blast_radius ?? 'LOW'] ?? DEFAULT_COLOR;
    const [r, g, b, a] = hexToRgba(hex);
    pointColors[i * 4]     = r;
    pointColors[i * 4 + 1] = g;
    pointColors[i * 4 + 2] = b;
    pointColors[i * 4 + 3] = a;
  }

  // Point sizes (all same)
  const pointSizes = new Float32Array(n).fill(5);

  // Links as [sourceIdx, targetIdx, ...]
  const validLinks = links.filter(
    l => idToIndex.has(l.source) && idToIndex.has(l.target)
  );
  const linkArray = new Float32Array(validLinks.length * 2);
  for (let i = 0; i < validLinks.length; i++) {
    linkArray[i * 2]     = idToIndex.get(validLinks[i].source)!;
    linkArray[i * 2 + 1] = idToIndex.get(validLinks[i].target)!;
  }

  return { pointPositions, pointColors, pointSizes, linkArray, idToIndex };
}

export default function DependencyGraph() {
  const navigate = useNavigate();
  const canvasRef = useRef<HTMLDivElement>(null);
  const cosmosRef = useRef<unknown>(null);
  // Keep a stable reference to the nodes array so onClick can look up by index
  const nodesRef = useRef<FlagNode[]>([]);

  const { data: graphData, isLoading, refetch } = useQuery({
    queryKey: ['graph', 'flags'],
    queryFn: async (): Promise<GraphData> => {
      const r = await fetch(`${API_URL}/api/v1/flags`, { headers: hdrs });
      if (!r.ok) throw new Error('Failed to fetch flags');
      const d = await r.json() as {
        flags?: Array<{ key: string; name: string; flag_type: string; prerequisite_flags?: string[] }>;
      };
      const flags = d.flags ?? [];
      return {
        nodes: flags.map(f => ({ id: f.key, name: f.name || f.key })),
        links: flags.flatMap(f =>
          (f.prerequisite_flags ?? []).map(dep => ({ source: f.key, target: dep }))
        ),
      };
    },
    // Fallback demo data shown while waiting for API
    placeholderData: {
      nodes: [
        { id: 'auth-v2',      name: 'Auth V2',     blast_radius: 'HIGH' },
        { id: 'new-checkout', name: 'New Checkout', blast_radius: 'MEDIUM' },
        { id: 'dark-mode',    name: 'Dark Mode',    blast_radius: 'LOW' },
        { id: 'feature-x',   name: 'Feature X',    blast_radius: 'LOW' },
      ],
      links: [
        { source: 'new-checkout', target: 'auth-v2' },
        { source: 'feature-x',   target: 'new-checkout' },
      ],
    },
  });

  // Init (or reinit) Cosmos.gl whenever data changes
  useEffect(() => {
    if (!canvasRef.current || !graphData || graphData.nodes.length === 0) return;

    let cancelled = false;

    import('@cosmograph/cosmos').then(mod => {
      if (cancelled || !canvasRef.current) return;

      // Destroy previous instance before creating a new one
      const prev = cosmosRef.current as { destroy?: () => void } | null;
      prev?.destroy?.();
      cosmosRef.current = null;

      nodesRef.current = graphData.nodes;
      const { pointPositions, pointColors, pointSizes, linkArray } =
        buildCosmosData(graphData.nodes, graphData.links);

      // Cosmos.gl v3 constructor: Graph(div, config)
      // TypeScript types are accurate in beta — cast via unknown to allow our ref type
      const GraphClass = mod.Graph as unknown as new (
        div: HTMLDivElement,
        config: Record<string, unknown>
      ) => {
        setPointPositions: (pos: Float32Array) => void;
        setPointColors: (colors: Float32Array) => void;
        setPointSizes: (sizes: Float32Array) => void;
        setLinks: (links: Float32Array) => void;
        render: () => void;
        fitView: (duration?: number) => void;
        zoom: (value: number, duration?: number) => void;
        pause: () => void;
        destroy: () => void;
      };

      const cosmos = new GraphClass(canvasRef.current, {
        backgroundColor: '#07080d',
        pointDefaultSize: 5,
        linkDefaultColor: '#1f2433',
        linkDefaultWidth: 1,
        simulationRepulsion: 0.5,
        simulationLinkDistance: 80,
        simulationGravity: 0.1,
        fitViewDelay: 300,
        fitViewPadding: 0.2,
        enableDrag: true,
        hoveredPointCursor: 'pointer',
        renderHoveredPointRing: true,
        // onClick receives point index (number | undefined) in Cosmos v2
        onClick: (index: number | undefined) => {
          if (index != null) {
            const node = nodesRef.current[index];
            if (node) navigate(`/flags/${node.id}`);
          }
        },
      });

      cosmos.setPointPositions(pointPositions);
      cosmos.setPointColors(pointColors);
      cosmos.setPointSizes(pointSizes);
      cosmos.setLinks(linkArray);
      cosmos.render();

      cosmosRef.current = cosmos;
    });

    return () => {
      cancelled = true;
      const cosmos = cosmosRef.current as { destroy?: () => void } | null;
      cosmos?.destroy?.();
      cosmosRef.current = null;
    };
  }, [graphData, navigate]);

  const handleZoomIn  = useCallback(() => {
    (cosmosRef.current as { zoom?: (v: number) => void } | null)?.zoom?.(3);
  }, []);

  const handleZoomOut = useCallback(() => {
    (cosmosRef.current as { zoom?: (v: number) => void } | null)?.zoom?.(0.5);
  }, []);

  const handleFit = useCallback(() => {
    (cosmosRef.current as { fitView?: () => void } | null)?.fitView?.();
  }, []);

  return (
    <div style={{
      padding: '24px 32px',
      display: 'flex',
      flexDirection: 'column',
      height: 'calc(100vh - 60px)',
      gap: 16,
    }}>

      {/* Header */}
      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
        <div>
          <h1 style={{ fontSize: 24, fontWeight: 700, color: 'var(--color-fg)', margin: '0 0 4px' }}>
            Causal Graph
          </h1>
          <p style={{ fontSize: 13, color: 'var(--color-fg-subtle)', margin: 0 }}>
            {isLoading
              ? 'Loading…'
              : `${graphData?.nodes.length ?? 0} flags · ${graphData?.links.length ?? 0} dependencies`}
          </p>
        </div>
        <div style={{ display: 'flex', gap: 8 }}>
          {[
            { icon: ZoomIn,    label: 'Zoom in',  fn: handleZoomIn },
            { icon: ZoomOut,   label: 'Zoom out', fn: handleZoomOut },
            { icon: Maximize2, label: 'Fit',      fn: handleFit },
            { icon: RefreshCw, label: 'Refresh',  fn: () => void refetch() },
          ].map(({ icon: Icon, label, fn }) => (
            <button
              key={label}
              onClick={fn}
              title={label}
              style={{
                width: 36,
                height: 36,
                borderRadius: 8,
                display: 'flex',
                alignItems: 'center',
                justifyContent: 'center',
                background: 'var(--color-bg-elevated)',
                border: '1px solid var(--color-border)',
                color: 'var(--color-fg-muted)',
                cursor: 'pointer',
              }}
            >
              <Icon size={14} />
            </button>
          ))}
        </div>
      </div>

      {/* Legend */}
      <div style={{ display: 'flex', gap: 16 }}>
        {Object.entries(BLAST_COLOR).map(([label, color]) => (
          <div
            key={label}
            style={{
              display: 'flex',
              alignItems: 'center',
              gap: 6,
              fontSize: 11,
              color: 'var(--color-fg-subtle)',
            }}
          >
            <div style={{ width: 8, height: 8, borderRadius: '50%', background: color }} />
            {label}
          </div>
        ))}
      </div>

      {/* Canvas */}
      <div style={{
        flex: 1,
        borderRadius: 12,
        border: '1px solid var(--color-border)',
        overflow: 'hidden',
        background: '#07080d',
        position: 'relative',
      }}>
        {isLoading && (
          <div style={{
            position: 'absolute',
            inset: 0,
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'center',
            color: 'var(--color-fg-subtle)',
            flexDirection: 'column',
            gap: 12,
          }}>
            <GitBranch size={40} style={{ opacity: 0.3 }} />
            <div style={{ fontSize: 14 }}>Loading dependency graph…</div>
          </div>
        )}
        <div ref={canvasRef} style={{ width: '100%', height: '100%' }} />
      </div>
    </div>
  );
}
