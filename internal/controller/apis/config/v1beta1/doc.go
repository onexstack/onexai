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

// +k8s:deepcopy-gen=package
// +k8s:conversion-gen=github.com/onexstack/onexai/internal/controller/apis/config
// +k8s:conversion-gen=github.com/onexstack/onexai/pkg/config/v1beta1
// +k8s:conversion-gen=k8s.io/component-base/config/v1alpha1
// +k8s:defaulter-gen=TypeMeta
// +k8s:defaulter-gen-input=github.com/onexstack/onexai/internal/controller/apis/config/v1beta1
// +groupName=onexcontrollermanager.config.onexai.io

package v1beta1 // import "github.com/onexstack/onexai/internal/controller/apis/config/v1beta1"
