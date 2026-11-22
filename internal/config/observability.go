package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
)

// ObservabilityConfig is what the server tells the outside about itself:
// metrics for a monitoring system that scrapes them, and traces sent to a
// collector. Both are bootstrap settings: they are read before the database
// is, and they never leave the machine unless an operator turns them on.
type ObservabilityConfig struct {
	Metrics MetricsConfig `yaml:"metrics"`
	Tracing TracingConfig `yaml:"tracing"`
}

// MetricsConfig is the metrics endpoint, on a listener of its own that is
// never the one the web UI and the API use.
type MetricsConfig struct {
	// Enabled turns the endpoint on or off (default on).
	Enabled *bool `yaml:"enabled"`
	// Listen is the address of the metrics listener (default 127.0.0.1:9102).
	// The Helm chart sets it to all addresses of the pod, and lets only the
	// scraper's namespace reach it.
	Listen string `yaml:"listen"`
	// TokenEnv names the environment variable that holds a bearer token the
	// scraper must send. Empty: no token. The token itself is never in this
	// file; the chart fills the variable from a Secret.
	TokenEnv string `yaml:"token_env"`
	// NameLabels records Flight names as a label of one extra metric
	// (launchway_flight_runs_finished_total), limited to 300 series. Default off.
	NameLabels bool `yaml:"name_labels"`
}

// The environment variables the Helm chart sets instead of writing the keys into the
// configuration file (an older server, during an upgrade, refuses a key it does not know).
// A key in the file wins over the variable of the same meaning.
const (
	MetricsListenEnv     = "LAUNCHWAY_METRICS_LISTEN"
	MetricsEnabledEnv    = "LAUNCHWAY_METRICS_ENABLED"
	MetricsTokenEnv      = "LAUNCHWAY_METRICS_TOKEN"
	MetricsNameLabelsEnv = "LAUNCHWAY_METRICS_NAME_LABELS"
)

// On reports whether the metrics endpoint is on.
func (m MetricsConfig) On() bool {
	if m.Enabled != nil {
		return *m.Enabled
	}
	return os.Getenv(MetricsEnabledEnv) != "false"
}

// Address is where the metrics listener binds.
func (m MetricsConfig) Address() string {
	switch {
	case m.Listen != "":
		return m.Listen
	case os.Getenv(MetricsListenEnv) != "":
		return os.Getenv(MetricsListenEnv)
	}
	return "127.0.0.1:9102"
}

// Token reads the bearer token from the environment variable TokenEnv names
// (LAUNCHWAY_METRICS_TOKEN when none is named).
func (m MetricsConfig) Token() string {
	if m.TokenEnv != "" {
		return os.Getenv(m.TokenEnv)
	}
	return os.Getenv(MetricsTokenEnv)
}

// Names says whether Flight names may be recorded as a label.
func (m MetricsConfig) Names() bool { return m.NameLabels || os.Getenv(MetricsNameLabelsEnv) == "true" }

// TracingConfig is trace export over OTLP/HTTP (JSON encoding).
type TracingConfig struct {
	// Enabled turns trace export on (default off).
	Enabled bool `yaml:"enabled"`
	// Endpoint is the collector's OTLP/HTTP traces address, such as
	// https://collector.example.com:4318/v1/traces. The request goes through
	// the installation's egress rules like every other outbound request.
	Endpoint string `yaml:"endpoint"`
	// HeadersEnv names an environment variable holding extra request headers,
	// as "Name=value,Name2=value2" (an API key for a hosted collector). The
	// values are never in this file.
	HeadersEnv string `yaml:"headers_env"`
	// SampleRatio is the share of traces kept, 0 to 1 (default 1).
	SampleRatio *float64 `yaml:"sample_ratio"`
	// ViewerURLTemplate makes the run page link to a trace viewer: the text
	// {trace_id} is replaced with the run's trace id. Empty: the page shows the id only.
	ViewerURLTemplate string `yaml:"viewer_url_template"`
}

// Ratio is the sampling ratio in force.
func (t TracingConfig) Ratio() float64 {
	if t.SampleRatio == nil {
		return 1
	}
	return *t.SampleRatio
}

// Headers reads the extra headers from the environment variable HeadersEnv names.
func (t TracingConfig) Headers() map[string]string {
	out := map[string]string{}
	if t.HeadersEnv == "" {
		return out
	}
	for _, kv := range strings.Split(os.Getenv(t.HeadersEnv), ",") {
		if k, v, ok := strings.Cut(strings.TrimSpace(kv), "="); ok && k != "" {
			out[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return out
}

// Check validates the settings.
func (o ObservabilityConfig) Check() error {
	if l := o.Metrics.Listen; l != "" {
		if _, _, err := net.SplitHostPort(l); err != nil {
			return fmt.Errorf("observability.metrics.listen must be an address such as 127.0.0.1:9102 or :9102: %w", err)
		}
	}
	t := o.Tracing
	if r := t.Ratio(); r < 0 || r > 1 {
		return fmt.Errorf("observability.tracing.sample_ratio is a number from 0 to 1")
	}
	if t.Enabled {
		u, err := url.Parse(t.Endpoint)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("observability.tracing.endpoint must be an http or https address such as https://collector.example.com:4318/v1/traces when tracing is enabled")
		}
	}
	if v := t.ViewerURLTemplate; v != "" {
		if !strings.Contains(v, "{trace_id}") {
			return fmt.Errorf("observability.tracing.viewer_url_template must contain {trace_id}")
		}
		if u, err := url.Parse(strings.ReplaceAll(v, "{trace_id}", "0")); err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			return fmt.Errorf("observability.tracing.viewer_url_template must be an http or https address")
		}
	}
	return nil
}
