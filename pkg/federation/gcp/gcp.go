// Package gcp provides a GCP Workload Identity Federation implementation of the Federation Provider interface
package gcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"github.com/hixichen/kube-iam-assume/pkg/constants"
	"github.com/hixichen/kube-iam-assume/pkg/federation"
)

// Ensure gcpProvider implements federation.Provider interface.
var _ federation.Provider = (*gcpProvider)(nil)

// gcpProvider implements federation.Provider for GCP Workload Identity Federation.
type gcpProvider struct {
	httpClient *http.Client
	projectID  string
	logger     *slog.Logger
}

// WorkloadIdentityPool represents a GCP Workload Identity Pool.
type WorkloadIdentityPool struct {
	Name        string            `json:"name"`
	DisplayName string            `json:"displayName"`
	Description string            `json:"description"`
	State       string            `json:"state"`
	Disabled    bool              `json:"disabled"`
	CreateTime  string            `json:"createTime"`
	Labels      map[string]string `json:"labels,omitempty"`
}

// WorkloadIdentityPoolProvider represents a GCP Workload Identity Pool Provider.
type WorkloadIdentityPoolProvider struct {
	Name             string            `json:"name"`
	DisplayName      string            `json:"displayName"`
	Description      string            `json:"description"`
	State            string            `json:"state"`
	Disabled         bool              `json:"disabled"`
	Oidc             *OidcConfig       `json:"oidc,omitempty"`
	AttributeMapping map[string]string `json:"attributeMapping,omitempty"`
	CreateTime       string            `json:"createTime"`
}

// OidcConfig contains OIDC configuration.
type OidcConfig struct {
	IssuerURI        string   `json:"issuerUri"`
	AllowedAudiences []string `json:"allowedAudiences,omitempty"`
}

// NewProvider creates a new GCP Provider.
func NewProvider(ctx context.Context, projectID string, logger *slog.Logger) (federation.Provider, error) {
	// Create HTTP client with default credentials (Workload Identity if configured)
	credentials, err := google.FindDefaultCredentials(ctx, "https://www.googleapis.com/auth/cloud-platform")
	if err != nil {
		return nil, fmt.Errorf("failed to find default credentials: %w", err)
	}

	httpClient := oauth2.NewClient(ctx, credentials.TokenSource)

	return &gcpProvider{
		httpClient: httpClient,
		projectID:  projectID,
		logger:     logger,
	}, nil
}

