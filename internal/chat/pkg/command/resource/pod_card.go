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

package resource

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mattn/go-runewidth"

	"github.com/onexstack/onexai/internal/chat/pkg/larkcard"
)

const (
	// podCardTTL bounds how long a rendered /pod card can still be clicked. A
	// card outlives the answer it was drawn from, and the snapshot behind it
	// would otherwise be kept for the life of the process.
	podCardTTL = 30 * time.Minute
	// podCardMaxEntries bounds the snapshots one process holds. A snapshot is
	// bounded by maxMatchedPods pods, so this caps the memory a burst of /pod
	// queries can pin down.
	podCardMaxEntries = 64
	// podTablePageSize is how many rows the native table shows at a time. The
	// platform accepts 1–10; the client paginates the rest on its own.
	podTablePageSize = 10
	// podTableFieldHeader is the key column's header. The column is sized to hold
	// it too, so it is spelled once.
	podTableFieldHeader = "字段"

	// The key column is sized to its content, and the number has to be guessed:
	// the platform takes a pixel width, not a character count. These estimate a
	// half-width character at the table's font size, add the cell's own padding,
	// and keep the result inside what the platform accepts ([80, 600] px) and
	// inside what a phone-width card can spare.
	podKeyColumnCharPx    = 7
	podKeyColumnPaddingPx = 16
	podKeyColumnMinPx     = 80
	// A key column wider than this leaves too little of a phone-width card for the
	// values; a longer key wraps instead, which row_height auto absorbs.
	podKeyColumnMaxPx = 240

	// PodCardActionPrefix is what a select option carries inside its "nav:"
	// value, e.g. "nav:pod:sel:<token>:0". The "nav:" part is what routes it to
	// the card navigation handler; this is the body that handler sees. It is
	// exported because the card layer has to recognize it before treating an
	// unrecognized action as a command name.
	PodCardActionPrefix = "pod:"
	podActionSelect     = "sel"
)

// podSnapshot is one /pod answer kept in memory so a card click can redraw the
// card without re-querying Prometheus. The token identifies it in the card's
// callback values.
//
// The snapshot is per-process, like the card-message registry the card layer
// already keeps: a callback that lands on another replica finds nothing and is
// told the result expired rather than shown a card it cannot fill.
type podSnapshot struct {
	token   string
	pods    []*PodMetrics
	created time.Time
}

// podCardStore is a small TTL- and size-bounded map of pod snapshots.
type podCardStore struct {
	mu    sync.Mutex
	m     map[string]*podSnapshot
	order []string
}

// podCards is the process-wide snapshot store /pod renders from and card
// callbacks read back.
var podCards = &podCardStore{m: make(map[string]*podSnapshot)}

// put stores a freshly enriched pod list and returns a snapshot whose token the
// card carries.
func (s *podCardStore) put(pods []*PodMetrics) *podSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()

	snap := &podSnapshot{
		token:   newPodCardToken(),
		pods:    pods,
		created: time.Now(),
	}
	s.m[snap.token] = snap
	s.order = append(s.order, snap.token)
	for len(s.order) > podCardMaxEntries {
		oldest := s.order[0]
		s.order = s.order[1:]
		delete(s.m, oldest)
	}
	return snap
}

// get returns the snapshot a token names. An expired entry is a miss: the pod
// list it holds is a stale answer, and drawing it would report metric values
// the operator cannot tell are old.
func (s *podCardStore) get(token string) (*podSnapshot, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	snap, ok := s.m[token]
	if !ok {
		return nil, false
	}
	if time.Since(snap.created) > podCardTTL {
		delete(s.m, token)
		return nil, false
	}
	return snap, true
}

// newPodCardToken returns a short random token. It is opaque on purpose: it
// names an in-memory answer, not anything a user should read or construct.
func newPodCardToken() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand does not fail on the platforms this runs on; falling back
		// to the clock keeps a card clickable rather than panicking.
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(b[:])
}

// HandlePodCardAction renders the next /pod card for a "pod:sel:<token>:<index>"
// action, making it the one entry point the card layer calls. The second return
// reports whether the action belonged to this card at all: a caller that gets
// false must fall through to its own handling, while a malformed or expired pod
// card is answered with a card of its own rather than silently dropped.
func HandlePodCardAction(ctx context.Context, action string) (*larkcard.Card, bool) {
	return handlePodCardAction(ctx, podCards, action)
}

// handlePodCardAction is HandlePodCardAction against a given store, so a test
// can drive it without the process-wide snapshots.
func handlePodCardAction(ctx context.Context, store *podCardStore, action string) (*larkcard.Card, bool) {
	if !strings.HasPrefix(action, PodCardActionPrefix) {
		return nil, false
	}

	selected, token, ok := parsePodSelectAction(action)
	if !ok {
		slog.InfoContext(ctx, "unrecognized pod card action", "action", action)
		return expiredPodCard(), true
	}

	snap, found := store.get(token)
	if !found {
		slog.InfoContext(ctx, "pod card action references an expired result",
			"token", token)
		return expiredPodCard(), true
	}
	return renderPodCard(snap, selected), true
}

