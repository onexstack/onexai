// Copyright 2022 Lingfei Kong <colin404@foxmail.com>
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
//
// The original repo for this file is https://github.com/onexstack/onex.
//

package latest

import (
	"github.com/onexstack/onexai/internal/controller/apis/config"
	"github.com/onexstack/onexai/internal/controller/apis/config/scheme"
	"github.com/onexstack/onexai/internal/controller/apis/config/v1beta1"
)

// Default creates a default configuration of the latest versioned type.
// This function needs to be updated whenever we bump the miner controller's component config version.
func Default() (*config.OneXControllerManagerConfiguration, error) {
	versioned := v1beta1.OneXControllerManagerConfiguration{}

	scheme.Scheme.Default(&versioned)
	cfg := config.OneXControllerManagerConfiguration{}
	if err := scheme.Scheme.Convert(&versioned, &cfg, nil); err != nil {
		return nil, err
	}

	// We don't set this field in internal/controller/apis/config/{version}/conversion.go
	// because the field will be cleared later by API machinery during
	// conversion. See MinerControllerConfiguration internal type definition for
	// more details.
	cfg.TypeMeta.APIVersion = v1beta1.SchemeGroupVersion.String()
	return &cfg, nil
}
