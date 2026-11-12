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

package v1

import (
	"encoding/json"
	"fmt"
)

// The generated enum fields are int32-backed. The handlers bind requests with
// encoding/json (gin's default), which would otherwise reject the symbolic
// names the wires send ("MODE_CALLER", "CHANNEL_NONE"). These methods make the
// enum accept either the name or the number, and marshal back to the name, so
// the contract stays typed while the JSON stays readable.

func (m *Metering_Mode) UnmarshalJSON(b []byte) error {
	v, err := enumFromJSON(b, Metering_Mode_value)
	if err != nil {
		return fmt.Errorf("metering mode: %w", err)
	}

	*m = Metering_Mode(v)
	return nil
}

func (m Metering_Mode) MarshalJSON() ([]byte, error) {
	return enumToJSON(int32(m), Metering_Mode_name)
}

func (c *Approval_Channel) UnmarshalJSON(b []byte) error {
	v, err := enumFromJSON(b, Approval_Channel_value)
	if err != nil {
		return fmt.Errorf("approval channel: %w", err)
	}

	*c = Approval_Channel(v)
	return nil
}

func (c Approval_Channel) MarshalJSON() ([]byte, error) {
	return enumToJSON(int32(c), Approval_Channel_name)
}

func (f *Approval_Fallback) UnmarshalJSON(b []byte) error {
	v, err := enumFromJSON(b, Approval_Fallback_value)
	if err != nil {
		return fmt.Errorf("approval fallback: %w", err)
	}

	*f = Approval_Fallback(v)
	return nil
}

func (f Approval_Fallback) MarshalJSON() ([]byte, error) {
	return enumToJSON(int32(f), Approval_Fallback_name)
}

// enumFromJSON accepts a symbolic name or a number.
func enumFromJSON(b []byte, names map[string]int32) (int32, error) {
	var s string

	if err := json.Unmarshal(b, &s); err == nil {
		if v, ok := names[s]; ok {
			return v, nil
		}

		return 0, fmt.Errorf("unknown value %q", s)
	}

	var n int32

	if err := json.Unmarshal(b, &n); err == nil {
		return n, nil
	}

	return 0, fmt.Errorf("value is neither a name nor a number")
}

func enumToJSON(v int32, names map[int32]string) ([]byte, error) {
	if name, ok := names[v]; ok {
		return json.Marshal(name)
	}

	return json.Marshal(v)
}
