// Copyright 2024 Lingfei Kong <colin404@foxmail.com>
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

// Package printers builds the tables `kubectl get` renders for the resources
// oai-declserver serves.
//
// Those resources are not CRDs, so their columns cannot be declared in an
// additionalPrinterColumns block: each store has to carry its own
// rest.TableConvertor. The convertors live here, next to each other, so the
// column layout of the two API groups can be read in one place.
//
// A convertor always returns every column and every cell. `kubectl get -o wide`
// is a client-side filter: the client drops the columns whose Priority is not 0
// unless --wide was passed, so the server must not hide them itself.
package printers
