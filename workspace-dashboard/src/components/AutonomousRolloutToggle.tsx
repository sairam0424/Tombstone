import { useState, useEffect, useRef } from 'react';

interface PosteriorData {
  alpha: number;
  beta: number;
  total_observations: number;
  autonomous_enabled: boolean;
  current_rollout_pct: number;
}

interface Recommendation {
  flag_key: string;
  environment: string;
  current_pct: number;
  recommended_pct: number;
  confidence: number;
  reason: string;
  should_advance: boolean;
}

interface Props {
  flagKey: string;
  environment: string;
  currentRolloutPct: number;
}

const INTEL_URL = 'http://localhost:8083';

export function AutonomousRolloutToggle({ flagKey, environment, currentRolloutPct }: Props) {
  const [posterior, setPosterior] = useState<PosteriorData | null>(null);
  const [recommendation, setRecommendation] = useState<Recommendation | null>(null);
  const [toggling, setToggling] = useState(false);
  const [applying, setApplying] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const pollRef = useRef<ReturnType<typeof setInterval> | null>(null);

  const fetchPosterior = async () => {
    try {
      const res = await fetch(
        `${INTEL_URL}/api/v1/rollout/posterior/${flagKey}?environment=${environment}`
      );
      if (!res.ok) return;
      const data = (await res.json()) as PosteriorData;
      setPosterior(data);
    } catch {
      // silently ignore network errors
    }
  };

  const fetchRecommendation = async () => {
    try {
      const res = await fetch(`${INTEL_URL}/api/v1/rollout/recommendations`);
      if (!res.ok) return;
      const data = (await res.json()) as { recommendations?: Recommendation[] };
      const match = data.recommendations?.find(
        r => r.flag_key === flagKey && r.environment === environment
      );
      setRecommendation(match ?? null);
    } catch {
      // silently ignore
    }
  };

  useEffect(() => {
    void fetchPosterior();
  }, [flagKey, environment]);

  useEffect(() => {
    if (posterior?.autonomous_enabled) {
      void fetchRecommendation();
      pollRef.current = setInterval(() => {
        void fetchRecommendation();
      }, 30_000);
    } else {
      setRecommendation(null);
      if (pollRef.current) {
        clearInterval(pollRef.current);
        pollRef.current = null;
      }
    }
    return () => {
      if (pollRef.current) {
        clearInterval(pollRef.current);
        pollRef.current = null;
      }
    };
  }, [posterior?.autonomous_enabled]);

  const handleToggle = async () => {
    if (!posterior) return;
    setToggling(true);
    setError(null);
    const endpoint = posterior.autonomous_enabled
      ? '/api/v1/rollout/disable'
      : '/api/v1/rollout/enable';
    try {
      const res = await fetch(`${INTEL_URL}${endpoint}`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ flag_key: flagKey, environment }),
      });
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      await fetchPosterior();
    } catch (e) {
      setError(String(e));
    } finally {
      setToggling(false);
    }
  };

  const handleApply = async () => {
    if (!recommendation) return;
    setApplying(true);
    setError(null);
    try {
      const res = await fetch(`${INTEL_URL}/api/v1/rollout/update`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          flag_key: flagKey,
          environment,
          rollout_pct: recommendation.recommended_pct,
        }),
      });
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      await fetchPosterior();
      await fetchRecommendation();
    } catch (e) {
      setError(String(e));
    } finally {
      setApplying(false);
    }
  };

  // Hide when fully rolled out
  if (currentRolloutPct >= 100) return null;
  if (!posterior) return null;

  const confidencePct = posterior.total_observations > 0
    ? Math.round((posterior.alpha / (posterior.alpha + posterior.beta)) * 100)
    : 0;

  const styles = {
    container: {
      background: '#0d1117',
      border: '1px solid #21262d',
      borderRadius: '8px',
      padding: '16px',
      marginTop: '16px',
    } as React.CSSProperties,
    toggleRow: {
      display: 'flex',
      alignItems: 'center',
      justifyContent: 'space-between',
    } as React.CSSProperties,
    label: {
      color: '#e6edf3',
      fontSize: '13px',
      fontWeight: 600,
      display: 'flex',
      alignItems: 'center',
      gap: '8px',
    } as React.CSSProperties,
    badge: {
      display: 'inline-block',
      padding: '2px 8px',
      borderRadius: '4px',
      fontSize: '11px',
      fontWeight: 600,
    } as React.CSSProperties,
    badgeEnabled: {
      background: '#1a3a1a',
      color: '#3fb950',
      border: '1px solid #2ea043',
    } as React.CSSProperties,
    badgeDisabled: {
      background: '#1c1c1c',
      color: '#8b949e',
      border: '1px solid #30363d',
    } as React.CSSProperties,
    button: {
      padding: '6px 14px',
      borderRadius: '6px',
      fontSize: '12px',
      fontWeight: 600,
      cursor: 'pointer',
      border: 'none',
      transition: 'background 0.15s',
    } as React.CSSProperties,
    enableButton: {
      background: '#238636',
      color: '#e6edf3',
    } as React.CSSProperties,
    disableButton: {
      background: '#21262d',
      color: '#8b949e',
    } as React.CSSProperties,
    disabledButton: {
      opacity: 0.5,
      cursor: 'not-allowed',
    } as React.CSSProperties,
    recommendationCard: {
      background: '#161b22',
      border: '1px solid #21262d',
      borderRadius: '6px',
      padding: '12px',
      marginTop: '12px',
    } as React.CSSProperties,
    recTitle: {
      color: '#8b949e',
      fontSize: '11px',
      fontWeight: 600,
      letterSpacing: '0.05em',
      textTransform: 'uppercase' as const,
      marginBottom: '8px',
    } as React.CSSProperties,
    recGrid: {
      display: 'grid',
      gridTemplateColumns: '1fr 1fr 1fr',
      gap: '8px',
      marginBottom: '10px',
    } as React.CSSProperties,
    recCell: {
      background: '#0d1117',
      borderRadius: '4px',
      padding: '8px 10px',
    } as React.CSSProperties,
    recCellLabel: {
      color: '#6e7681',
      fontSize: '10px',
      marginBottom: '2px',
    } as React.CSSProperties,
    recCellValue: {
      color: '#e6edf3',
      fontSize: '14px',
      fontWeight: 700,
    } as React.CSSProperties,
    reasonText: {
      color: '#8b949e',
      fontSize: '12px',
      marginBottom: '10px',
      lineHeight: '1.5',
    } as React.CSSProperties,
    applyButton: {
      background: '#1f6feb',
      color: '#e6edf3',
      padding: '6px 14px',
      borderRadius: '6px',
      fontSize: '12px',
      fontWeight: 600,
      cursor: 'pointer',
      border: 'none',
    } as React.CSSProperties,
    errorText: {
      color: '#f85149',
      fontSize: '11px',
      marginTop: '8px',
    } as React.CSSProperties,
    divider: {
      borderTop: '1px solid #21262d',
      marginTop: '12px',
      paddingTop: '12px',
    } as React.CSSProperties,
    observationsText: {
      color: '#6e7681',
      fontSize: '11px',
      marginTop: '8px',
    } as React.CSSProperties,
  };

  return (
    <div style={styles.container}>
      <div style={styles.toggleRow}>
        <span style={styles.label}>
          Autonomous Rollout
          <span style={{ ...styles.badge, ...(posterior.autonomous_enabled ? styles.badgeEnabled : styles.badgeDisabled) }}>
            {posterior.autonomous_enabled ? 'ENABLED' : 'DISABLED'}
          </span>
        </span>
        <button
          onClick={() => void handleToggle()}
          disabled={toggling}
          style={{
            ...styles.button,
            ...(posterior.autonomous_enabled ? styles.disableButton : styles.enableButton),
            ...(toggling ? styles.disabledButton : {}),
          }}
        >
          {toggling
            ? 'Updating…'
            : posterior.autonomous_enabled
            ? 'Disable'
            : 'Enable'}
        </button>
      </div>

      <div style={styles.observationsText}>
        {posterior.total_observations} observations · alpha {posterior.alpha.toFixed(2)} · beta {posterior.beta.toFixed(2)}
      </div>

      {posterior.autonomous_enabled && (
        <div style={styles.divider}>
          {recommendation ? (
            <div style={styles.recommendationCard}>
              <div style={styles.recTitle}>Recommendation</div>
              <div style={styles.recGrid}>
                <div style={styles.recCell}>
                  <div style={styles.recCellLabel}>Confidence</div>
                  <div style={{ ...styles.recCellValue, color: confidencePct >= 80 ? '#3fb950' : confidencePct >= 60 ? '#d29922' : '#f85149' }}>
                    {confidencePct}%
                  </div>
                </div>
                <div style={styles.recCell}>
                  <div style={styles.recCellLabel}>Current %</div>
                  <div style={styles.recCellValue}>{recommendation.current_pct}%</div>
                </div>
                <div style={styles.recCell}>
                  <div style={styles.recCellLabel}>Suggested %</div>
                  <div style={{ ...styles.recCellValue, color: '#58a6ff' }}>{recommendation.recommended_pct}%</div>
                </div>
              </div>
              {recommendation.reason && (
                <div style={styles.reasonText}>{recommendation.reason}</div>
              )}
              {recommendation.should_advance && (
                <button
                  onClick={() => void handleApply()}
                  disabled={applying}
                  style={{
                    ...styles.applyButton,
                    ...(applying ? styles.disabledButton : {}),
                  }}
                >
                  {applying ? 'Applying…' : 'Apply Recommendation'}
                </button>
              )}
              {!recommendation.should_advance && (
                <div style={{ color: '#6e7681', fontSize: '11px' }}>
                  No advance recommended at this time.
                </div>
              )}
            </div>
          ) : (
            <div style={{ color: '#6e7681', fontSize: '12px' }}>
              Fetching recommendation…
            </div>
          )}
        </div>
      )}

      {error && <div style={styles.errorText}>Error: {error}</div>}
    </div>
  );
}
