// Copyright 2026 Lingfei Kong <colin404@foxmail.com>
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package agent

import (
	"context"
	"errors"

	"github.com/onexstack/onexstack/pkg/server"
)

// multiServer runs the public and internal listeners as one server. The
// service keeps one lifecycle: both listeners start together and stop
// together, and the first fatal error stops both.
type multiServer struct {
	servers []server.Server
}

var _ server.Server = (*multiServer)(nil)

// Run starts every listener and blocks until one fails or ctx is canceled.
func (m *multiServer) Run(ctx context.Context) error {
	if len(m.servers) == 0 {
		return nil
	}
	errCh := make(chan error, len(m.servers))
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	for _, srv := range m.servers {
		go func(srv server.Server) { errCh <- srv.Run(runCtx) }(srv)
	}
	for {
		select {
		case err := <-errCh:
			if err != nil {
				cancel()
			}
			return err
		case <-ctx.Done():
			return nil
		}
	}
}

// GracefulStop stops every listener.
func (m *multiServer) GracefulStop(ctx context.Context) error {
	var errs []error
	for _, srv := range m.servers {
		if err := srv.GracefulStop(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
