package resources

// tombstone_region resource — manages multi-region topology registration.
//
// Registers a secondary (or primary) region with the Tombstone flag-api so the
// full regional topology can be declared as Infrastructure-as-Code. Under the
// hood it calls:
//
//	CREATE  POST   /api/v1/regions
//	READ    GET    /api/v1/regions/{region}
//	DELETE  DELETE /api/v1/regions/{region}
//
// There is no update path: the region identifier is immutable — you must
// delete and recreate to change the api_url or gateway_url.

import (
	"context"
	"fmt"
	"net/http"

	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

// ResourceRegion returns the schema.Resource for tombstone_region.
func ResourceRegion() *schema.Resource {
	return &schema.Resource{
		CreateContext: resourceRegionCreate,
		ReadContext:   resourceRegionRead,
		DeleteContext: resourceRegionDelete,
		// No UpdateContext — all fields are ForceNew.
		Schema: map[string]*schema.Schema{
			"region": {
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
				Description: "Region identifier (e.g. us-east-1, eu-west-1). Immutable after creation.",
			},
			"api_url": {
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
				Description: "Base URL of the Tombstone flag-api in this region.",
			},
			"gateway_url": {
				Type:        schema.TypeString,
				Optional:    true,
				ForceNew:    true,
				Description: "Base URL of the Tombstone gateway (SSE endpoint) in this region.",
			},
			"is_primary": {
				Type:        schema.TypeBool,
				Required:    true,
				ForceNew:    true,
				Description: "Whether this region is the primary write region.",
			},
			// Computed fields populated by the API on creation.
			"created_at": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "RFC3339 timestamp of when the region was registered.",
			},
			"status": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "Current health status of the region as reported by the API.",
			},
		},
	}
}

// regionPayload is the request body for POST /api/v1/regions.
type regionPayload struct {
	Region     string `json:"region"`
	APIURL     string `json:"api_url"`
	GatewayURL string `json:"gateway_url,omitempty"`
	IsPrimary  bool   `json:"is_primary"`
}

// regionResponse models the fields returned by GET /api/v1/regions/{region}.
type regionResponse struct {
	Region     string `json:"region"`
	APIURL     string `json:"api_url"`
	GatewayURL string `json:"gateway_url"`
	IsPrimary  bool   `json:"is_primary"`
	CreatedAt  string `json:"created_at"`
	Status     string `json:"status"`
}

func resourceRegionCreate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := toClient(meta)

	payload := regionPayload{
		Region:     d.Get("region").(string),
		APIURL:     d.Get("api_url").(string),
		GatewayURL: d.Get("gateway_url").(string),
		IsPrimary:  d.Get("is_primary").(bool),
	}

	var result regionResponse
	if err := doJSON(c, http.MethodPost, "/api/v1/regions", payload, &result); err != nil {
		return diag.FromErr(fmt.Errorf("creating region %q: %w", payload.Region, err))
	}

	d.SetId(result.Region)
	return setRegionState(d, &result)
}

func resourceRegionRead(_ context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := toClient(meta)

	var result regionResponse
	err := doJSON(c, http.MethodGet, "/api/v1/regions/"+d.Id(), nil, &result)
	if err != nil {
		if isNotFound(err) {
			d.SetId("")
			return nil
		}
		return diag.FromErr(fmt.Errorf("reading region %q: %w", d.Id(), err))
	}

	return setRegionState(d, &result)
}

func resourceRegionDelete(_ context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := toClient(meta)

	err := doJSON(c, http.MethodDelete, "/api/v1/regions/"+d.Id(), nil, nil)
	if err != nil {
		if isNotFound(err) {
			return nil
		}
		return diag.FromErr(fmt.Errorf("deleting region %q: %w", d.Id(), err))
	}
	return nil
}

// setRegionState writes API response fields into Terraform state.
func setRegionState(d *schema.ResourceData, r *regionResponse) diag.Diagnostics {
	var diags diag.Diagnostics
	diags = append(diags, toDiag(d.Set("region", r.Region))...)
	diags = append(diags, toDiag(d.Set("api_url", r.APIURL))...)
	diags = append(diags, toDiag(d.Set("gateway_url", r.GatewayURL))...)
	diags = append(diags, toDiag(d.Set("is_primary", r.IsPrimary))...)
	diags = append(diags, toDiag(d.Set("created_at", r.CreatedAt))...)
	diags = append(diags, toDiag(d.Set("status", r.Status))...)
	return diags
}