// Setup creates the OIDC identity provider/federation.
func (g *gcpProvider) Setup(ctx context.Context, cfg federation.SetupConfig) (*federation.SetupResult, error) {
	g.logger.Info("Setting up GCP Workload Identity Federation",
		"project_id", g.projectID,
		"issuer_url", cfg.IssuerURL,
		"audiences", cfg.Audiences)

	poolID := g.getOptionString(cfg.Options, "pool_id", constants.DefaultGCPWorkloadIdentityPoolID)
	poolName := g.getOptionString(cfg.Options, "pool_name", "KubeAssume Workload Identity Pool")
	providerID := constants.DefaultGCPWorkloadIdentityPoolProviderID

	// 1. Create/Get Workload Identity Pool
	poolFullName := fmt.Sprintf("projects/%s/locations/global/workloadIdentityPools/%s", g.projectID, poolID)

	pool, err := g.getWorkloadIdentityPool(ctx, poolFullName)
	if err != nil {
		if strings.Contains(err.Error(), "NotFound") {
			g.logger.Info("Workload Identity Pool not found, creating it", "pool_id", poolID)
			pool, err = g.createWorkloadIdentityPool(ctx, poolID, poolName)
			if err != nil {
				return nil, fmt.Errorf("failed to create Workload Identity Pool %s: %w", poolID, err)
			}
			g.logger.Info("Successfully created Workload Identity Pool", "pool_full_name", pool.Name)
		} else {
			return nil, fmt.Errorf("failed to get Workload Identity Pool %s: %w", poolID, err)
		}
	} else {
		g.logger.Info("Workload Identity Pool already exists", "pool_full_name", pool.Name)
	}

	// 2. Create/Update Workload Identity Pool Provider
	providerFullName := fmt.Sprintf("%s/providers/%s", pool.Name, providerID)

	provider, err := g.getWorkloadIdentityPoolProvider(ctx, providerFullName)
	if err != nil {
		if strings.Contains(err.Error(), "NotFound") {
			g.logger.Info("Workload Identity Pool Provider not found, creating it", "provider_id", providerID)
			provider, err = g.createWorkloadIdentityPoolProvider(ctx, pool.Name, providerID, cfg)
			if err != nil {
				return nil, fmt.Errorf("failed to create Workload Identity Pool Provider %s: %w", providerID, err)
			}
			g.logger.Info("Successfully created Workload Identity Pool Provider", "provider_full_name", provider.Name)
		} else {
			return nil, fmt.Errorf("failed to get Workload Identity Pool Provider %s: %w", providerID, err)
		}
	} else {
		g.logger.Info("Workload Identity Pool Provider already exists, updating", "provider_full_name", provider.Name)
		provider, err = g.updateWorkloadIdentityPoolProvider(ctx, providerFullName, cfg)
		if err != nil {
			return nil, fmt.Errorf("failed to update Workload Identity Pool Provider %s: %w", providerID, err)
		}
		g.logger.Info("Successfully updated Workload Identity Pool Provider", "provider_full_name", provider.Name)
	}

	return &federation.SetupResult{
		ProviderARN: provider.Name,
		Audiences:   cfg.Audiences,
		Thumbprint:  "", // GCP does not use thumbprints
	}, nil
}

// Validate checks if setup is valid.
func (g *gcpProvider) Validate(ctx context.Context, issuerURL string) error {
	g.logger.Debug("Validating GCP Workload Identity Federation", "issuer_url", issuerURL)

	providerInfo, err := g.GetProviderInfo(ctx, issuerURL)
	if err != nil {
		return fmt.Errorf("failed to get OIDC provider info for validation: %w", err)
	}
	if providerInfo == nil {
		return fmt.Errorf("GCP Workload Identity Pool Provider for issuer '%s' not found", issuerURL)
	}

	g.logger.Info("GCP Workload Identity Federation validated successfully",
		"issuer_url", issuerURL,
		"provider_resource_name", providerInfo.ProviderARN)
	return nil
}

// GetProviderInfo returns info about existing provider.
func (g *gcpProvider) GetProviderInfo(ctx context.Context, issuerURL string) (*federation.ProviderInfo, error) {
	g.logger.Debug("Getting GCP Workload Identity Pool Provider info", "issuer_url", issuerURL)

	// List all Workload Identity Pools
	pools, err := g.listWorkloadIdentityPools(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list Workload Identity Pools: %w", err)
	}

	for _, pool := range pools {
		// For each pool, list its providers
		providers, err := g.listWorkloadIdentityPoolProviders(ctx, pool.Name)
		if err != nil {
			return nil, fmt.Errorf("failed to list providers for pool %s: %w", pool.Name, err)
		}

		for _, provider := range providers {
			if provider.Oidc != nil && provider.Oidc.IssuerURI == issuerURL {
				return &federation.ProviderInfo{
					ProviderARN:   provider.Name,
					IssuerURL:     provider.Oidc.IssuerURI,
					Audiences:     provider.Oidc.AllowedAudiences,
					Thumbprint:    "",
					Status:        provider.State,
					CreatedAt:     provider.CreateTime,
					CloudProvider: string(federation.ProviderTypeGCP),
				}, nil
			}
		}
	}

	return nil, fmt.Errorf("no GCP Workload Identity Pool Provider found for issuer: %s", issuerURL)
}

