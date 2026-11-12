package clientset

import (
	"github.com/onexstack/onexai/internal/mqserver/pkg/clientset/typed/fake"
	"github.com/onexstack/onexai/internal/mqserver/pkg/clientset/typed/oss"
)

// Interface defines the operations for accessing different client types within the clientset.
type Interface interface {
	// Fake returns the client interface for managing fake resources.
	Fake() fake.Interface
	// Oss returns the client interface for managing oss resources.
	Oss() oss.Interface
}

// Clientset provides a unified entry point to access various typed clients.
type Clientset struct {
	fake fake.Interface
	oss  oss.Interface
}

// New creates a new Clientset with the provided client interfaces.
// It acts as a constructor for the Clientset type.
func New(
	fake fake.Interface,
	oss oss.Interface,
) *Clientset {
	return &Clientset{
		fake: fake,
		oss:  oss,
	}
}

// Fake returns the client interface for fake resources.
func (c *Clientset) Fake() fake.Interface {
	return c.fake
}

// Oss returns the client interface for oss resources.
func (c *Clientset) Oss() oss.Interface {
	return c.oss
}
