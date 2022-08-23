package perf

import (
	"context"
	"os"
	"path"
	"time"

	"cloud.google.com/go/compute/metadata"
	"contrib.go.opencensus.io/exporter/stackdriver"
	"github.com/edaniels/golog"
	"go.opencensus.io/stats/view"
	"go.opencensus.io/trace"

	"go.viam.com/utils"
)

const (
	envVarStackDriverProjectID = "STACKDRIVER_PROJECT_ID"
)

// Exporter wrapper around Trace and Metric exporter for OpenCensus.
type Exporter interface {
	// Start will start the exporting of metrics and return any errors if failed to start.
	Start() error
	// Stop will stop all exporting and flush remaining metrics.
	Stop()
}

// NewCloudExporter creates a new Stackdriver (Cloud Monitoring) OpenCensus exporter with all options setup.
func NewCloudExporter(ctx context.Context, logger golog.Logger) (Exporter, error) {
	sdOpts := stackdriver.Options{
		Context: ctx,
		OnError: func(err error) {
			logger.Errorw("opencensus stackdriver error", "error", err)
		},
		// ReportingInterval sets the frequency of reporting metrics to stackdriver backend.
		ReportingInterval: 60 * time.Second,
		MetricPrefix:      path.Join("app.viam.com", "opencensus"),
	}

	// Allow a custom stackdriver project.
	if os.Getenv(envVarStackDriverProjectID) != "" {
		sdOpts.ProjectID = os.Getenv(envVarStackDriverProjectID)
	}

	// For Cloud Run applications use
	// See: https://cloud.google.com/run/docs/container-contract#env-vars
	if os.Getenv("K_SERVICE") != "" {
		// Allow for local testing with GCP_COMPUTE_ZONE
		var err error
		zone := os.Getenv("GCP_COMPUTE_ZONE")
		if zone == "" {
			// Get from GCP Metadata
			if zone, err = metadata.Zone(); err != nil {
				return nil, err
			}
		}

		// Allow for local testing with GCP_INSTANCE_ID
		instanceID := os.Getenv("GCP_INSTANCE_ID")
		if instanceID == "" {
			// Get from GCP Metadata
			if instanceID, err = metadata.Zone(); err != nil {
				return nil, err
			}
		}

		// We're using GAE resource even though we're running on Cloud Run. GCP only allows
		// for a limited subset of resource types when creating custom metrics. The default "Global"
		// is vauge, `generic_node` is better but doesn't have built in label for version/module.
		// GAE is essentially Cloud Run application under the hood and the resource lables with the
		// the type match to Cloud Run. With a vauge resource type we need to add lables on each metric
		// which makes the UI in Cloud Monitoring a little hard to reason about the labels on the
		// metric vs resource.
		//
		// See: https://cloud.google.com/monitoring/custom-metrics/creating-metrics#create-metric-desc
		sdOpts.MonitoredResource = &gaeResource{
			projectID:  os.Getenv(envVarStackDriverProjectID),
			module:     os.Getenv("K_SERVICE"),
			version:    os.Getenv("K_REVISION"),
			instanceID: instanceID,
			location:   zone,
		}
		sdOpts.DefaultMonitoringLabels = &stackdriver.Labels{}
	}

	sd, err := stackdriver.NewExporter(sdOpts)
	if err != nil {
		return nil, err
	}

	e := sdExporter{
		sdExporter: sd,
	}

	return &e, nil
}

type sdExporter struct {
	sdExporter *stackdriver.Exporter
}

// Starts the applications stats/span monitoring. Registers views and starts trace/metric exporters to opencensus.
func (e *sdExporter) Start() error {
	if err := registerApplicationViews(); err != nil {
		return err
	}

	if err := e.sdExporter.StartMetricsExporter(); err != nil {
		return err
	}
	trace.RegisterExporter(e.sdExporter)
	trace.ApplyConfig(trace.Config{DefaultSampler: trace.AlwaysSample()})
	return nil
}

// Stop all exporting.
func (e *sdExporter) Stop() {
	e.sdExporter.StopMetricsExporter()
	trace.UnregisterExporter(e.sdExporter)
	e.sdExporter.Flush()

	if err := utils.TryClose(context.Background(), e.sdExporter); err != nil {
		golog.Global.Errorf("Failed to close Stackdriver Exporter: %s", err)
	}
}

type gaeResource struct {
	projectID  string // GCP project ID
	module     string // GAE/Cloud Run app name
	version    string // GAE/Cloud Run app version
	instanceID string // unique id for task
	location   string // GCP zone
}

func (r *gaeResource) MonitoredResource() (resType string, labels map[string]string) {
	return "gae_instance", map[string]string{
		"project_id":  r.projectID,
		"module_id":   r.module,
		"version_id":  r.version,
		"instance_id": r.instanceID,
		"location":    r.location,
	}
}

// NewDevelopmentExporter creates a new exporter that outputs the console.
func NewDevelopmentExporter() Exporter {
	return &printExporter{
		trace:  NewNiceLoggingSpanExporter(),
		metric: NewPrintExporter(),
	}
}

type printExporter struct {
	metric view.Exporter
	trace  trace.Exporter
}

// Starts the applications stats/span monitoring. Registers views and starts trace/metric exporters to console.
func (e *printExporter) Start() error {
	if err := registerApplicationViews(); err != nil {
		return err
	}

	view.RegisterExporter(e.metric)
	trace.RegisterExporter(e.trace)
	trace.ApplyConfig(trace.Config{DefaultSampler: trace.AlwaysSample()})

	return nil
}

// Stop all exporting.
func (e *printExporter) Stop() {
	view.UnregisterExporter(e.metric)
	trace.UnregisterExporter(e.trace)
}
