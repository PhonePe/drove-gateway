package main

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

const ns = "drove_gateway"

type DroveGatewayPrometheusMetrics struct {
	CountFailedReloads                prometheus.Counter
	CountSuccessfulReloads            prometheus.Counter
	CountProxyControlPlaneTimeouts    prometheus.Counter
	HistogramReloadDuration           prometheus.Histogram
	GaugeResolverHealthy              prometheus.Gauge
	GaugeUpstreamUpdatesViaAPIHealthy prometheus.Gauge
	GaugeServerStateFileUpdateHealthy prometheus.Gauge
	GaugeProxyControlPlaneHealthy     prometheus.Gauge
	GaugeDiskIOHealthy                prometheus.Gauge
	GaugeConfigGenerationHealthy      prometheus.Gauge
	GaugeTemplateRenderingHealthy     prometheus.Gauge

	CountInvalidSubdomainLabelWarnings    prometheus.Counter
	CountDuplicateSubdomainLabelWarnings  *prometheus.CounterVec
	CountEndpointCheckFails               *prometheus.CounterVec
	CountEndpointDownErrors               *prometheus.CounterVec
	GaugeAllEndpointsDown                 *prometheus.GaugeVec
	CountDroveAppSyncErrors               *prometheus.CounterVec
	CountDroveStreamErrors                *prometheus.CounterVec
	CountDroveStreamNoDataWarnings        *prometheus.CounterVec
	CountDroveEventsReceived              *prometheus.CounterVec
	HistogramAppRefreshDuration           *prometheus.HistogramVec
	GaugeAppsWithRoutingTag               *prometheus.GaugeVec
	GaugeAppsWithoutRoutingTag            *prometheus.GaugeVec
	GaugeAppsIgnoredByRealm               *prometheus.GaugeVec
	HaproxyAPICallsSuccessful             *prometheus.CounterVec
	HaproxyAPICallsFailed                 *prometheus.CounterVec
	NginxAPICallsSuccessful               *prometheus.CounterVec
	NginxAPICallsFailed                   *prometheus.CounterVec
	GaugeConfiguredEndpoints              *prometheus.GaugeVec
	GaugeHealthyEndpoints                 *prometheus.GaugeVec
	HaproxyReconcileAllBackendsDuration   *prometheus.HistogramVec
	NginxPlusReconcileAllBackendsDuration *prometheus.HistogramVec
	TemplateRenderDuration                *prometheus.HistogramVec
}

