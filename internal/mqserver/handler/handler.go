package handler

import (
	"log/slog"

	"github.com/onexstack/onexai/internal/mqserver/biz"
	"github.com/onexstack/onexai/internal/mqserver/pkg/validation"
	"github.com/onexstack/onexai/internal/pkg/kafka"
)

// Handler manages the business logic for API requests and event processing.
type Handler struct {
	biz biz.IBiz
	val *validation.Validator
}

// Registrar defines a function signature for subscribing to Kafka topics.
type Registrar func(engine *kafka.Engine, h *Handler)

var registrars []Registrar

// NewHandler creates a new instance of Handler.
func NewHandler(biz biz.IBiz, val *validation.Validator) *Handler {
	return &Handler{
		biz: biz,
		val: val,
	}
}

// Register adds a new topic subscriber to the global registry.
func Register(r Registrar) {
	registrars = append(registrars, r)
}

// ApplyTo applies the registered topic subscribers to the provided kafka engine.
func (h *Handler) ApplyTo(engine *kafka.Engine) {
	for _, r := range registrars {
		r(engine, h)
	}

	slog.Info("kafka topic subscribers installed", "count", len(registrars))
}