// Delete removes the OIDC provider.
func (g *gcpProvider) Delete(ctx context.Context, issuerURL string) error {
	g.logger.Info("Deleting GCP Workload Identity Pool Provider", "issuer_url", issuerURL)

	providerInfo, err := g.GetProviderInfo(ctx, issuerURL)
	if err != nil {
		return fmt.Errorf("failed to get OIDC provider info for deletion: %w", err)
	}
	if providerInfo == nil {
		g.logger.Info("GCP Workload Identity Pool Provider not found, skipping deletion", "issuer_url", issuerURL)
		return nil
	}

	// Delete the provider
	if err := g.deleteWorkloadIdentityPoolProvider(ctx, providerInfo.ProviderARN); err != nil {
		return fmt.Errorf("failed to delete Workload Identity Pool Provider %s: %w", providerInfo.ProviderARN, err)
	}
	g.logger.Info("Successfully deleted Workload Identity Pool Provider", "provider_resource_name", providerInfo.ProviderARN)

	// Check if pool is empty and delete if so
	parts := strings.Split(providerInfo.ProviderARN, "/")
	if len(parts) >= 6 {
		poolName := strings.Join(parts[:len(parts)-2], "/")
		g.logger.Debug("Checking if parent Workload Identity Pool is empty", "pool_name", poolName)

		providers, err := g.listWorkloadIdentityPoolProviders(ctx, poolName)
		if err != nil {
			g.logger.Warn("Failed to check if parent Workload Identity Pool is empty",
				"pool_name", poolName, "error", err)
		} else if len(providers) == 0 {
			g.logger.Info("Parent Workload Identity Pool is empty, deleting it", "pool_name", poolName)
			if err := g.deleteWorkloadIdentityPool(ctx, poolName); err != nil {
				g.logger.Warn("Failed to delete parent Workload Identity Pool. Manual cleanup may be required.",
					"pool_name", poolName, "error", err)
			} else {
				g.logger.Info("Successfully deleted parent Workload Identity Pool", "pool_name", poolName)
			}
		} else {
			g.logger.Debug("Parent Workload Identity Pool is not empty, skipping deletion", "pool_name", poolName)
		}
	}

	return nil
}

// Type returns the provider type.
func (g *gcpProvider) Type() string {
	return string(federation.ProviderTypeGCP)
}

// Helper methods for GCP API calls

func (g *gcpProvider) getWorkloadIdentityPool(ctx context.Context, name string) (*WorkloadIdentityPool, error) {
	url := fmt.Sprintf("https://iam.googleapis.com/v1/%s", name)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := g.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("NotFound: %s", name)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	var pool WorkloadIdentityPool
	if err := json.NewDecoder(resp.Body).Decode(&pool); err != nil {
		return nil, err
	}
	return &pool, nil
}

func (g *gcpProvider) createWorkloadIdentityPool(ctx context.Context, poolID, displayName string) (*WorkloadIdentityPool, error) {
	parent := fmt.Sprintf("projects/%s/locations/global", g.projectID)
	url := fmt.Sprintf("https://iam.googleapis.com/v1/%s/workloadIdentityPools?workloadIdentityPoolId=%s", parent, poolID)

	pool := &WorkloadIdentityPool{
		DisplayName: displayName,
		Description: "Workload Identity Pool for Kubernetes OIDC federation managed by KubeAssume",
	}

	jsonData, err := json.Marshal(pool)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(jsonData))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := g.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	if err := json.NewDecoder(resp.Body).Decode(&pool); err != nil {
		return nil, err
	}
	return pool, nil
}

func (g *gcpProvider) getWorkloadIdentityPoolProvider(ctx context.Context, name string) (*WorkloadIdentityPoolProvider, error) {
	url := fmt.Sprintf("https://iam.googleapis.com/v1/%s", name)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := g.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("NotFound: %s", name)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	var provider WorkloadIdentityPoolProvider
	if err := json.NewDecoder(resp.Body).Decode(&provider); err != nil {
		return nil, err
	}
	return &provider, nil
}

