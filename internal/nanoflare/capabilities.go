package nanoflare

import "sort"

// RuntimeCapabilities reports guarantees, rather than merely configured intent.
// It deliberately distinguishes the selected trusted profile from untrusted cells.
type RuntimeCapabilities struct {
	WorkerID               string            `json:"worker_id"`
	Execution              string            `json:"execution"`
	Enabled                bool              `json:"enabled"`
	Isolation              string            `json:"isolation"`
	PublicIngress          bool              `json:"public_ingress"`
	DynamicDispatchEnabled bool              `json:"dynamic_dispatch_enabled"`
	CPUAccounting          bool              `json:"cpu_accounting"`
	CPULimit               bool              `json:"cpu_limit"`
	CacheAPI               string            `json:"cache_api"`
	RawTCP                 string            `json:"raw_tcp"`
	Outbound               string            `json:"outbound"`
	DurationMeasurement    string            `json:"duration_measurement"`
	CompatibilityCeiling   string            `json:"compatibility_ceiling"`
	EngineVersion          string            `json:"engine_version"`
	Experimental           []string          `json:"experimental"`
	Unavailable            []string          `json:"unavailable_features"`
	DispatchCounters       *DispatchCounters `json:"dispatch_counters,omitempty"`
	Limits                 RuntimeLimits     `json:"limits"`
}
type RuntimeLimits struct {
	WallMS               int `json:"wall_ms,omitempty"`
	BackgroundMS         int `json:"background_ms,omitempty"`
	Subrequests          int `json:"subrequests,omitempty"`
	Depth                int `json:"depth,omitempty"`
	GlobalInvocations    int `json:"global_invocations,omitempty"`
	NamespaceInvocations int `json:"namespace_invocations,omitempty"`
	CustomerInvocations  int `json:"customer_invocations,omitempty"`
	CodeDescriptors      int `json:"code_descriptors,omitempty"`
	CodeDescriptorBytes  int `json:"code_descriptor_bytes,omitempty"`
}

func (s *Service) RuntimeCapabilitiesForOrg(org, worker string) (RuntimeCapabilities, error) {
	app, err := s.appForOrg(org, worker)
	if err != nil {
		return RuntimeCapabilities{}, err
	}
	enabled := s.dispatch.enabled.Load()
	result := RuntimeCapabilities{WorkerID: app.ID, CacheAPI: "runner_configured", RawTCP: "runner_configured", Execution: "static", Enabled: true, Isolation: "trusted_shared_workerd", PublicIngress: !app.IsPrivate(), DynamicDispatchEnabled: enabled, Outbound: "runner_configured", DurationMeasurement: "elapsed_handler", CompatibilityCeiling: s.maxCompatibilityDate, EngineVersion: "unreported", Experimental: []string{}, Unavailable: []string{"untrusted_execution", "cpu_accounting", "cpu_limit"}}
	// Static Cache/TCP support is configured by the runner, not attested here.
	if app.IsDynamic() {
		counts := s.DispatchCounters(worker)
		result.DispatchCounters = &counts
		result.Execution = "dynamic"
		result.CacheAPI = "unavailable"
		result.RawTCP = "unavailable"
		result.Enabled = enabled
		result.Outbound = "invocation_http_policy"
		result.DurationMeasurement = "elapsed_dispatch"
		result.Experimental = []string{"worker_loader", "native_props", "nodejs_als"}
		result.Unavailable = append(result.Unavailable, "cache_api", "raw_tcp", "websockets", "classic_scripts", "custom_dispatch_limits", "custom_dispatch_outbound", "arbitrary_service_rpc", "customer_namespace_dispatch", "schedules", "custom_dns_profiles", "presigned_object_urls")
		result.Limits = RuntimeLimits{WallMS: 30000, BackgroundMS: 2000, Subrequests: 100, Depth: 8, GlobalInvocations: 1024, NamespaceInvocations: 128, CustomerInvocations: 32, CodeDescriptors: 64, CodeDescriptorBytes: 32 << 20}
	}
	return result, nil
}
func (s *Service) PublicationsForOrg(org, worker string) ([]PublicationResult, error) {
	if _, err := s.appForOrg(org, worker); err != nil {
		return nil, err
	}
	records, err := s.store.ListPublications()
	if err != nil {
		return nil, err
	}
	result := []PublicationResult{}
	for _, record := range records {
		if record.AppID == worker {
			result = append(result, record.Result())
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.After(result[j].CreatedAt) })
	if len(result) > 100 {
		result = result[:100]
	}
	return result, nil
}