// parsePodSelectAction parses "pod:sel:<token>:<index>".
func parsePodSelectAction(action string) (selected int, token string, ok bool) {
	parts := strings.Split(action, ":")
	if len(parts) != 4 || parts[0] != "pod" || parts[1] != podActionSelect || parts[2] == "" {
		return 0, "", false
	}
	idx, err := strconv.Atoi(parts[3])
	if err != nil || idx < 0 {
		return 0, "", false
	}
	return idx, parts[2], true
}

// renderPodCard draws the /pod answer for one selected pod: a selector over the
// matched pods and the pod's fields as a two-column table. It is the only place
// this card's shape is built, so the command's first draw and every card click
// render the same thing.
func renderPodCard(snap *podSnapshot, selected int) *larkcard.Card {
	if snap == nil || len(snap.pods) == 0 {
		return expiredPodCard()
	}
	if selected < 0 || selected >= len(snap.pods) {
		selected = 0
	}

	pod := snap.pods[selected]
	// No card header: the pod's identity and status are the table's first rows,
	// so a title bar would only repeat them above the fold.
	cb := larkcard.NewCard()

	// The selector is what makes a many-pod match navigable: one option per
	// pod, each carrying the snapshot index it selects.
	if len(snap.pods) > 1 {
		options := make([]larkcard.CardSelectOption, 0, len(snap.pods))
		for i, p := range snap.pods {
			options = append(options, larkcard.CardSelectOption{
				Text:  podSelectLabel(p),
				Value: podSelectAction(snap.token, i),
			})
		}
		cb.Select("选择 Pod", options, podSelectAction(snap.token, selected))
	}

	fields := podKVFields(pod)
	rows := make([]map[string]any, 0, len(fields))
	for _, f := range fields {
		rows = append(rows, map[string]any{"field": f.Key, "value": f.Value})
	}
	// A native table: the platform draws the header, freezes the key column and
	// pages the rows on the client, so this card carries no page controls. The key
	// column asks for exactly the width its widest key needs; the value column
	// asks for none, which is how the rest of the card is left to it.
	cb.Table(larkcard.CardTable{
		Columns: []larkcard.CardTableColumn{
			{Name: "field", DisplayName: podTableFieldHeader, DataType: "text",
				Width: podKeyColumnWidth(podTableFieldHeader, fields), HorizontalAlign: "left"},
			{Name: "value", DisplayName: "值", DataType: "lark_md", HorizontalAlign: "left"},
		},
		Rows:              rows,
		PageSize:          podTablePageSize,
		RowHeight:         "auto",
		FreezeFirstColumn: true,
	})

	if len(snap.pods) > 1 {
		cb.Note("下拉可切换 Pod")
	}
	return cb.Build()
}

// podKeyColumnWidth sizes the key column to the widest key it has to hold — the
// header counts, since the column carries it too. A key is measured in half-width
// units (go-runewidth, because the keys are mostly Chinese and a byte or rune
// count would size a Chinese key like Pod), converted to the pixels the platform takes, and
// clamped to a width that is neither below the platform's minimum nor so wide
// that the values lose their room. The value column is deliberately given no
// width: what this column does not take, the client spends on that one.
func podKeyColumnWidth(header string, fields []podField) string {
	units := runewidth.StringWidth(header)
	for _, f := range fields {
		units = max(units, runewidth.StringWidth(f.Key))
	}
	px := min(max(units*podKeyColumnCharPx+podKeyColumnPaddingPx, podKeyColumnMinPx), podKeyColumnMaxPx)
	return strconv.Itoa(px) + "px"
}

// expiredPodCard is what a click on a card this process can no longer fill is
// answered with. It says what to do rather than what went wrong, and carries no
// controls: the selector it replaces would fail the same way.
func expiredPodCard() *larkcard.Card {
	return larkcard.NewCard().
		Markdown("这次查询结果已失效，请重新执行 `/pod`。").
		Build()
}

// podSelectAction is the callback value a selector option carries.
func podSelectAction(token string, index int) string {
	return fmt.Sprintf("nav:%s%s:%s:%d", PodCardActionPrefix, podActionSelect, token, index)
}

// podSelectLabel names one pod in the selector. The namespace and cluster are
// what tell two pods with the same name apart.
func podSelectLabel(p *PodMetrics) string {
	label := p.PodName
	if p.Namespace != "" {
		label = p.Namespace + "/" + label
	}
	if p.ClusterName != "" {
		label += " (" + p.ClusterName + ")"
	}
	return label
}
