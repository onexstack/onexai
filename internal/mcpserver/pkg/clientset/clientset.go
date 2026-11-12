package clientset

import (
	"sync"

	"github.com/onexstack/onexai/internal/mcpserver/pkg/clientset/typed/fake"
	"github.com/onexstack/onexai/internal/mcpserver/pkg/clientset/typed/oss"
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

var (
	// CS is the global singleton instance of the clientset interface.
	// It allows global access to the initialized clientset from legacy packages.
	CS *Clientset
	// once ensures that the clientset initialization logic executes exactly once.
	once sync.Once
)

// New creates a new Clientset with the provided client interfaces.
// It acts as a constructor for the Clientset type.
func New(
	fake fake.Interface,
	oss oss.Interface,
) *Clientset {
	once.Do(func() {
		CS = &Clientset{
			fake: fake,
			oss:  oss,
		}
	})

	return CS
}

// Fake returns the client interface for fake resources.
func (c *Clientset) Fake() fake.Interface {
	return c.fake
}

// Oss returns the client interface for oss resources.
func (c *Clientset) Oss() oss.Interface {
	return c.oss
}
