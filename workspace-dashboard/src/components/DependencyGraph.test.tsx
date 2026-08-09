/// <reference types="@testing-library/jest-dom" />
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen } from '@testing-library/react';
import { DependencyGraph } from './DependencyGraph.js';

// Mock d3-force before importing the component
vi.mock('d3', () => ({
  forceSimulation: vi.fn(() => ({
    nodes: vi.fn().mockReturnThis(),
    force: vi.fn().mockReturnThis(),
    on: vi.fn().mockReturnThis(),
    stop: vi.fn(),
  })),
  forceLink: vi.fn(() => ({
    id: vi.fn().mockReturnThis(),
    distance: vi.fn().mockReturnThis(),
  })),
  forceManyBody: vi.fn(() => ({
    strength: vi.fn().mockReturnThis(),
  })),
  forceCenter: vi.fn(),
}));

describe('DependencyGraph', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('renders a canvas element for the graph', () => {
    const nodes = [
      { key: 'A', enabled: true, rollout_pct: 100 },
      { key: 'B', enabled: false, rollout_pct: 0 },
    ];
    const edges: Array<{ source: string; target: string; weight: number }> = [
      { source: 'A', target: 'B', weight: 0.8 },
    ];

    const { container } = render(
      <DependencyGraph nodes={nodes} edges={edges} width={800} height={600} />
    );

    const canvas = container.querySelector('canvas');
    expect(canvas).toBeTruthy();
  });

  it('displays edge count in the legend', () => {
    const nodes = [{ key: 'A', enabled: true, rollout_pct: 100 }, { key: 'B', enabled: false, rollout_pct: 0 }];
    const edges = [{ source: 'A', target: 'B', weight: 0.8 }];

    render(<DependencyGraph nodes={nodes} edges={edges} width={800} height={600} />);

    expect(screen.getByText(/1 edge/i)).toBeInTheDocument();
  });

  it('calls onNodeClick when a node is clicked', () => {
    const handleClick = vi.fn();
    const nodes = [
      { key: 'A', enabled: true, rollout_pct: 100, x: 400, y: 300 },
    ];
    const edges: Array<{ source: string; target: string; weight: number }> = [];

    const { container } = render(
      <DependencyGraph nodes={nodes} edges={edges} width={800} height={600} onNodeClick={handleClick} />
    );

    const canvas = container.querySelector('canvas') as HTMLCanvasElement;
    expect(canvas).toBeTruthy();

    // Simulate a click event on the canvas at the position where node 'A' is located.
    const clickEvent = new MouseEvent('click', {
      bubbles: true,
      clientX: 400,
      clientY: 300,
    });
    canvas.dispatchEvent(clickEvent);

    // The click handler should detect the node at the clicked coordinates and call the callback.
    // Note: Since d3 is mocked, the simulation doesn't run, but we can still test the click
    // detection logic with pre-set x/y coordinates on the nodes.
    expect(handleClick).toHaveBeenCalledWith('A');
  });
});
