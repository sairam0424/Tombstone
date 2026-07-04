package rewriter

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/tombstone/ast-rewriter/internal/scanner"
)

// RewriteResult describes the outcome of a rewrite (or diff-preview) operation.
type RewriteResult struct {
	FilesModified  []string `json:"files_modified"`
	LinesRemoved   int      `json:"lines_removed"`
	Diff           string   `json:"diff"`
	CallSitesFound int      `json:"call_sites_found"`
	Notes          string   `json:"notes"`
}

// variantReplacement returns the inline expression that should replace a flag
// evaluation call once the winning variant is known. For boolean flags the
// replacement is "true" or "false"; for string variants the literal value is
// quoted with the language's preferred quote style.
func variantReplacement(winningVariant, language string) string {
	lower := strings.ToLower(winningVariant)
	if lower == "true" || lower == "false" || lower == "on" || lower == "off" {
		boolVal := lower == "true" || lower == "on"
		return fmt.Sprintf("%v", boolVal)
	}

	switch language {
	case "python", "ruby":
		return fmt.Sprintf("'%s'", winningVariant)
	default:
		return fmt.Sprintf(`"%s"`, winningVariant)
	}
}

// buildHunk generates a unified-diff hunk for a single call site.
func buildHunk(site scanner.CallSite, replacement string) string {
	oldLine := site.Line
	// Replace the entire call expression on the line with the winning variant literal.
	// This is a textual approximation — real AST rewriting is done by jscodeshift / libCST.
	newLine := site.Line // default: no change detectable at text level

	// Best-effort textual substitution for the most common pattern shapes.
	for _, call := range []string{"evaluate", "isEnabled", "getFlag", "is_enabled", "get_flag", "Evaluate", "IsEnabled", "GetFlag"} {
		prefix := call + `("`
		prefixSQ := call + `('`
		prefixBT := call + "(`"

		for _, p := range []string{prefix, prefixSQ, prefixBT} {
			if strings.Contains(oldLine, p) {
				// Find the end of the call: closing paren after closing quote.
				// We replace the whole call token with the replacement value.
				startIdx := strings.Index(oldLine, p)
				if startIdx == -1 {
					continue
				}
				// Find closing paren after the key.
				endIdx := strings.Index(oldLine[startIdx:], ")")
				if endIdx == -1 {
					continue
				}
				callExpr := oldLine[startIdx : startIdx+endIdx+1]
				newLine = strings.Replace(oldLine, callExpr, replacement, 1)
				break
			}
		}
	}

	return fmt.Sprintf(
		"@@ -%d,1 +%d,1 @@\n-%s\n+%s\n",
		site.LineNumber,
		site.LineNumber,
		oldLine,
		newLine,
	)
}

