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

package media

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/jpeg"
	"image/png"
	"math"
	"net/http"
	"strings"

	xdraw "golang.org/x/image/draw"

	// The decoders are registered for their side effect: image.DecodeConfig
	// only knows a format if its package has been linked in, and a format that
	// is not linked in reports "unknown format" for a file this service
	// accepted. They are listed here, beside the sniffing, so that adding a
	// type to Kinds' allow-list and forgetting its decoder is a change that
	// reads as one edit.
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	_ "golang.org/x/image/webp"
)

// sniffLen is how many leading bytes DetectContentType examines. It is the
// same 512 that net/http documents, restated here because the sniffing and the
// reading have to agree about how much is enough.
const sniffLen = 512

// SniffImageType reports the media type of an image, from its bytes.
//
// # Why the bytes and not the declaration
//
// A declared content type is a claim by whoever sent the file, and the whole
// point of an allow-list is that the sender is not trusted to name what they
// sent. A file declared image/png that is really an HTML document would pass a
// declaration check and be served back to a browser later. So the type that is
// validated and stored is the one sniffed here.
//
// # Why the declaration is still consulted
//
// http.DetectContentType answers "application/octet-stream" for any binary
// format it has no signature for — TIFF and HEIC among them. Falling through to
// the declaration in exactly that case is what keeps a real image from being
// refused for the sin of being a format the standard library does not know. It
// is a fallback, not an equal: a sniffed answer always wins, so a text file
// named .png is called text/plain and rejected on its own merits.
//
// There is no error return. The only unusable outcome is an empty string, and
// it is more useful to hand that back for the caller's allow-list to reject by
// name than to raise it as a second, differently-worded failure here.
func SniffImageType(data []byte, declared string) string {
	sniffed := NormalizeContentType(sniffedType(data))
	if sniffed != "" && sniffed != "application/octet-stream" {
		return sniffed
	}
	if declared = NormalizeContentType(declared); declared != "" {
		return declared
	}
	return sniffed
}

// sniffedType reads the leading bytes without over-reading a short file.
func sniffedType(data []byte) string {
	if len(data) > sniffLen {
		data = data[:sniffLen]
	}
	return http.DetectContentType(data)
}

// ImageSize reports an image's pixel dimensions.
//
// It reads only the header, so it is cheap enough to call on every upload, and
// it never fails an upload: a file whose dimensions cannot be read is still a
// file whose bytes and type were validated, and the dimensions are metadata
// rather than a gate. A format with no decoder linked in lands here too, which
// is why this returns zeros rather than an error.
func ImageSize(data []byte) (width, height int) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return 0, 0
	}
	return cfg.Width, cfg.Height
}

// DataURL renders bytes as the data URL a model endpoint expects.
//
// # Why the parameters, and not the line protocol's MIME string
//
// A data URL's media type is followed by ";base64,". Building it in one place
// keeps the separator from being spelled differently at each call site, which
// is the kind of difference that fails only for the second provider.
func DataURL(contentType string, data []byte) string {
	ct := NormalizeContentType(contentType)
	if ct == "" || !strings.HasPrefix(ct, "image/") {
		ct = "application/octet-stream"
	}
	return "data:" + ct + ";base64," + base64.StdEncoding.EncodeToString(data)
}

// DefaultMaxImagePixels is the ceiling above which an image is shrunk before a
// model is shown it, when a deployment asks for shrinking at all.
//
// It is the long-standing guidance for vision endpoints — roughly 1.15 megapixels,
// or a little over a 1500px square — and it is the point past which the extra
// detail stops changing an answer while the payload and the token bill keep
// growing. It is a default for the switch rather than the switch itself: a
// deployment that wants the original pixels sets the limit to zero.
const DefaultMaxImagePixels = 1568 * 1568

// Resize shrinks an image so that it has at most maxPixels pixels, and reports
// the media type of the result.
//
// # What it is for
//
// The model edge inlines an image as base64 and re-sends it on every turn that
// references it, so the bytes are paid for repeatedly. A 4K screenshot is four
// megabytes of context that a model does not read any better than the same
// picture at a megapixel.
//
// # What it costs
//
// It is a lossy step: a downscaled screenshot can lose small text, which is
// exactly the detail a support bot is often sent. So it is off unless a
// deployment asks for it — see the caller, which passes zero to disable.
//
// A maxPixels of zero or less returns the input unchanged, which is how the
// disabled case is spelled. An image already inside the limit is returned as
// the same slice rather than re-encoded: re-encoding a JPEG to say nothing is
// a loss with no benefit.
func Resize(data []byte, maxPixels int) ([]byte, string, error) {
	contentType := SniffImageType(data, "")
	if maxPixels <= 0 {
		return data, contentType, nil
	}

	img, format, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		// An image this cannot decode is one it cannot shrink. Handing back the
		// original is the right answer: the model edge's job is to send
		// something, and a scaling that failed is not a reason to send nothing.
		return data, contentType, nil
	}

	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 || w*h <= maxPixels {
		return data, contentType, nil
	}

	// Scale by the ratio of the square roots, so the area lands on the limit
	// while the shape is preserved. Rounded down, so the result is inside the
	// ceiling rather than a rounding step over it — the caller asked for a
	// bound, and a bound that is exceeded by a few percent is not one.
	scale := math.Sqrt(float64(maxPixels) / float64(w*h))
	nw := int(float64(w) * scale)
	nh := int(float64(h) * scale)
	if nw < 1 {
		nw = 1
	}
	if nh < 1 {
		nh = 1
	}

	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	// CatmullRom is the slow one and the good one: shrinking with a box filter
	// aliases exactly the fine detail — small text — that the picture was sent
	// for. This runs once per image per turn, not once per token.
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), img, b, xdraw.Over, nil)

	var buf bytes.Buffer
	outType := contentType
	switch format {
	case "png":
		if err := png.Encode(&buf, dst); err != nil {
			return data, contentType, nil
		}
		outType = "image/png"
	case "jpeg":
		if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 85}); err != nil {
			return data, contentType, nil
		}
		outType = "image/jpeg"
	default:
		// Everything else — webp and gif among them — has no encoder linked in
		// (x/image/webp decodes and does not encode), so the result is written
		// as JPEG. It is the smaller of the two formats that can be written, and
		// the alternative is returning the oversized original.
		if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 85}); err != nil {
			return data, contentType, nil
		}
		outType = "image/jpeg"
	}
	return buf.Bytes(), outType, nil
}
