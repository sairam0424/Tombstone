package syncer

import (
    "bytes"
    "context"
    "encoding/json"
    "fmt"
    "net/http"
    "strings"
    "time"

    "github.com/tombstone/gitops-sync/internal/parser"
    "go.uber.org/zap"
)

type SyncResult struct {
    Created []string
    Updated []string
    Skipped []string
    Errors  []string
}

type Syncer struct {
    flagAPIURL  string
    apiToken    string
    httpClient  *http.Client
    logger      *zap.Logger
}

func NewSyncer(flagAPIURL, apiToken string, logger *zap.Logger) *Syncer {
    return &Syncer{
        flagAPIURL: flagAPIURL,
        apiToken:   apiToken,
        httpClient: &http.Client{Timeout: 10 * time.Second},
        logger:     logger,
    }
}

// Sync reconciles a slice of flag definitions with the live flag-api.
// For each definition:
//   - If the flag doesn't exist: create it
//   - If it exists: update environment states
//   - Never deletes flags (deletion requires explicit archiving)
func (s *Syncer) Sync(ctx context.Context, defs []*parser.FlagDefinition, projectID string) SyncResult {
    result := SyncResult{}

    for _, def := range defs {
        // Check if flag exists
        exists, err := s.flagExists(ctx, def.Spec.Key)
        if err != nil {
            result.Errors = append(result.Errors, fmt.Sprintf("%s: check failed: %v", def.Spec.Key, err))
            continue
        }

        if !exists {
            if err := s.createFlag(ctx, def, projectID); err != nil {
                result.Errors = append(result.Errors, fmt.Sprintf("%s: create failed: %v", def.Spec.Key, err))
                continue
            }
            result.Created = append(result.Created, def.Spec.Key)
            s.logger.Info("created flag", zap.String("key", def.Spec.Key))
        }

        // Sync environment states
        for env, envSpec := range def.Spec.Environments {
            if err := s.updateEnvironment(ctx, def.Spec.Key, env, envSpec.Enabled, envSpec.RolloutPct); err != nil {
                result.Errors = append(result.Errors, fmt.Sprintf("%s[%s]: update failed: %v", def.Spec.Key, env, err))
                continue
            }
        }

        if exists {
            result.Updated = append(result.Updated, def.Spec.Key)
        }
    }

    return result
}

func (s *Syncer) flagExists(ctx context.Context, key string) (bool, error) {
    req, _ := http.NewRequestWithContext(ctx, http.MethodGet,
        s.flagAPIURL+"/api/v1/flags/"+key, nil)
    req.Header.Set("Authorization", "Bearer "+s.apiToken)
    resp, err := s.httpClient.Do(req)
    if err != nil {
        return false, err
    }
    defer resp.Body.Close()
    return resp.StatusCode == http.StatusOK, nil
}

func (s *Syncer) createFlag(ctx context.Context, def *parser.FlagDefinition, projectID string) error {
    body, _ := json.Marshal(map[string]any{
        "key":          def.Spec.Key,
        "name":         def.Metadata.Name,
        "description":  def.Metadata.Description,
        "flag_type":    strings.ToUpper(def.Spec.Type),
        "owner_id":     def.Metadata.Owner,
        "project_id":   projectID,
        "safe_default": def.Spec.SafeDefault,
    })
    req, _ := http.NewRequestWithContext(ctx, http.MethodPost,
        s.flagAPIURL+"/api/v1/flags", bytes.NewReader(body))
    req.Header.Set("Authorization", "Bearer "+s.apiToken)
    req.Header.Set("Content-Type", "application/json")
    resp, err := s.httpClient.Do(req)
    if err != nil {
        return err
    }
    defer resp.Body.Close()
    if resp.StatusCode >= 400 {
        return fmt.Errorf("HTTP %d", resp.StatusCode)
    }
    return nil
}

func (s *Syncer) updateEnvironment(ctx context.Context, key, env string, enabled bool, pct int) error {
    body, _ := json.Marshal(map[string]any{
        "enabled":     enabled,
        "rollout_pct": pct,
        "updated_by":  "gitops-sync",
    })
    req, _ := http.NewRequestWithContext(ctx, http.MethodPatch,
        fmt.Sprintf("%s/api/v1/flags/%s/environments/%s", s.flagAPIURL, key, env),
        bytes.NewReader(body))
    req.Header.Set("Authorization", "Bearer "+s.apiToken)
    req.Header.Set("Content-Type", "application/json")
    resp, err := s.httpClient.Do(req)
    if err != nil {
        return err
    }
    defer resp.Body.Close()
    if resp.StatusCode >= 400 {
        return fmt.Errorf("HTTP %d", resp.StatusCode)
    }
    return nil
}