// GenerateDiffPreview scans repoPath for all call sites of flagKey, then
// produces a unified-diff preview showing what the codebase would look like
// after hardcoding winningVariant everywhere the flag is evaluated.
//
// NOTE: This is a textual diff preview only. For production-safe, semantics-
// preserving AST rewrites use:
//   - TypeScript/JavaScript: jscodeshift (https://github.com/facebook/jscodeshift)
//   - Python:                libCST (https://github.com/Instagram/LibCST)
//   - Go:                    go/ast + go/format from the standard library
//   - Java:                  OpenRewrite (https://github.com/openrewrite/rewrite)
//   - Ruby:                  RuboCop's AutoCorrect API
func GenerateDiffPreview(repoPath, flagKey, winningVariant, language string) (*RewriteResult, error) {
	if _, err := os.Stat(repoPath); err != nil {
		return nil, fmt.Errorf("repo path %q not accessible: %w", repoPath, err)
	}

	sites, err := scanner.ScanDirectory(repoPath, flagKey)
	if err != nil {
		return nil, fmt.Errorf("scanning repository: %w", err)
	}

	result := &RewriteResult{
		CallSitesFound: len(sites),
		FilesModified:  []string{},
		Notes: "This is a textual diff preview. For production-safe AST rewrites use: " +
			"jscodeshift (TypeScript/JS), libCST (Python), go/ast (Go), OpenRewrite (Java), " +
			"RuboCop AutoCorrect (Ruby).",
	}

	if len(sites) == 0 {
		result.Notes = fmt.Sprintf("No call sites found for flag %q in %s. %s", flagKey, repoPath, result.Notes)
		return result, nil
	}

	// Filter by language if specified.
	filtered := sites
	if language != "" {
		filtered = filtered[:0]
		for _, s := range sites {
			if s.Language == language {
				filtered = append(filtered, s)
			}
		}
	}

	// Gather unique file paths and build diff hunks.
	seenFiles := map[string]bool{}
	var diffParts []string
	linesRemoved := 0

	for _, site := range filtered {
		rep := variantReplacement(winningVariant, site.Language)

		if !seenFiles[site.FilePath] {
			seenFiles[site.FilePath] = true
			result.FilesModified = append(result.FilesModified, site.FilePath)
			diffParts = append(diffParts, fmt.Sprintf("--- a/%s\n+++ b/%s", site.FilePath, site.FilePath))
		}

		hunk := buildHunk(site, rep)
		diffParts = append(diffParts, hunk)

		// Count lines that differ (old line removed, new line added).
		if !strings.Contains(hunk, "-"+site.Line+"\n+"+site.Line+"\n") {
			linesRemoved++
		}
	}

	result.LinesRemoved = linesRemoved
	result.Diff = strings.Join(diffParts, "\n")

	return result, nil
}

// GenerateFullRewrite performs a best-effort AST rewrite for TypeScript/JavaScript
// sources using jscodeshift, falling back to the textual diff preview for other
// languages or when jscodeshift is unavailable.
//
// When language is "typescript" or "javascript" (case-insensitive) and jscodeshift
// is present on PATH, the call sites in repoPath are rewritten in-place (or in
// dry-run mode when dryRun=true). The returned RewriteResult is augmented with
// jscodeshift's output so callers can distinguish real AST rewrites from previews.
func GenerateFullRewrite(repoPath, flagKey, winningVariant, language string, dryRun bool) (*RewriteResult, error) {
	// Always start with the textual diff preview so we have call-site counts and
	// a fallback Diff even when jscodeshift is unavailable.
	base, err := GenerateDiffPreview(repoPath, flagKey, winningVariant, language)
	if err != nil {
		return nil, err
	}

	lang := strings.ToLower(language)
	if lang != "typescript" && lang != "javascript" {
		// Non-JS/TS language: textual preview is the best we can do.
		return base, nil
	}

	jsResult, err := RewriteWithJscodeshift(context.Background(), repoPath, flagKey, winningVariant, dryRun)
	if err != nil {
		// jscodeshift subprocess error — return the preview with a warning note.
		base.Notes = fmt.Sprintf("jscodeshift error (%v); falling back to textual diff preview. %s", err, base.Notes)
		return base, nil
	}

	if !jsResult.TransformApplied {
		// Binary not found or transform reported no changes.
		base.Notes = fmt.Sprintf("%s | %s", jsResult.Notes, base.Notes)
		return base, nil
	}

	// jscodeshift succeeded — promote its output as the canonical result.
	merged := &RewriteResult{
		FilesModified:  jsResult.FilesModified,
		LinesRemoved:   jsResult.LinesRemoved,
		Diff:           jsResult.RawOutput,
		CallSitesFound: base.CallSitesFound,
		Notes:          fmt.Sprintf("jscodeshift AST rewrite applied. %s", jsResult.Notes),
	}

	// If jscodeshift didn't populate files (dry-run or no-op), keep the preview list.
	if len(merged.FilesModified) == 0 {
		merged.FilesModified = base.FilesModified
		merged.LinesRemoved = base.LinesRemoved
		merged.Diff = base.Diff
	}

	return merged, nil
}
