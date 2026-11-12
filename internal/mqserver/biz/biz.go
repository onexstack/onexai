package biz

import (
	"github.com/google/wire"

	"github.com/onexstack/onexai/internal/mqserver/pkg/clientset"
	"github.com/onexstack/onexai/internal/mqserver/store"
)

// ProviderSet declares dependency injection rules for the business logic layer.
var ProviderSet = wire.NewSet(NewBiz, wire.Bind(new(IBiz), new(*biz)))

// IBiz is a placeholder from the service scaffold. oai-mqserver has no
// subscribers yet; when one is added, it gets a real method set here and the
// handler registers it. It is deliberately empty rather than pretending a
// behaviour layer exists.
type IBiz any

// biz is the concrete implementation of the business logic IBiz.
type biz struct {
	store     store.IStore
	clientset clientset.Interface
}

// Ensure biz implements IBiz at compile time.
var _ IBiz = (*biz)(nil)

// NewBiz creates and returns a new instance of the business logic layer.
func NewBiz(store store.IStore, clientset clientset.Interface) *biz {
	return &biz{store: store, clientset: clientset}
}
