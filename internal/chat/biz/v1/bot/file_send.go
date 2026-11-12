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
	"bytes"
	"context"
	"fmt"
	"strings"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"

	v1 "github.com/onexstack/onexai/pkg/api/chat/v1"
)

// SendImage uploads raw image bytes and sends them to the receiver as an image
// message.
func (b *botBiz) SendImage(ctx context.Context, recv *v1.Receiver, data []byte) error {
	imageKey, err := b.uploadImageKey(ctx, data)
	if err != nil {
		return err
	}
	content, err := (&larkim.MessageImage{ImageKey: imageKey}).String()
	if err != nil {
		return fmt.Errorf("build image message: %w", err)
	}
	return b.sendMediaMessage(ctx, recv, larkim.MsgTypeImage, content)
}

// SendFile uploads raw file bytes and sends them to the receiver, choosing the
// message type from the file name and MIME type (file / audio / video).
func (b *botBiz) SendFile(ctx context.Context, recv *v1.Receiver, fileName, mimeType string, data []byte) error {
	if fileName == "" {
		fileName = "attachment"
	}
	fileType := detectFeishuFileType(mimeType, fileName)
	fileKey, err := b.uploadFileKey(ctx, fileType, fileName, data)
	if err != nil {
		return err
	}

	msgType := detectFeishuFileMessageType(fileType)
	content, err := buildFeishuFileMessageContent(msgType, fileKey)
	if err != nil {
		return fmt.Errorf("build file message: %w", err)
	}
	return b.sendMediaMessage(ctx, recv, msgType, content)
}

// sendMediaMessage sends a media (image/file/audio/video) message, replying in
// thread when the receiver carries a triggering message ID.
func (b *botBiz) sendMediaMessage(ctx context.Context, recv *v1.Receiver, msgType, content string) error {
	_, err := b.sendMessage(ctx, recv, msgType, content)
	return err
}

// uploadImageKey uploads image bytes via Im.Image.Create and returns the key.
func (b *botBiz) uploadImageKey(ctx context.Context, data []byte) (string, error) {
	var imageKey string
	err := b.retrier.Do(ctx, "upload image", func(client *lark.Client, options ...larkcore.RequestOptionFunc) error {
		req := larkim.NewCreateImageReqBuilder().
			Body(larkim.NewCreateImageReqBodyBuilder().
				ImageType("message").
				Image(bytes.NewReader(data)).
				Build()).
			Build()
		resp, err := client.Im.Image.Create(ctx, req, options...)
		if err != nil {
			return fmt.Errorf("upload image: %w", err)
		}
		if !resp.Success() {
			return fmt.Errorf("upload image code=%d msg=%s", resp.Code, resp.Msg)
		}
		if resp.Data == nil || resp.Data.ImageKey == nil {
			return fmt.Errorf("upload image: no image_key returned")
		}
		imageKey = *resp.Data.ImageKey
		return nil
	})
	if err != nil {
		return "", err
	}
	return imageKey, nil
}

// uploadFileKey uploads file bytes via Im.File.Create and returns the key.
func (b *botBiz) uploadFileKey(ctx context.Context, fileType, fileName string, data []byte) (string, error) {
	var fileKey string
	err := b.retrier.Do(ctx, "upload file", func(client *lark.Client, options ...larkcore.RequestOptionFunc) error {
		req := larkim.NewCreateFileReqBuilder().
			Body(larkim.NewCreateFileReqBodyBuilder().
				FileType(fileType).
				FileName(fileName).
				File(bytes.NewReader(data)).
				Build()).
			Build()
		resp, err := client.Im.File.Create(ctx, req, options...)
		if err != nil {
			return fmt.Errorf("upload file: %w", err)
		}
		if !resp.Success() {
			return fmt.Errorf("upload file code=%d msg=%s", resp.Code, resp.Msg)
		}
		if resp.Data == nil || resp.Data.FileKey == nil {
			return fmt.Errorf("upload file: no file_key returned")
		}
		fileKey = *resp.Data.FileKey
		return nil
	})
	if err != nil {
		return "", err
	}
	return fileKey, nil
}

// detectFeishuFileType maps a MIME type / file name to a Lark file upload type.
func detectFeishuFileType(mimeType, fileName string) string {
	name := strings.ToLower(fileName)
	switch {
	case mimeType == "application/pdf" || strings.HasSuffix(name, ".pdf"):
		return larkim.CreateFileFileTypePdf
	case strings.HasSuffix(name, ".doc") || strings.HasSuffix(name, ".docx"):
		return larkim.CreateFileFileTypeDoc
	case strings.HasSuffix(name, ".xls") || strings.HasSuffix(name, ".xlsx") || strings.HasSuffix(name, ".csv"):
		return larkim.CreateFileFileTypeXls
	case strings.HasSuffix(name, ".ppt") || strings.HasSuffix(name, ".pptx"):
		return larkim.CreateFileFileTypePpt
	// Lark's file API only has "mp4" as the video type. Map common video MIME
	// types and extensions to mp4 so the message renders as a native player.
	case strings.HasPrefix(mimeType, "video/") ||
		strings.HasSuffix(name, ".mp4") || strings.HasSuffix(name, ".mov") ||
		strings.HasSuffix(name, ".avi") || strings.HasSuffix(name, ".m4v") ||
		strings.HasSuffix(name, ".mkv") || strings.HasSuffix(name, ".webm"):
		return larkim.CreateFileFileTypeMp4
	case mimeType == "audio/ogg" || mimeType == "audio/opus" || mimeType == "application/ogg" ||
		strings.HasSuffix(name, ".ogg") || strings.HasSuffix(name, ".opus"):
		return larkim.CreateFileFileTypeOpus
	default:
		return larkim.CreateFileFileTypeStream
	}
}

// detectFeishuFileMessageType maps a Lark file upload type to the message type
// used to send it.
func detectFeishuFileMessageType(fileType string) string {
	switch fileType {
	case larkim.CreateFileFileTypeOpus:
		return larkim.MsgTypeAudio
	case larkim.CreateFileFileTypeMp4:
		return larkim.MsgTypeMedia
	default:
		return larkim.MsgTypeFile
	}
}

// buildFeishuFileMessageContent serializes the message content for a file /
// audio / media message.
func buildFeishuFileMessageContent(msgType, fileKey string) (string, error) {
	switch msgType {
	case larkim.MsgTypeAudio:
		return (&larkim.MessageAudio{FileKey: fileKey}).String()
	case larkim.MsgTypeMedia:
		return (&larkim.MessageMedia{FileKey: fileKey}).String()
	default:
		return (&larkim.MessageFile{FileKey: fileKey}).String()
	}
}
