package resources

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/validation"
)

// flagClient is the minimal interface expected from meta.
type flagClient interface {
	do(method, path string, body interface{}) (*http.Response, error)
}

// ResourceFlag returns the schema.Resource for tombstone_flag.
func ResourceFlag() *schema.Resource {
	return &schema.Resource{
		CreateContext: resourceFlagCreate,
		ReadContext:   resourceFlagRead,
		UpdateContext: resourceFlagUpdate,
		DeleteContext: resourceFlagDelete,
		Schema: map[string]*schema.Schema{
			"key": {
				Type:        schema.TypeString,
				Required:    true,
				ForceNew:    true,
				Description: "Unique flag key (immutable after creation).",
			},
			"name": {
				Type:        schema.TypeString,
				Required:    true,
				Description: "Human-readable name of the flag.",
			},
			"description": {
				Type:        schema.TypeString,
				Optional:    true,
				Description: "Optional description of the flag.",
			},
			"flag_type": {
				Type:     schema.TypeString,
				Required: true,
				ForceNew: true,
				ValidateFunc: validation.StringInSlice(
					[]string{"BOOLEAN", "STRING", "INTEGER", "FLOAT", "JSON"}, false,
				),
				Description: "Type of the flag (BOOLEAN, STRING, INTEGER, FLOAT, JSON). Immutable after creation.",
			},
			"owner_id": {
				Type:        schema.TypeString,
				Required:    true,
				Description: "UUID of the owner (user or team) responsible for this flag.",
			},
			"safe_default": {
				Type:        schema.TypeString,
				Optional:    true,
				Default:     "false",
				Description: "Safe default value returned when evaluation fails.",
			},
			"state": {
				Type:        schema.TypeString,
				Computed:    true,
				Description: "Current lifecycle state of the flag (active, archived, tombstoned).",
			},
		},
	}
}

// flagPayload is used for create/update requests.
type flagPayload struct {
	Key         string `json:"key,omitempty"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	FlagType    string `json:"flag_type,omitempty"`
	OwnerID     string `json:"owner_id"`
	SafeDefault string `json:"safe_default,omitempty"`
}

// flagResponse models the API response fields we care about.
type flagResponse struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	Description string `json:"description"`
	FlagType    string `json:"flag_type"`
	OwnerID     string `json:"owner_id"`
	SafeDefault string `json:"safe_default"`
	State       string `json:"state"`
}

func resourceFlagCreate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := toClient(meta)

	payload := flagPayload{
		Key:         d.Get("key").(string),
		Name:        d.Get("name").(string),
		Description: d.Get("description").(string),
		FlagType:    d.Get("flag_type").(string),
		OwnerID:     d.Get("owner_id").(string),
		SafeDefault: d.Get("safe_default").(string),
	}

	var result flagResponse
	if err := doJSON(c, http.MethodPost, "/api/v1/flags", payload, &result); err != nil {
		return diag.FromErr(fmt.Errorf("creating flag %q: %w", payload.Key, err))
	}

	d.SetId(result.Key)
	return setFlagState(d, &result)
}

func resourceFlagRead(_ context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := toClient(meta)

	var result flagResponse
	err := doJSON(c, http.MethodGet, "/api/v1/flags/"+d.Id(), nil, &result)
	if err != nil {
		if isNotFound(err) {
			d.SetId("")
			return nil
		}
		return diag.FromErr(fmt.Errorf("reading flag %q: %w", d.Id(), err))
	}

	return setFlagState(d, &result)
}

func resourceFlagUpdate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := toClient(meta)

	payload := flagPayload{
		Name:        d.Get("name").(string),
		Description: d.Get("description").(string),
		OwnerID:     d.Get("owner_id").(string),
	}

	var result flagResponse
	if err := doJSON(c, http.MethodPut, "/api/v1/flags/"+d.Id(), payload, &result); err != nil {
		return diag.FromErr(fmt.Errorf("updating flag %q: %w", d.Id(), err))
	}

	return setFlagState(d, &result)
}

func resourceFlagDelete(_ context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := toClient(meta)

	if err := doJSON(c, http.MethodDelete, "/api/v1/flags/"+d.Id(), nil, nil); err != nil {
		if isNotFound(err) {
			return nil
		}
		return diag.FromErr(fmt.Errorf("deleting flag %q: %w", d.Id(), err))
	}
	return nil
}

// setFlagState writes all computed/known fields from an API response into state.
func setFlagState(d *schema.ResourceData, r *flagResponse) diag.Diagnostics {
	var diags diag.Diagnostics
	diags = append(diags, toDiag(d.Set("key", r.Key))...)
	diags = append(diags, toDiag(d.Set("name", r.Name))...)
	diags = append(diags, toDiag(d.Set("description", r.Description))...)
	diags = append(diags, toDiag(d.Set("flag_type", r.FlagType))...)
	diags = append(diags, toDiag(d.Set("owner_id", r.OwnerID))...)
	diags = append(diags, toDiag(d.Set("safe_default", r.SafeDefault))...)
	diags = append(diags, toDiag(d.Set("state", r.State))...)
	return diags
}

// -- shared helpers ----------------------------------------------------------

// apiClient is the concrete type we expect from meta (provider.Client).
type apiClient struct {
	APIURL     string
	Token      string
	HTTPClient *http.Client
}

// toClient safely casts meta to *apiClient (matches provider.Client fields).
func toClient(meta interface{}) *apiClient {
	type rawClient struct {
		APIURL     string
		Token      string
		HTTPClient *http.Client
	}
	rc := meta.(*rawClient)
	return &apiClient{APIURL: rc.APIURL, Token: rc.Token, HTTPClient: rc.HTTPClient}
}

// doJSON executes an HTTP request with an optional JSON body and unmarshals
// the response into out (may be nil for DELETE with empty response).
func doJSON(c *apiClient, method, path string, body, out interface{}) error {
	var req *http.Request
	var err error

	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshalling request body: %w", err)
		}
		req, err = http.NewRequest(method, c.APIURL+path, bytes.NewReader(b))
		if err != nil {
			return fmt.Errorf("building request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
	} else {
		req, err = http.NewRequest(method, c.APIURL+path, nil)
		if err != nil {
			return fmt.Errorf("building request: %w", err)
		}
	}

	req.Header.Set("Authorization", "Bearer "+c.Token)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("executing request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return &notFoundError{path: path}
	}
	if resp.StatusCode >= 300 {
		var errBody map[string]interface{}
		_ = json.NewDecoder(resp.Body).Decode(&errBody)
		return fmt.Errorf("API returned HTTP %d for %s %s: %v", resp.StatusCode, method, path, errBody)
	}
	if out != nil && resp.ContentLength != 0 {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("decoding response: %w", err)
		}
	}
	return nil
}

type notFoundError struct{ path string }

func (e *notFoundError) Error() string { return "not found: " + e.path }

func isNotFound(err error) bool {
	_, ok := err.(*notFoundError)
	return ok
}

// toDiag converts a Set error to a diag slice.
func toDiag(err error) diag.Diagnostics {
	if err != nil {
		return diag.FromErr(err)
	}
	return nil
}
