package resources

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/validation"
)

// ResourceFlagEnvironment returns the schema.Resource for tombstone_flag_environment.
func ResourceFlagEnvironment() *schema.Resource {
	return &schema.Resource{
		CreateContext: resourceFlagEnvironmentCreate,
		ReadContext:   resourceFlagEnvironmentRead,
		UpdateContext: resourceFlagEnvironmentUpdate,
		DeleteContext: resourceFlagEnvironmentDelete,
		Schema: map[string]*schema.Schema{
			"flag_key": {
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
				Description: "Key of the parent tombstone_flag. Immutable after creation.",
			},
			"environment": {
				Type:     schema.TypeString,
				Required: true,
				ForceNew: true,
				ValidateFunc: validation.StringInSlice(
					[]string{"development", "staging", "production"}, false,
				),
				Description: "Target environment (development, staging, production). Immutable after creation.",
			},
			"enabled": {
				Type:        schema.TypeBool,
				Required:    true,
				Description: "Whether the flag is enabled in this environment.",
			},
			"rollout_pct": {
				Type:         schema.TypeInt,
				Optional:     true,
				Default:      0,
				ValidateFunc: validation.IntBetween(0, 100),
				Description:  "Percentage of traffic that sees this flag (0-100).",
			},
		},
	}
}

// envPayload is the request body for PATCH /api/v1/flags/{key}/environments/{env}.
type envPayload struct {
	Enabled    bool `json:"enabled"`
	RolloutPct int  `json:"rollout_pct"`
}

// envResponse models the relevant fields in the API response.
type envResponse struct {
	FlagKey     string `json:"flag_key"`
	Environment string `json:"environment"`
	Enabled     bool   `json:"enabled"`
	RolloutPct  int    `json:"rollout_pct"`
}

func resourceFlagEnvironmentCreate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	return upsertFlagEnvironment(ctx, d, meta)
}

func resourceFlagEnvironmentRead(_ context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := toClient(meta)
	flagKey, env := splitID(d.Id())

	var result envResponse
	err := doJSON(c, http.MethodGet,
		fmt.Sprintf("/api/v1/flags/%s/environments/%s", flagKey, env),
		nil, &result)
	if err != nil {
		if isNotFound(err) {
			d.SetId("")
			return nil
		}
		return diag.FromErr(fmt.Errorf("reading flag_environment %q: %w", d.Id(), err))
	}

	return setEnvState(d, &result)
}

func resourceFlagEnvironmentUpdate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	return upsertFlagEnvironment(ctx, d, meta)
}

func resourceFlagEnvironmentDelete(_ context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := toClient(meta)
	flagKey, env := splitID(d.Id())

	// Reset to safe defaults rather than hard-deleting the environment binding.
	payload := envPayload{Enabled: false, RolloutPct: 0}
	if err := doJSON(c, http.MethodPatch,
		fmt.Sprintf("/api/v1/flags/%s/environments/%s", flagKey, env),
		payload, nil); err != nil {
		if isNotFound(err) {
			return nil
		}
		return diag.FromErr(fmt.Errorf("resetting flag_environment %q: %w", d.Id(), err))
	}
	return nil
}

// upsertFlagEnvironment handles both create and update via PATCH.
func upsertFlagEnvironment(_ context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := toClient(meta)

	flagKey := d.Get("flag_key").(string)
	env := d.Get("environment").(string)

	payload := envPayload{
		Enabled:    d.Get("enabled").(bool),
		RolloutPct: d.Get("rollout_pct").(int),
	}

	var result envResponse
	if err := doJSON(c, http.MethodPatch,
		fmt.Sprintf("/api/v1/flags/%s/environments/%s", flagKey, env),
		payload, &result); err != nil {
		return diag.FromErr(fmt.Errorf("upserting flag_environment %s/%s: %w", flagKey, env, err))
	}

	d.SetId(flagKey + "/" + env)
	return setEnvState(d, &result)
}

// setEnvState writes environment fields into Terraform state.
func setEnvState(d *schema.ResourceData, r *envResponse) diag.Diagnostics {
	var diags diag.Diagnostics
	diags = append(diags, toDiag(d.Set("flag_key", r.FlagKey))...)
	diags = append(diags, toDiag(d.Set("environment", r.Environment))...)
	diags = append(diags, toDiag(d.Set("enabled", r.Enabled))...)
	diags = append(diags, toDiag(d.Set("rollout_pct", r.RolloutPct))...)
	return diags
}

// splitID splits a "{flag_key}/{environment}" ID into its two parts.
func splitID(id string) (string, string) {
	parts := strings.SplitN(id, "/", 2)
	if len(parts) != 2 {
		return id, ""
	}
	return parts[0], parts[1]
}
