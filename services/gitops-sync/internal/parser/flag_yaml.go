package parser

import (
    "fmt"
    "os"
    "path/filepath"

    "gopkg.in/yaml.v3"
)

// FlagDefinition is the YAML schema for a flag-as-code definition.
// Example file: flags/checkout/checkout-v2.yaml
type FlagDefinition struct {
    APIVersion  string            `yaml:"apiVersion"`  // "tombstone.io/v1"
    Kind        string            `yaml:"kind"`        // "FeatureFlag"
    Metadata    FlagMetadata      `yaml:"metadata"`
    Spec        FlagSpec          `yaml:"spec"`
}

type FlagMetadata struct {
    Name        string            `yaml:"name"`
    Description string            `yaml:"description"`
    Owner       string            `yaml:"owner"`
    Tags        []string          `yaml:"tags"`
}

type FlagSpec struct {
    Key         string            `yaml:"key"`
    Type        string            `yaml:"type"`        // BOOLEAN | STRING | INTEGER | FLOAT | JSON
    SafeDefault string            `yaml:"safeDefault"`
    Environments map[string]EnvSpec `yaml:"environments"`
}

type EnvSpec struct {
    Enabled    bool   `yaml:"enabled"`
    RolloutPct int    `yaml:"rolloutPct"`
}

// ParseFile parses a single YAML flag definition file.
func ParseFile(path string) (*FlagDefinition, error) {
    data, err := os.ReadFile(path)
    if err != nil {
        return nil, fmt.Errorf("read %s: %w", path, err)
    }
    var def FlagDefinition
    if err := yaml.Unmarshal(data, &def); err != nil {
        return nil, fmt.Errorf("parse %s: %w", path, err)
    }
    if def.Kind != "FeatureFlag" {
        return nil, fmt.Errorf("%s: kind must be FeatureFlag, got %q", path, def.Kind)
    }
    if def.Spec.Key == "" {
        return nil, fmt.Errorf("%s: spec.key is required", path)
    }
    return &def, nil
}

// ParseDirectory walks a directory and parses all *.yaml / *.yml files.
func ParseDirectory(dir string) ([]*FlagDefinition, []error) {
    var defs []*FlagDefinition
    var errs []error

    _ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
        if err != nil || info.IsDir() {
            return nil
        }
        ext := filepath.Ext(path)
        if ext != ".yaml" && ext != ".yml" {
            return nil
        }
        def, parseErr := ParseFile(path)
        if parseErr != nil {
            errs = append(errs, parseErr)
            return nil
        }
        defs = append(defs, def)
        return nil
    })

    return defs, errs
}
