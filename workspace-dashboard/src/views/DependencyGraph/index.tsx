import { useEffect, useRef, useState, useCallback } from 'react';
import { INTEL_URL, API_URL, SDK_TOKEN } from '../../config.js';

type Win = '1h' | '6h' | '24h' | '7d';
const WIN_SEC: Record<Win, number> = { '1h': 3600, '6h': 21600, '24h': 86400, '7d': 604800 };

interface GNode { flagKey: string; enabled: boolean; rolloutPct: number; state: string; ownerId: string; x?: number; y?: number; vx?: number; vy?: number; fx?: number | null; fy?: number | null; }
interface GEdge { source: string | GNode; target: string | GNode; weight: number; coChangeCount: number; }
interface Graph { nodes: GNode[]; edges: GEdge[]; generatedAt: number; eventCount: number; }

export default function DependencyGraph() {
  const svgRef = useRef<SVGSVGElement>(null);
  const simRef = useRef<unknown>(null);
  const [graph, setGraph] = useState<Graph | null>(null);
  const [selected, setSelected] = useState<GNode | null>(null);
  const [win, setWin] = useState<Win>('6h');
  const [env, setEnv] = useState('production');
  const [loading, setLoading] = useState(false);
  const [killing, setKilling] = useState(false);

  const INTEL = INTEL_URL;
  const API = API_URL;
  const TOK = SDK_TOKEN;

  const fetchGraph = useCallback(async () => {
    setLoading(true);
    try {
      const now = Math.floor(Date.now() / 1000);
      const from = now - WIN_SEC[win];
      const r = await fetch(
        INTEL + '/api/v1/dependency-graph?environment=' + env + '&from_unix=' + from + '&to_unix=' + now,
        { method: 'POST' }
      );
      if (r.ok) setGraph(await r.json());
    } catch (e) { console.error(e); }
    finally { setLoading(false); }
  }, [win, env]);

  useEffect(() => { fetchGraph(); }, [fetchGraph]);

  useEffect(() => {
    if (!graph || !svgRef.current || !graph.nodes.length) return;
    import('d3').then(d3 => {
      const el = svgRef.current!;
      const W = el.parentElement?.clientWidth || 900;
      const H = el.parentElement?.clientHeight || 600;
      const svg = d3.select(el).attr('width', W).attr('height', H);
      svg.selectAll('*').remove();
      const g = svg.append('g');
      svg.call(
        d3.zoom<SVGSVGElement, unknown>()
          .scaleExtent([0.2, 4])
          .on('zoom', e => g.attr('transform', e.transform))
      );

      const nodes: GNode[] = graph.nodes.map(n => ({ ...n }));
      const links: GEdge[] = graph.edges.map(e => ({ ...e }));

      const sim = d3.forceSimulation(nodes as d3.SimulationNodeDatum[])
        .force('link', d3.forceLink(links).id((d: d3.SimulationNodeDatum) => (d as GNode).flagKey).distance(130))
        .force('charge', d3.forceManyBody().strength(-250))
        .force('center', d3.forceCenter(W / 2, H / 2))
        .force('collide', d3.forceCollide(30));
      simRef.current = sim;

      const link = g.append('g').selectAll('line').data(links).join('line')
        .attr('stroke', '#30363d')
        .attr('stroke-width', (d: GEdge) => Math.min(4, d.coChangeCount + 1))
        .attr('stroke-opacity', (d: GEdge) => 0.15 + d.weight * 0.75);

      const drag = d3.drag<SVGGElement, GNode>()
        .on('start', (e, d) => { if (!e.active) sim.alphaTarget(0.3).restart(); d.fx = d.x; d.fy = d.y; })
        .on('drag', (e, d) => { d.fx = e.x; d.fy = e.y; })
        .on('end', (e, d) => { if (!e.active) sim.alphaTarget(0); d.fx = null; d.fy = null; });

      const node = g.append('g').selectAll<SVGGElement, GNode>('g').data(nodes).join('g')
        .attr('cursor', 'pointer')
        .call(drag)
        .on('click', (_, d) => setSelected(d));

      node.append('circle').attr('r', 16)
        .attr('fill', (d: GNode) => d.enabled ? '#1a3520' : '#1a1a22')
        .attr('stroke', (d: GNode) => d.enabled ? '#3fb950' : '#30363d')
        .attr('stroke-width', 2);

      node.append('text').attr('dy', 30).attr('text-anchor', 'middle')
        .attr('font-size', '9px').attr('fill', '#6e7681')
        .text((d: GNode) => { const p = d.flagKey.split('.'); return p[p.length - 1].slice(0, 12); });

      sim.on('tick', () => {
        link
          .attr('x1', (d: GEdge) => (d.source as GNode).x || 0)
          .attr('y1', (d: GEdge) => (d.source as GNode).y || 0)
          .attr('x2', (d: GEdge) => (d.target as GNode).x || 0)
          .attr('y2', (d: GEdge) => (d.target as GNode).y || 0);
        node.attr('transform', (d: GNode) => 'translate(' + (d.x || 0) + ',' + (d.y || 0) + ')');
      });
    });
    return () => {
      if (simRef.current) (simRef.current as { stop: () => void }).stop();
    };
  }, [graph]);

  const killFlag = async (flagKey: string) => {
    setKilling(true);
    try {
      await fetch(API + '/api/v1/flags/' + flagKey + '/kill', {
        method: 'POST',
        headers: { 'Authorization': 'Bearer ' + TOK, 'Content-Type': 'application/json' },
        body: JSON.stringify({ environment: env, reason: 'kill from dependency graph' }),
      });
      setSelected(null);
      fetchGraph();
    } finally { setKilling(false); }
  };

  const btnStyle = (active: boolean, col: string): React.CSSProperties => ({
    padding: '6px 13px', borderRadius: 6, fontSize: 12, cursor: 'pointer',
    border: '1px solid ' + (active ? col : '#21262d'),
    background: active ? col + '18' : 'transparent',
    color: active ? col : '#6e7681', transition: 'all 0.15s',
  });

  return (
    <div style={{ height: '100%', display: 'flex', flexDirection: 'column', background: '#080c14' }}>
      <div style={{ padding: '20px 28px 14px', borderBottom: '1px solid #21262d' }}>
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start' }}>
          <div>
            <h1 style={{ fontSize: 22, fontWeight: 700, color: '#e6edf3', margin: '0 0 4px' }}>Causal Dependency Graph</h1>
            <p style={{ fontSize: 13, color: '#6e7681', margin: 0 }}>
              Flags changed together within 5 min — thicker edges = stronger coupling
              {graph ? ' · ' + graph.nodes.length + ' nodes · ' + graph.edges.length + ' edges · ' + graph.eventCount + ' events' : ''}
            </p>
          </div>
          <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap', justifyContent: 'flex-end' }}>
            {(['development', 'staging', 'production'] as const).map(e =>
              <button key={e} onClick={() => setEnv(e)} style={btnStyle(env === e, '#58a6ff')}>{e}</button>
            )}
            <div style={{ width: 1, background: '#21262d', margin: '0 3px' }} />
            {(['1h', '6h', '24h', '7d'] as Win[]).map(w =>
              <button key={w} onClick={() => setWin(w)} style={btnStyle(win === w, '#3fb950')}>{w}</button>
            )}
            <button onClick={fetchGraph} style={btnStyle(false, '#58a6ff')}>↻</button>
          </div>
        </div>
      </div>

      <div style={{ flex: 1, position: 'relative', overflow: 'hidden' }}>
        {loading && (
          <div style={{ position: 'absolute', top: '50%', left: '50%', transform: 'translate(-50%,-50%)', color: '#484f58' }}>
            Building graph…
          </div>
        )}
        {!loading && graph && !graph.nodes.length && (
          <div style={{ position: 'absolute', top: '50%', left: '50%', transform: 'translate(-50%,-50%)', color: '#484f58', textAlign: 'center' }}>
            <div style={{ fontSize: 40, marginBottom: 8 }}>◎</div>
            No co-occurrences in this window.<br />
            <span style={{ fontSize: 12 }}>Try a wider time range or make some flag changes first.</span>
          </div>
        )}
        <svg ref={svgRef} style={{ width: '100%', height: '100%', display: 'block' }} />

        {selected && (
          <div style={{ position: 'absolute', top: 16, right: 16, width: 256, background: '#0d1117', border: '1px solid #21262d', borderRadius: 10, padding: 16 }}>
            <div style={{ display: 'flex', justifyContent: 'space-between', marginBottom: 12 }}>
              <code style={{ fontSize: 11, color: '#58a6ff', wordBreak: 'break-all', maxWidth: 200 }}>{selected.flagKey}</code>
              <button onClick={() => setSelected(null)} style={{ background: 'none', border: 'none', color: '#484f58', cursor: 'pointer', fontSize: 18, padding: 0 }}>×</button>
            </div>
            <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 8, marginBottom: 12 }}>
              {[
                { l: 'Status', v: selected.enabled ? 'ENABLED' : 'DISABLED', c: selected.enabled ? '#3fb950' : '#484f58' },
                { l: 'Rollout', v: selected.rolloutPct + '%', c: '#e6edf3' },
                { l: 'State', v: selected.state, c: '#8b949e' },
                { l: 'Owner', v: (selected.ownerId || '').split('@')[0], c: '#8b949e' },
              ].map(s => (
                <div key={s.l} style={{ background: '#161b22', borderRadius: 6, padding: '7px 10px' }}>
                  <div style={{ fontSize: 10, color: '#484f58', marginBottom: 2 }}>{s.l}</div>
                  <div style={{ fontSize: 12, fontWeight: 600, color: s.c, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{s.v}</div>
                </div>
              ))}
            </div>
            {selected.enabled && (
              <button
                onClick={() => killFlag(selected.flagKey)}
                disabled={killing}
                style={{
                  width: '100%', padding: 8, borderRadius: 6, fontSize: 12, fontWeight: 600,
                  background: '#300', border: '1px solid #611', color: '#ff7b7b', cursor: 'pointer',
                }}
              >
                {killing ? 'Disabling…' : '⚡ Kill Switch — ' + env}
              </button>
            )}
          </div>
        )}

        <div style={{ position: 'absolute', bottom: 16, left: 16, background: '#0d1117', border: '1px solid #21262d', borderRadius: 8, padding: '8px 14px', display: 'flex', gap: 14, fontSize: 11, color: '#6e7681' }}>
          {[{ c: '#3fb950', l: 'Enabled' }, { c: '#484f58', l: 'Disabled' }].map(i =>
            <div key={i.l} style={{ display: 'flex', alignItems: 'center', gap: 5 }}>
              <div style={{ width: 8, height: 8, borderRadius: '50%', background: i.c }} />{i.l}
            </div>
          )}
          <span style={{ borderLeft: '1px solid #21262d', paddingLeft: 12 }}>Thicker = more co-changes</span>
        </div>
      </div>
    </div>
  );
}
