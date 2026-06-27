package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.uber.org/zap"

	"github.com/sairam0424/Tombstone/services/ast-rewriter/internal/rewriter"
	"github.com/sairam0424/Tombstone/services/ast-rewriter/internal/scanner"
	"github.com/sairam0424/Tombstone/services/ast-rewriter/internal/telemetry"
)

// ---- request / response types ------------------------------------------------

type scanRequest struct {
	FlagKey  string `json:"flag_key"`
	RepoPath string `json:"repo_path"`
}

type scanResponse struct {
	CallSites     []scanner.CallSite `json:"call_sites"`
	CallSiteCount int                `json:"call_site_count"`
	Summary       string             `json:"summary"`
}

type rewriteRequest struct {
	FlagKey        string `json:"flag_key"`
	RepoPath       string `json:"repo_path"`
	WinningVariant string `json:"winning_variant"`
	Language       string `json:"language"`
	DryRun         *bool  `json:"dry_run"`
	FullRewrite    bool   `json:"full_rewrite"`
}

type rewriteResponse struct {
	FilesModified    []string `json:"files_modified"`
	LinesRemoved     int      `json:"lines_removed"`
	Diff             string   `json:"diff"`
	CallSitesFound   int      `json:"call_sites_found"`
	Notes            string   `json:"notes"`
	TransformApplied bool     `json:"transform_applied"`
	JscodeShiftUsed  bool     `json:"jscodeshift_used"`
}

// ---- helpers -----------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func decodeJSON(r *http.Request, dst any) error {
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	return d.Decode(dst)
}

// ---- handlers ----------------------------------------------------------------

func healthHandler(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "ok",
		"service": "ast-rewriter",
	})
}

func scanHandler(log *zap.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req scanRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		if req.FlagKey == "" || req.RepoPath == "" {
			writeError(w, http.StatusBadRequest, "flag_key and repo_path are required")
			return
		}

		log.Info("scan request", zap.String("flag_key", req.FlagKey), zap.String("repo_path", req.RepoPath))

		sites, err := scanner.ScanDirectory(req.RepoPath, req.FlagKey)
		if err != nil {
			log.Error("scan failed", zap.Error(err))
			writeError(w, http.StatusInternalServerError, "scan error: "+err.Error())
			return
		}

		if sites == nil {
			sites = []scanner.CallSite{}
		}

		writeJSON(w, http.StatusOK, scanResponse{
			CallSites:     sites,
			CallSiteCount: len(sites),
			Summary:       scanner.Summary(sites),
		})
	}
}

func rewriteHandler(log *zap.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req rewriteRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		if req.FlagKey == "" || req.RepoPath == "" || req.WinningVariant == "" {
			writeError(w, http.StatusBadRequest, "flag_key, repo_path, and winning_variant are required")
			return
		}

		// dry_run defaults to true when not provided.
		dryRun := true
		if req.DryRun != nil {
			dryRun = *req.DryRun
		}

		log.Info("rewrite request",
			zap.String("flag_key", req.FlagKey),
			zap.String("repo_path", req.RepoPath),
			zap.String("winning_variant", req.WinningVariant),
			zap.String("language", req.Language),
			zap.Bool("dry_run", dryRun),
			zap.Bool("full_rewrite", req.FullRewrite),
		)

		var result *rewriter.RewriteResult
		var err error
		jscodeShiftUsed := false

		if req.FullRewrite {
			result, err = rewriter.GenerateFullRewrite(req.RepoPath, req.FlagKey, req.WinningVariant, req.Language, dryRun)
			if err == nil && result != nil {
				// Detect whether jscodeshift was used by checking the Notes field.
				jscodeShiftUsed = containsJscodeShiftMarker(result.Notes)
			}
		} else {
			result, err = rewriter.GenerateDiffPreview(req.RepoPath, req.FlagKey, req.WinningVariant, req.Language)
		}

		if err != nil {
			log.Error("rewrite failed", zap.Error(err))
			writeError(w, http.StatusInternalServerError, "rewrite error: "+err.Error())
			return
		}

		resp := rewriteResponse{
			FilesModified:    result.FilesModified,
			LinesRemoved:     result.LinesRemoved,
			Diff:             result.Diff,
			CallSitesFound:   result.CallSitesFound,
			Notes:            result.Notes,
			TransformApplied: jscodeShiftUsed,
			JscodeShiftUsed:  jscodeShiftUsed,
		}

		writeJSON(w, http.StatusOK, resp)
	}
}

// containsJscodeShiftMarker checks whether the Notes string from GenerateFullRewrite
// indicates that an actual jscodeshift AST rewrite (not just a preview) was applied.
func containsJscodeShiftMarker(notes string) bool {
	return strings.Contains(notes, "jscodeshift AST rewrite applied") ||
		strings.Contains(notes, "jscodeshift transform applied")
}

// ---- main --------------------------------------------------------------------

func main() {
	log, _ := zap.NewProduction()
	defer log.Sync() //nolint:errcheck

	initCtx := context.Background()

	// Initialise OpenTelemetry. OTLP_ENDPOINT is optional — noop when unset.
	shutdownTracer, err := telemetry.InitTracer(initCtx, "ast-rewriter")
	if err != nil {
		log.Fatal("init tracer", zap.Error(err))
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = shutdownTracer(shutdownCtx)
	}()

	port := os.Getenv("PORT")
	if port == "" {
		port = "8085"
	}

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)

	r.Get("/health", healthHandler)
	r.Post("/api/v1/scan", scanHandler(log))
	r.Post("/api/v1/rewrite", rewriteHandler(log))

	addr := ":" + port
	log.Info("ast-rewriter starting", zap.String("addr", addr))

	// Wrap the router with otelhttp for automatic HTTP trace spans.
	if err := http.ListenAndServe(addr, otelhttp.NewHandler(r, "ast-rewriter")); err != nil {
		log.Fatal("server error", zap.Error(err))
	}
}
