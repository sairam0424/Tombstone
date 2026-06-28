package scanner

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// CallSite represents a single occurrence of a flag evaluation call in source code.
type CallSite struct {
	FilePath   string `json:"file_path"`
	LineNumber int    `json:"line_number"`
	Line       string `json:"line"`
	FlagKey    string `json:"flag_key"`
	Language   string `json:"language"`
}

// extToLanguage maps file extensions to language names.
var extToLanguage = map[string]string{
	".ts":  "typescript",
	".tsx": "typescript",
	".js":  "javascript",
	".jsx": "javascript",
	".py":  "python",
	".go":  "go",
	".java": "java",
	".rb":  "ruby",
}

// dirsToSkip contains directory names that should never be walked.
var dirsToSkip = map[string]bool{
	"node_modules": true,
	".git":         true,
	"vendor":       true,
	"__pycache__":  true,
	".venv":        true,
	"dist":         true,
	"build":        true,
	".next":        true,
}

// languagePatterns holds compiled regexps for each language.
// Each pattern must capture the flag key in group 1.
var languagePatterns = map[string][]*regexp.Regexp{
	"typescript": {
		regexp.MustCompile(`(?i)evaluate\s*\(\s*["'` + "`" + `]([^"'` + "`" + `]+)["'` + "`" + `]`),
		regexp.MustCompile(`(?i)isEnabled\s*\(\s*["'` + "`" + `]([^"'` + "`" + `]+)["'` + "`" + `]`),
		regexp.MustCompile(`(?i)getFlag\s*\(\s*["'` + "`" + `]([^"'` + "`" + `]+)["'` + "`" + `]`),
	},
	"javascript": {
		regexp.MustCompile(`(?i)evaluate\s*\(\s*["'` + "`" + `]([^"'` + "`" + `]+)["'` + "`" + `]`),
		regexp.MustCompile(`(?i)isEnabled\s*\(\s*["'` + "`" + `]([^"'` + "`" + `]+)["'` + "`" + `]`),
		regexp.MustCompile(`(?i)getFlag\s*\(\s*["'` + "`" + `]([^"'` + "`" + `]+)["'` + "`" + `]`),
	},
	"python": {
		regexp.MustCompile(`(?i)evaluate\s*\(\s*["']([^"']+)["']`),
		regexp.MustCompile(`(?i)is_enabled\s*\(\s*["']([^"']+)["']`),
		regexp.MustCompile(`(?i)get_flag\s*\(\s*["']([^"']+)["']`),
		regexp.MustCompile(`(?i)isEnabled\s*\(\s*["']([^"']+)["']`),
	},
	"go": {
		regexp.MustCompile("(?i)Evaluate\\s*\\(\\s*[`\"]([^`\"]+)[`\"]"),
		regexp.MustCompile("(?i)IsEnabled\\s*\\(\\s*[`\"]([^`\"]+)[`\"]"),
		regexp.MustCompile("(?i)GetFlag\\s*\\(\\s*[`\"]([^`\"]+)[`\"]"),
	},
	"java": {
		regexp.MustCompile(`(?i)evaluate\s*\(\s*"([^"]+)"`),
		regexp.MustCompile(`(?i)isEnabled\s*\(\s*"([^"]+)"`),
		regexp.MustCompile(`(?i)getFlag\s*\(\s*"([^"]+)"`),
	},
	"ruby": {
		regexp.MustCompile(`(?i)evaluate\s*\(\s*["']([^"']+)["']`),
		regexp.MustCompile(`(?i)is_enabled\s*\(\s*["']([^"']+)["']`),
		regexp.MustCompile(`(?i)get_flag\s*\(\s*["']([^"']+)["']`),
	},
}

// ScanDirectory walks dir recursively, skips noisy dirs, and returns all call
// sites that reference flagKey. If flagKey is empty, all flag calls are returned.
func ScanDirectory(dir, flagKey string) ([]CallSite, error) {
	var sites []CallSite

	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}

		if d.IsDir() {
			if dirsToSkip[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}

		ext := strings.ToLower(filepath.Ext(path))
		lang, ok := extToLanguage[ext]
		if !ok {
			return nil
		}

		patterns, ok := languagePatterns[lang]
		if !ok {
			return nil
		}

		found, err := scanFile(path, lang, flagKey, patterns)
		if err != nil {
			return nil // skip unreadable files silently
		}
		sites = append(sites, found...)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walking directory %q: %w", dir, err)
	}

	return sites, nil
}

// scanFile opens a file and searches every line against all patterns for the language.
func scanFile(path, lang, flagKey string, patterns []*regexp.Regexp) ([]CallSite, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	var sites []CallSite
	scanner := bufio.NewScanner(f)
	lineNum := 0

	for scanner.Scan() {
		lineNum++
		line := scanner.Text()

		for _, re := range patterns {
			matches := re.FindAllStringSubmatch(line, -1)
			for _, m := range matches {
				if len(m) < 2 {
					continue
				}
				key := m[1]
				if flagKey != "" && key != flagKey {
					continue
				}
				sites = append(sites, CallSite{
					FilePath:   path,
					LineNumber: lineNum,
					Line:       strings.TrimSpace(line),
					FlagKey:    key,
					Language:   lang,
				})
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return sites, err
	}
	return sites, nil
}

// Summary returns a human-readable description of the scan results.
func Summary(sites []CallSite) string {
	if len(sites) == 0 {
		return "No call sites found."
	}

	// Count per language and per file.
	langCount := map[string]int{}
	fileCount := map[string]bool{}
	for _, s := range sites {
		langCount[s.Language]++
		fileCount[s.FilePath] = true
	}

	langParts := make([]string, 0, len(langCount))
	for lang, count := range langCount {
		langParts = append(langParts, fmt.Sprintf("%s: %d", lang, count))
	}

	return fmt.Sprintf(
		"Found %d call site(s) across %d file(s). Breakdown by language — %s.",
		len(sites),
		len(fileCount),
		strings.Join(langParts, ", "),
	)
}
