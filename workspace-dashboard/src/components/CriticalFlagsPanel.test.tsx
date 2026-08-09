import { describe, it, expect, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import { BrowserRouter } from 'react-router-dom';
import { CriticalFlagsPanel } from './CriticalFlagsPanel.js';

global.fetch = vi.fn();

const fixtureAuthValue = 'fixture-auth-value';

describe('CriticalFlagsPanel', () => {
  it('displays loading state initially', () => {
    (global.fetch as any).mockResolvedValueOnce({
      json: async () => ({ flags: [], generated_at: 1234567890 }),
    });

    render(
      <BrowserRouter>
        <CriticalFlagsPanel apiUrl="http://localhost:8083" token={fixtureAuthValue} limit={10} />
      </BrowserRouter>
    );

    expect(screen.getByText(/loading/i)).toBeInTheDocument();
  });

  it('renders critical flags sorted by score', async () => {
    (global.fetch as any).mockResolvedValueOnce({
      json: async () => ({
        flags: [
          { key: 'payments.checkout', score: 25.6, blast_radius_tier: 'BLOCKED', in_degree: 5, out_degree: 3, avg_edge_weight: 0.7 },
          { key: 'auth.sso', score: 18.0, blast_radius_tier: 'HIGH', in_degree: 8, out_degree: 2, avg_edge_weight: 0.6 },
        ],
        generated_at: 1234567890,
      }),
    });

    render(
      <BrowserRouter>
        <CriticalFlagsPanel apiUrl="http://localhost:8083" token={fixtureAuthValue} limit={10} />
      </BrowserRouter>
    );

    await waitFor(() => {
      expect(screen.getByText('payments.checkout')).toBeInTheDocument();
      expect(screen.getByText('auth.sso')).toBeInTheDocument();
      expect(screen.getByText('25.60')).toBeInTheDocument();
    });
  });
});
