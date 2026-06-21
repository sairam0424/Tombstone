package datasources

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

// DataSourceFlags returns the schema.Resource for the tombstone_flags data source.
func DataSourceFlags() *schema.Resource {
	return &schema.Resource{
		ReadContext: dataSourceFlagsRead,
		Schema: map[string]*schema.Schema{
			"project_id": {
				Type:        schema.TypeString,
				Optional:    true,
				Default:     "00000000-0000-0000-0000-000000000001",
				Description: "Project UUID to filter flags. Defaults to the root project.",
			},
			"flags": {
				Type:     schema.TypeList,
				Computed: true,
				Elem: &schema.Resource{
					Schema: map[string]*schema.Schema{
						"key": {
							Type:     schema.TypeString,
							Computed: true,
						},
						"name": {
							Type:     schema.TypeString,
							Computed: true,
						},
						"state": {
							Type:     schema.TypeString,
							Computed: true,
						},
					},
				},
				Description: "List of flags belonging to the given project.",
			},
		},
	}
}

// flagSummary is a minimal view of a flag returned by the list endpoint.
type flagSummary struct {
	Key   string `json:"key"`
	Name  string `json:"name"`
	State string `json:"state"`
}

// dsClient mirrors the minimal fields we need from provider.Client.
type dsClient struct {
	APIURL     string
	Token      string
	HTTPClient *http.Client
}

func dataSourceFlagsRead(_ context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	c := toClient(meta)
	projectID := d.Get("project_id").(string)

	path := fmt.Sprintf("/api/v1/flags?project_id=%s", projectID)
	req, err := http.NewRequest(http.MethodGet, c.APIURL+path, nil)
	if err != nil {
		return diag.FromErr(fmt.Errorf("building flags list request: %w", err))
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return diag.FromErr(fmt.Errorf("executing flags list request: %w", err))
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		var errBody map[string]interface{}
		_ = json.NewDecoder(resp.Body).Decode(&errBody)
		return diag.FromErr(fmt.Errorf("flags list returned HTTP %d: %v", resp.StatusCode, errBody))
	}

	var flags []flagSummary
	if err := json.NewDecoder(resp.Body).Decode(&flags); err != nil {
		return diag.FromErr(fmt.Errorf("decoding flags list response: %w", err))
	}

	flat := make([]map[string]interface{}, 0, len(flags))
	for _, f := range flags {
		flat = append(flat, map[string]interface{}{
			"key":   f.Key,
			"name":  f.Name,
			"state": f.State,
		})
	}
	if err := d.Set("flags", flat); err != nil {
		return diag.FromErr(fmt.Errorf("setting flags in state: %w", err))
	}

	// Use project_id as the data source ID (stable, no server-generated ID).
	d.SetId(hashProjectID(projectID))
	return nil
}

// toClient casts meta to the concrete client type via a duck-typed struct.
func toClient(meta interface{}) *dsClient {
	type rawClient struct {
		APIURL     string
		Token      string
		HTTPClient *http.Client
	}
	rc := meta.(*rawClient)
	return &dsClient{APIURL: rc.APIURL, Token: rc.Token, HTTPClient: rc.HTTPClient}
}

// hashProjectID produces a deterministic, stable ID string for the data source.
func hashProjectID(projectID string) string {
	h := fmt.Sprintf("%x", mustHash(projectID))
	return "flags-" + h
}

func mustHash(s string) []byte {
	return bytes.NewBufferString(s).Bytes()
}
