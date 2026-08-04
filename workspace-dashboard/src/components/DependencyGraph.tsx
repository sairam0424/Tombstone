import { useEffect, useRef, useState } from 'react';
import * as d3 from 'd3';

interface Node {
  key: string;
  enabled: boolean;
  rollout_pct: number;
  x?: number;
  y?: number;
  vx?: number;
  vy?: number;
}

interface Edge {
  source: string | Node;
  target: string | Node;
  weight: number;
}

interface DependencyGraphProps {
  nodes: Node[];
  edges: Edge[];
  width: number;
  height: number;
  onNodeClick?: (key: string) => void;
  highlightedSubgraph?: Set<string>;
  disableSimulation?: boolean;
}

export function DependencyGraph({
  nodes,
  edges,
  width,
  height,
  onNodeClick,
  highlightedSubgraph,
  disableSimulation = false,
}: DependencyGraphProps) {
  const canvasRef = useRef<HTMLCanvasElement>(null);
  const [hoveredEdge, setHoveredEdge] = useState<string | null>(null);

  useEffect(() => {
    if (!canvasRef.current || nodes.length === 0) return;

    const canvas = canvasRef.current;
    const context = canvas.getContext('2d');
    if (!context) return;

    // Clone nodes and edges to avoid mutating props
    const nodesCopy = nodes.map(n => ({ ...n }));
    const edgesCopy = edges.map(e => ({ ...e }));

    // d3-force simulation
    const simulation = d3.forceSimulation(nodesCopy)
      .force('link', d3.forceLink(edgesCopy).id((d: any) => d.key).distance(100))
      .force('charge', d3.forceManyBody().strength(-300))
      .force('center', d3.forceCenter(width / 2, height / 2))
      .on('tick', () => {
        context.clearRect(0, 0, width, height);

        // Draw edges
        context.strokeStyle = '#94a3b8';
        context.lineWidth = 1;
        edgesCopy.forEach(edge => {
          const source = edge.source as Node;
          const target = edge.target as Node;
          const isHighlighted = highlightedSubgraph?.has(source.key) && highlightedSubgraph?.has(target.key);

          context.strokeStyle = isHighlighted ? '#22c55e' : '#94a3b8';
          context.lineWidth = isHighlighted ? 2 : 1;
          context.beginPath();
          context.moveTo(source.x!, source.y!);
          context.lineTo(target.x!, target.y!);
          context.stroke();
        });

        // Draw nodes
        nodesCopy.forEach(node => {
          const isHighlighted = highlightedSubgraph?.has(node.key);
          const isEnabled = node.enabled;

          context.fillStyle = isHighlighted ? '#22c55e' : (isEnabled ? '#3b82f6' : '#64748b');
          context.beginPath();
          context.arc(node.x!, node.y!, 8, 0, 2 * Math.PI);
          context.fill();

          // Draw label
          context.fillStyle = '#fff';
          context.font = '10px sans-serif';
          context.textAlign = 'center';
          context.fillText(node.key, node.x!, node.y! + 20);
        });
      });

    if (disableSimulation) {
      simulation.stop();
    }

    // Click handler
    const handleClick = (event: MouseEvent) => {
      const rect = canvas.getBoundingClientRect();
      const x = event.clientX - rect.left;
      const y = event.clientY - rect.top;

      // Find clicked node (within 10px radius)
      const clickedNode = nodesCopy.find(n => {
        const dx = (n.x || 0) - x;
        const dy = (n.y || 0) - y;
        return Math.sqrt(dx * dx + dy * dy) < 10;
      });

      if (clickedNode && onNodeClick) {
        onNodeClick(clickedNode.key);
      }
    };

    canvas.addEventListener('click', handleClick);

    return () => {
      simulation.stop();
      canvas.removeEventListener('click', handleClick);
    };
  }, [nodes, edges, width, height, highlightedSubgraph, disableSimulation, onNodeClick]);

  return (
    <div>
      <canvas ref={canvasRef} width={width} height={height} role="img" aria-label="Dependency graph visualization" />
      <div className="text-sm text-gray-400 mt-2">
        {nodes.length} nodes, {edges.length} {edges.length === 1 ? 'edge' : 'edges'}
      </div>
    </div>
  );
}
