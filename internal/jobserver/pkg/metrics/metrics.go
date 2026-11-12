package metrics

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

type Metrics struct {
	Meter metric.Meter

	// Job metrics
	JobExecutionTotal   metric.Int64Counter
	JobExecutionFailure metric.Int64Counter

	// CronJob metrics
	CronJobExecutionTotal   metric.Int64Counter
	CronJobExecutionFailure metric.Int64Counter
}

var M *Metrics

// Init initializes OpenTelemetry metrics and custom business metrics
func Init(scope string) error {
	meter := otel.Meter(scope + ".metrics")

	// Job metrics
	totalCounter, _ := meter.Int64Counter("bke_job_execution_total",
		metric.WithDescription("Total number of job executions"))

	failureCounter, _ := meter.Int64Counter("bke_job_execution_failure_total",
		metric.WithDescription("Total number of failed job executions"))

	// CronJob metrics
	cronTotalCounter, _ := meter.Int64Counter("bke_cronjob_execution_total",
		metric.WithDescription("Total number of cron job executions"))

	cronFailureCounter, _ := meter.Int64Counter("bke_cronjob_execution_failure_total",
		metric.WithDescription("Total number of failed cron job executions"))

	// Assign global instance
	M = &Metrics{
		Meter:                   meter,
		JobExecutionTotal:       totalCounter,
		JobExecutionFailure:     failureCounter,
		CronJobExecutionTotal:   cronTotalCounter,
		CronJobExecutionFailure: cronFailureCounter,
	}

	return nil
}

// RecordJobExecution records a job execution (both success and failure)
func (m *Metrics) RecordJobExecution(ctx context.Context, resourceType, resourceID string) {
	m.JobExecutionTotal.Add(ctx, 1, metric.WithAttributes(
		attribute.String("resource.type", resourceType),
		attribute.String("resource.id", resourceID),
	))
}

// RecordJobFailure records a failed job execution
func (m *Metrics) RecordJobFailure(ctx context.Context, resourceType, resourceID, err string) {
	m.JobExecutionFailure.Add(ctx, 1, metric.WithAttributes(
		attribute.String("resource.type", resourceType),
		attribute.String("resource.id", resourceID),
		attribute.String("error", err),
	))
}

// RecordCronJobExecution records a cron job execution
func (m *Metrics) RecordCronJobExecution(ctx context.Context, cronJobID string) {
	m.CronJobExecutionTotal.Add(ctx, 1, metric.WithAttributes(
		attribute.String("resource.type", "cronjob"),
		attribute.String("resource.id", cronJobID),
	))
}

// RecordCronJobFailure records a failed cron job execution
func (m *Metrics) RecordCronJobFailure(ctx context.Context, cronJobID string, err string) {
	m.CronJobExecutionFailure.Add(ctx, 1, metric.WithAttributes(
		attribute.String("resource.type", "cronjob"),
		attribute.String("resource.id", cronJobID),
		attribute.String("error", err),
	))
}
