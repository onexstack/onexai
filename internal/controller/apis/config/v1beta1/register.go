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

package v1beta1

import (
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// GroupName is the group name use in this package.
const GroupName = "onexcontrollermanager.config.onexai.io"

// SchemeGroupVersion is group version used to register these objects.
var SchemeGroupVersion = schema.GroupVersion{Group: GroupName, Version: "v1beta1"}

var (
	SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes, addDefaultingFuncs)
	// localSchemeBuilder extends the SchemeBuilder instance with the external types. In this package,
	// defaulting and conversion init funcs are registered as well.
	localSchemeBuilder = &SchemeBuilder
	// AddToScheme is a global function that registers this API group & version to a scheme.
	AddToScheme = localSchemeBuilder.AddToScheme
)

// addKnownTypes registers known types to the given scheme.
func addKnownTypes(scheme *runtime.Scheme) error {
	scheme.AddKnownTypes(SchemeGroupVersion,
		&OneXControllerManagerConfiguration{},
	)
	return nil
}

func init() {
	// We only register manually written functions here. The registration of the
	// generated functions takes place in the generated files. The separation
	// makes the code compile even when the generated files are missing.
	localSchemeBuilder.Register(addDefaultingFuncs)
}