func setupPrometheusMetrics() {
	// Initialize metrics that depend on the proxy platform configuration.
	Metrics.CountFailedReloads = prometheus.NewCounter(
		prometheus.CounterOpts{
			Namespace: ns,
			Name:      "reloads_failed",
			Help:      "Total number of failed " + config.ProxyPlatform + " reloads",
		},
	)
	Metrics.CountSuccessfulReloads = prometheus.NewCounter(
		prometheus.CounterOpts{
			Namespace: ns,
			Name:      "reloads_successful",
			Help:      "Total number of successful " + config.ProxyPlatform + " reloads",
		},
	)
	Metrics.CountProxyControlPlaneTimeouts = prometheus.NewCounter(
		prometheus.CounterOpts{
			Namespace: ns,
			Name:      "proxy_control_plane_timeout_breaches_total",
			Help:      "Total number of times proxy control-plane remained unresponsive beyond configured timeout",
		},
	)
	Metrics.HistogramReloadDuration = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Namespace: ns,
			Name:      "reload_duration",
			Help:      config.ProxyPlatform + " reload duration",
			Buckets:   prometheus.ExponentialBuckets(0.05, 2, 10),
		},
	)

	Metrics.CountInvalidSubdomainLabelWarnings = prometheus.NewCounter(
		prometheus.CounterOpts{
			Namespace: ns,
			Name:      "invalid_subdomain_label_warnings",
			Help:      "Total number of warnings about invalid subdomain label",
		},
	)
	Metrics.CountDuplicateSubdomainLabelWarnings = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: ns,
			Name:      "duplicate_subdomain_label_warnings",
			Help:      "Total number of warnings about duplicate subdomain label",
		},
		[]string{"namespace"},
	)
	Metrics.CountEndpointCheckFails = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: ns,
			Name:      "endpoint_check_fails",
			Help:      "Total number of endpoint check failure errors",
		},
		[]string{"namespace"},
	)
	Metrics.CountEndpointDownErrors = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: ns,
			Name:      "endpoint_down_errors",
			Help:      "Total number of endpoint down errors",
		},
		[]string{"namespace"},
	)
	Metrics.GaugeConfigGenerationHealthy = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Namespace: ns,
			Name:      "config_generation_healthy",
			Help:      "1 if config generation is healthy, 0 otherwise",
		},
	)
	Metrics.GaugeTemplateRenderingHealthy = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Namespace: ns,
			Name:      "template_rendering_healthy",
			Help:      "1 if template rendering is healthy, 0 otherwise",
		},
	)
	Metrics.GaugeResolverHealthy = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Namespace: ns,
			Name:      "resolver_healthy",
			Help:      "1 if DNS resolver is healthy, 0 otherwise",
		},
	)
	Metrics.GaugeUpstreamUpdatesViaAPIHealthy = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Namespace: ns,
			Name:      "upstream_updates_via_api_healthy",
			Help:      "1 if upstream updates via API is healthy, 0 otherwise",
		},
	)
	Metrics.GaugeServerStateFileUpdateHealthy = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Namespace: ns,
			Name:      "server_state_file_update_healthy",
			Help:      "1 if HAProxy global server state file updates are healthy, 0 otherwise",
		},
	)
	Metrics.GaugeProxyControlPlaneHealthy = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Namespace: ns,
			Name:      "proxy_control_plane_healthy",
			Help:      "1 if proxy control plane is responsive within configured timeout, 0 otherwise",
		},
	)
	Metrics.GaugeDiskIOHealthy = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Namespace: ns,
			Name:      "disk_io_healthy",
			Help:      "1 if disk reads/writes for state persistence complete within configured timeout, 0 otherwise",
		},
	)
	Metrics.GaugeAllEndpointsDown = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: ns,
			Name:      "all_endpoints_down_errors",
			Help:      "1 if all endpoints for a namespace are down, 0 otherwise",
		},
		[]string{"namespace"},
	)
	Metrics.CountDroveAppSyncErrors = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: ns,
			Name:      "drove_app_sync_failed",
			Help:      "Total number of failed Drove app syncs",
		},
		[]string{"namespace"},
	)
	Metrics.CountDroveStreamErrors = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: ns,
			Name:      "drove_stream_errors",
			Help:      "Total number of Drove stream errors",
		},
		[]string{"namespace"},
	)
	Metrics.CountDroveStreamNoDataWarnings = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: ns,
			Name:      "drove_stream_no_data_warnings",
			Help:      "Total number of warnings about no data in Drove stream",
		},
		[]string{"namespace"},
	)
	Metrics.CountDroveEventsReceived = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: ns,
			Name:      "drove_events_received",
			Help:      "Total number of received Drove events",
		},
		[]string{"namespace"},
	)
	Metrics.HistogramAppRefreshDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: ns,
			Name:      "drove_app_refresh_duration",
			Help:      "Drove app refresh duration",
			Buckets:   prometheus.ExponentialBuckets(0.05, 2, 10),
		},
		[]string{"namespace"},
	)
	Metrics.GaugeAppsWithRoutingTag = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: ns,
			Name:      "apps_with_routing_tag_total",
			Help:      "Total number of apps that have the configured routing tag.",
		},
		[]string{"namespace"},
	)
	Metrics.GaugeAppsWithoutRoutingTag = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: ns,
			Name:      "apps_without_routing_tag_total",
			Help:      "Total number of apps that do not have the configured routing tag.",
		},
		[]string{"namespace"},
	)
	Metrics.GaugeAppsIgnoredByRealm = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: ns,
			Name:      "apps_ignored_by_realm_total",
			Help:      "Total number of apps ignored due to realm mismatch",
		},
		[]string{"namespace"},
	)
	Metrics.HaproxyAPICallsSuccessful = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: ns,
			Name:      "haproxy_api_calls_successful_total",
			Help:      "Total number of successful HAProxy API calls.",
		},
		[]string{"operation"},
	)
	Metrics.HaproxyAPICallsFailed = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: ns,
			Name:      "haproxy_api_calls_failed_total",
			Help:      "Total number of failed HAProxy API calls.",
		},
		[]string{"operation"},
	)
	Metrics.NginxAPICallsSuccessful = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: ns,
			Name:      "nginx_api_calls_successful_total",
			Help:      "Total number of successful Nginx API calls.",
		},
		[]string{"operation"},
	)
	Metrics.NginxAPICallsFailed = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: ns,
			Name:      "nginx_api_calls_failed_total",
			Help:      "Total number of failed Nginx API calls.",
		},
		[]string{"operation"},
	)
	Metrics.GaugeConfiguredEndpoints = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: ns,
			Name:      "configured_endpoints_total",
			Help:      "Total number of endpoints configured for a namespace.",
		},
		[]string{"namespace"},
	)
	Metrics.GaugeHealthyEndpoints = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: ns,
			Name:      "healthy_endpoints_total",
			Help:      "Total number of healthy endpoints for a namespace.",
		},
		[]string{"namespace"},
	)
	Metrics.HaproxyReconcileAllBackendsDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "haproxy_runtime_api_duration_seconds",
			Help:    "Duration of haproxyRuntimeAPI in seconds.",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"result"},
	)
	Metrics.NginxPlusReconcileAllBackendsDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "nginx_plus_runtime_api_duration_seconds",
			Help:    "Duration of nginxPlusRuntimeAPI in seconds.",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"result"},
	)
	Metrics.TemplateRenderDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: ns,
			Name:      "template_render_duration_seconds",
			Help:      "Duration taken to render the config from template in seconds.",
			Buckets:   prometheus.DefBuckets,
		},
		[]string{"result"},
	)

	prometheus.MustRegister(Metrics.CountFailedReloads)
	prometheus.MustRegister(Metrics.CountSuccessfulReloads)
	prometheus.MustRegister(Metrics.CountProxyControlPlaneTimeouts)
	prometheus.MustRegister(Metrics.HistogramReloadDuration)
	prometheus.MustRegister(Metrics.CountInvalidSubdomainLabelWarnings)
	prometheus.MustRegister(Metrics.CountDuplicateSubdomainLabelWarnings)
	prometheus.MustRegister(Metrics.CountEndpointCheckFails)
	prometheus.MustRegister(Metrics.CountEndpointDownErrors)
	prometheus.MustRegister(Metrics.GaugeAllEndpointsDown)
	prometheus.MustRegister(Metrics.CountDroveAppSyncErrors)
	prometheus.MustRegister(Metrics.CountDroveStreamErrors)
	prometheus.MustRegister(Metrics.CountDroveStreamNoDataWarnings)
	prometheus.MustRegister(Metrics.CountDroveEventsReceived)
	prometheus.MustRegister(Metrics.HistogramAppRefreshDuration)
	prometheus.MustRegister(Metrics.GaugeAppsWithRoutingTag)
	prometheus.MustRegister(Metrics.GaugeAppsWithoutRoutingTag)
	prometheus.MustRegister(Metrics.GaugeAppsIgnoredByRealm)
	prometheus.MustRegister(Metrics.HaproxyAPICallsSuccessful)
	prometheus.MustRegister(Metrics.HaproxyAPICallsFailed)
	prometheus.MustRegister(Metrics.NginxAPICallsSuccessful)
	prometheus.MustRegister(Metrics.NginxAPICallsFailed)
	prometheus.MustRegister(Metrics.GaugeConfiguredEndpoints)
	prometheus.MustRegister(Metrics.GaugeHealthyEndpoints)
	prometheus.MustRegister(Metrics.HaproxyReconcileAllBackendsDuration)
	prometheus.MustRegister(Metrics.NginxPlusReconcileAllBackendsDuration)
	prometheus.MustRegister(Metrics.TemplateRenderDuration)
	prometheus.MustRegister(Metrics.GaugeDiskIOHealthy)

}

func observeAppRefreshTimeMetric(namespace string, e time.Duration) {
	Metrics.HistogramAppRefreshDuration.WithLabelValues(namespace).Observe(float64(e) / float64(time.Second))
}
