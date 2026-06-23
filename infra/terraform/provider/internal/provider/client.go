package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/tombstone/terraform-provider-tombstone/internal/datasources"
	"github.com/tombstone/terraform-provider-tombstone/internal/resources"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"go.uber.org/zap"
)

// Client holds the Tombstone API connection details.
type Client struct {
	APIURL     string
	Token      string
	HTTPClient *http.Client
}

// New returns a configured *schema.Provider for the Tombstone provider.
func New() *schema.Provider {
	return &schema.Provider{
		Schema: map[string]*schema.Schema{
			"api_url": {
				Type:        schema.TypeString,
				Required:    true,
				DefaultFunc: schema.EnvDefaultFunc("TOMBSTONE_API_URL", nil),
				Description: "Base URL of the Tombstone API (e.g. https://api.tombstone.io).",
			},
			"api_token": {
				Type:        schema.TypeString,
				Required:    true,
				Sensitive:   true,
				DefaultFunc: schema.EnvDefaultFunc("TOMBSTONE_TOKEN", nil),
				Description: "Bearer token used to authenticate with the Tombstone API.",
			},
		},
		ResourcesMap: map[string]*schema.Resource{
			"tombstone_flag":             resources.ResourceFlag(),
			"tombstone_flag_environment": resources.ResourceFlagEnvironment(),
			"tombstone_region":           resources.ResourceRegion(),
		},
		DataSourcesMap: map[string]*schema.Resource{
			"tombstone_flags": datasources.DataSourceFlags(),
		},
		ConfigureContextFunc: configureClient,
	}
}

// configureClient validates provider config, constructs a Client, and performs
// a health-check against the Tombstone API.
func configureClient(_ context.Context, d *schema.ResourceData) (interface{}, diag.Diagnostics) {
	logger, _ := zap.NewProduction()
	defer logger.Sync() //nolint:errcheck

	apiURL := d.Get("api_url").(string)
	token := d.Get("api_token").(string)

	client := &Client{
		APIURL: apiURL,
		Token:  token,
		HTTPClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}

	if err := healthCheck(client); err != nil {
		logger.Error("Tombstone API health check failed", zap.Error(err))
		return nil, diag.FromErr(fmt.Errorf("tombstone provider: health check failed: %w", err))
	}

	logger.Info("Tombstone provider configured", zap.String("api_url", apiURL))
	return client, nil
}

// healthCheck calls GET /api/v1/health to verify connectivity.
func healthCheck(c *Client) error {
	req, err := http.NewRequest(http.MethodGet, c.APIURL+"/api/v1/health", nil)
	if err != nil {
		return fmt.Errorf("building health-check request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("executing health-check request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		var body map[string]interface{}
		_ = json.NewDecoder(resp.Body).Decode(&body)
		return fmt.Errorf("health check returned HTTP %d: %v", resp.StatusCode, body)
	}
	return nil
}