func (g *gcpProvider) createWorkloadIdentityPoolProvider(ctx context.Context, parent, providerID string, cfg federation.SetupConfig) (*WorkloadIdentityPoolProvider, error) {
	url := fmt.Sprintf("https://iam.googleapis.com/v1/%s/providers?workloadIdentityPoolProviderId=%s", parent, providerID)

	provider := &WorkloadIdentityPoolProvider{
		DisplayName: "KubeAssume OIDC Provider",
		Description: "OIDC Provider for Kubernetes OIDC federation managed by KubeAssume",
		AttributeMapping: map[string]string{
			"google.subject":                 "assertion.sub",
			"attribute.actor":                "assertion.sub",
			"attribute.aud":                  "assertion.aud",
			"attribute.namespace":            "assertion['kubernetes.io']['namespace']",
			"attribute.pod_name":             "assertion['kubernetes.io']['pod']['name']",
			"attribute.pod_uid":              "assertion['kubernetes.io']['pod']['uid']",
			"attribute.service_account_name": "assertion['kubernetes.io']['serviceaccount']['name']",
			"attribute.original_claims":      "assertion",
		},
		Oidc: &OidcConfig{
			IssuerURI:        cfg.IssuerURL,
			AllowedAudiences: cfg.Audiences,
		},
		State: "ACTIVE",
	}

	jsonData, err := json.Marshal(provider)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(jsonData))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := g.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	if err := json.NewDecoder(resp.Body).Decode(&provider); err != nil {
		return nil, err
	}
	return provider, nil
}

func (g *gcpProvider) updateWorkloadIdentityPoolProvider(ctx context.Context, name string, cfg federation.SetupConfig) (*WorkloadIdentityPoolProvider, error) {
	url := fmt.Sprintf("https://iam.googleapis.com/v1/%s", name)

	provider := &WorkloadIdentityPoolProvider{
		Name: name,
		Oidc: &OidcConfig{
			AllowedAudiences: cfg.Audiences,
		},
	}

	jsonData, err := json.Marshal(provider)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, "PATCH", url, bytes.NewReader(jsonData))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := g.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	if err := json.NewDecoder(resp.Body).Decode(&provider); err != nil {
		return nil, err
	}
	return provider, nil
}

func (g *gcpProvider) deleteWorkloadIdentityPoolProvider(ctx context.Context, name string) error {
	url := fmt.Sprintf("https://iam.googleapis.com/v1/%s", name)
	req, err := http.NewRequestWithContext(ctx, "DELETE", url, nil)
	if err != nil {
		return err
	}

	resp, err := g.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}
	return nil
}

func (g *gcpProvider) deleteWorkloadIdentityPool(ctx context.Context, name string) error {
	url := fmt.Sprintf("https://iam.googleapis.com/v1/%s", name)
	req, err := http.NewRequestWithContext(ctx, "DELETE", url, nil)
	if err != nil {
		return err
	}

	resp, err := g.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}
	return nil
}

func (g *gcpProvider) listWorkloadIdentityPools(ctx context.Context) ([]*WorkloadIdentityPool, error) {
	parent := fmt.Sprintf("projects/%s/locations/global", g.projectID)
	url := fmt.Sprintf("https://iam.googleapis.com/v1/%s/workloadIdentityPools", parent)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := g.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	var result struct {
		WorkloadIdentityPools []*WorkloadIdentityPool `json:"workloadIdentityPools"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	return result.WorkloadIdentityPools, nil
}

func (g *gcpProvider) listWorkloadIdentityPoolProviders(ctx context.Context, poolName string) ([]*WorkloadIdentityPoolProvider, error) {
	url := fmt.Sprintf("https://iam.googleapis.com/v1/%s/providers", poolName)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := g.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	var result struct {
		WorkloadIdentityPoolProviders []*WorkloadIdentityPoolProvider `json:"workloadIdentityPoolProviders"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	return result.WorkloadIdentityPoolProviders, nil
}

func (g *gcpProvider) getOptionString(options map[string]interface{}, key, defaultValue string) string {
	if val, ok := options[key]; ok {
		if strVal, isString := val.(string); isString {
			return strVal
		}
	}
	return defaultValue
}
