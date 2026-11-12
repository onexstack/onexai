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

package bot

import (
	"testing"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

func TestDetectFeishuFileType(t *testing.T) {
	tests := []struct {
		name     string
		mime     string
		fileName string
		want     string
	}{
		{"pdf by mime", "application/pdf", "x", larkim.CreateFileFileTypePdf},
		{"doc by ext", "", "a.docx", larkim.CreateFileFileTypeDoc},
		{"xls by ext", "", "b.csv", larkim.CreateFileFileTypeXls},
		{"ppt by ext", "", "c.pptx", larkim.CreateFileFileTypePpt},
		{"video by mime", "video/mp4", "", larkim.CreateFileFileTypeMp4},
		{"video by ext", "", "d.mov", larkim.CreateFileFileTypeMp4},
		{"audio opus by mime", "audio/opus", "", larkim.CreateFileFileTypeOpus},
		{"default stream", "", "unknown.bin", larkim.CreateFileFileTypeStream},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := detectFeishuFileType(tt.mime, tt.fileName); got != tt.want {
				t.Errorf("detectFeishuFileType(%q,%q) = %q, want %q", tt.mime, tt.fileName, got, tt.want)
			}
		})
	}
}

func TestDetectFeishuFileMessageType(t *testing.T) {
	tests := []struct {
		fileType string
		want     string
	}{
		{larkim.CreateFileFileTypeOpus, larkim.MsgTypeAudio},
		{larkim.CreateFileFileTypeMp4, larkim.MsgTypeMedia},
		{larkim.CreateFileFileTypePdf, larkim.MsgTypeFile},
		{larkim.CreateFileFileTypeStream, larkim.MsgTypeFile},
	}
	for _, tt := range tests {
		if got := detectFeishuFileMessageType(tt.fileType); got != tt.want {
			t.Errorf("detectFeishuFileMessageType(%q) = %q, want %q", tt.fileType, got, tt.want)
		}
	}
}
