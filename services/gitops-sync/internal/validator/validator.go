package validator

import (
    "fmt"
    "regexp"
    "strings"

    "github.com/tombstone/gitops-sync/internal/parser"
)

// Naming convention: team.service.feature (dot-notation, all lowercase, no spaces)
// NOTE: the dot MUST be escaped (\.) — an unescaped dot matches any character,
// allowing invalid keys like "teamXservice" to pass validation.
var keyPattern = regexp.MustCompile(`^[a-z][a-z0-9-]*(\.[a-z][a-z0-9-]*)*$`)

type ValidationError struct {
    FlagKey string
    Message string
}

func (e ValidationError) Error() string {
    return fmt.Sprintf("flag %q: %s", e.FlagKey, e.Message)
}

// Validate checks a FlagDefinition for naming, type, and rollout validity.
func Validate(def *parser.FlagDefinition) []ValidationError {
    var errs []ValidationError

    if !keyPattern.MatchString(def.Spec.Key) {
        errs = append(errs, ValidationError{
            FlagKey: def.Spec.Key,
            Message: "key must match pattern team.service.feature (lowercase, dots, hyphens only)",
        })
    }

    validTypes := map[string]bool{
        "BOOLEAN": true, "STRING": true, "INTEGER": true, "FLOAT": true, "JSON": true,
    }
    if !validTypes[strings.ToUpper(def.Spec.Type)] {
        errs = append(errs, ValidationError{
            FlagKey: def.Spec.Key,
            Message: fmt.Sprintf("type %q is invalid; must be BOOLEAN, STRING, INTEGER, FLOAT, or JSON", def.Spec.Type),
        })
    }

    if def.Metadata.Owner == "" {
        errs = append(errs, ValidationError{
            FlagKey: def.Spec.Key,
            Message: "metadata.owner is required",
        })
    }

    for env, spec := range def.Spec.Environments {
        if spec.RolloutPct < 0 || spec.RolloutPct > 100 {
            errs = append(errs, ValidationError{
                FlagKey: def.Spec.Key,
                Message: fmt.Sprintf("environment %q: rolloutPct must be 0-100", env),
            })
        }
    }

    return errs
}

// ValidateAll validates a slice of definitions and returns all errors.
func ValidateAll(defs []*parser.FlagDefinition) map[string][]ValidationError {
    result := make(map[string][]ValidationError)
    for _, def := range defs {
        if errs := Validate(def); len(errs) > 0 {
            result[def.Spec.Key] = errs
        }
    }
    return result
}
