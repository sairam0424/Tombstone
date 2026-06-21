export type FlagState = 'DRAFT' | 'ACTIVE' | 'COMPLETE' | 'ARCHIVED';

export interface FlagListItem {
  id: string;
  key: string;
  name: string;
  description: string;
  state: FlagState;
  ownerId: string;
  enabled: boolean;
  rolloutPct: number;
  environment: string;
  createdAt: number;
  updatedAt: number;
  isStale: boolean;
  isOrphaned: boolean;
}

export interface GraphNode {
  flagKey: string;
  enabled: boolean;
  rolloutPct: number;
  state: string;
  ownerId: string;
}

export interface GraphEdge {
  source: string;
  target: string;
  weight: number;
  coChangeCount: number;
}

export interface CausalGraph {
  nodes: GraphNode[];
  edges: GraphEdge[];
  generatedAt: number;
  eventCount: number;
}
