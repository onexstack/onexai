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

// Package oncallsummary turns one closed on-call ticket's material into a closing
// write-up (结案总结, closing summary).
//
// # Where this sits, and why it is not a command
//
// It is the material-and-prompt half of the /close summary path: it knows what to
// send and how to ask, and nothing about tickets, the store or the chat platform.
// The command that calls it owns those. The split is what lets the prompt be
// tested as a pure function and the model call be tested with a fake, rather than
// either being tested through a Lark conversation.
//
// # The write-up has two readers
//
// It is stored on the ticket as its closing record, and it is republished by
// another platform as an answer to learners who hit the same problem. So it is
// asked for in the shape a reader scanning for "what do I do" needs — fixed
// sections, the fix in the same place every time — rather than as the free prose a
// one-off report would take. See the prompt for the rest.
package oncallsummary

import (
	"fmt"
	"strings"

	chatknown "github.com/onexstack/onexai/pkg/known/chat"
)

// Section headings the answer is asked to use, in order.
//
// They are a fixed shape rather than free prose because of who reads the result
// is not only the operator: a learner scanning for the fix needs it in the same
// place every time, and a model asked for "a summary" invents a different
// structure per run.
const (
	sectionSymptom  = "问题现象"
	sectionAttempts = "排查过程"
	sectionCause    = "根因"
	sectionFix      = "解决方案"
	sectionPrevent  = "预防"
)

// UnknownMarker is what the answer must say instead of guessing.
//
// A specific string rather than "say so", so that it is greppable: an operator
// reviewing closings can find every one that admits a gap, and a write-up that
// filled one in silently is the failure this exists to make visible.
const UnknownMarker = "（群聊未明确）"

// promptHeader is the instruction. It is one constant rather than a file or a
// database row because it is a decision of this service, versioned with it: a
// wording change should be a code change reviewable next to the behaviour it
// alters.
//
// The brief-marker rule is spelled with chatknown.SummaryBrief concatenated in
// rather than typed out, because the reader that excludes those tickets matches
// on that same string — and a prompt that asked for one wording while the query
// excluded another would publish a ticket the model had declined to write up.
const promptHeader = `你是 OneX 平台的值班助理。下面是一条**已经关闭**的 OnCall 求助工单的全部材料。
请把它整理成这条工单的**结案总结**：它会作为这条工单的结案记录存档，也会被整理给后来遇到同样问题的学员看。

要求：
1. 用 Markdown 输出，严格按这五个二级标题组织，顺序不变，不要增加其它标题：
   ## ` + sectionSymptom + ` / ## ` + sectionAttempts + ` / ## ` + sectionCause + ` / ## ` + sectionFix + ` / ## ` + sectionPrevent + `
2. 只写材料里出现过的事实。材料没讲清楚的，那一条写「` + UnknownMarker + `」，不要推测、不要补充常识性的建议。
3. 不要复述对话里的寒暄、致谢、表情、机器人卡片和与问题无关的闲聊。
4. 材料里可能含有敏感信息。**绝对不要**在输出里出现：IP 地址、内网域名或主机名、账号与用户名、口令、token、密钥、证书内容、对象存储地址。需要指代它们时用「相关主机」「相关凭据」这类说法。
5. 保留材料里的命令、配置项名、报错原文和版本号——它们正是后来者要搜的东西，也是唯一能对上号的线索。
6. 不要以「好的」「以下是」开头，直接输出正文。
7. **如果这条工单不值得复盘**（纯咨询、纯确认、没有技术处置过程、或者有效信息不足三条），
   **整条回复只输出这一句**：` + chatknown.SummaryBrief + `
   不要标题、不要补充任何其它句子。`

// Material is one ticket's inputs, already rendered by the caller.
//
// Every text part arrives as a string because the caller is the process that holds
// the ticket: it knows how much of a conversation fits, which parts are missing,
// and what a note looks like. This package decides how to ask, not what to fetch.
type Material struct {
	// TicketID is carried for the caller's logging rather than for the prompt: it
	// is an opaque identifier with nothing in it a reader of the write-up needs.
	TicketID string
	// Title is what the asker wrote when raising the ticket — for `/oncall`, the
	// whole problem description in one line.
	Title string
	// Description is the elaboration the asker added, if any. Usually empty.
	Description string
	// Notes is the handling log, one entry per line. It is not recoverable from the
	// conversation: a /mark note exists nowhere else, and it is usually the closest
	// thing to a conclusion in the whole ticket.
	Notes string
	// Transcript is the ticket group's conversation, already trimmed.
	Transcript string
	// Missing names the material the caller could not obtain, so the prompt can say
	// so rather than let the model read a gap as the whole story.
	Missing []string
}

// Prompt renders the material into the single question the model answers.
//
// # Why the material is labelled rather than concatenated
//
// The parts are different kinds of thing — what was asked, what people wrote down,
// what was said — and the same sentence in the wrong part reads differently: a
// one-line note is a conclusion somebody recorded, while the same words in a chat
// log are someone thinking out loud. Unlabelled, the model has to work out which
// is which from the prose, and gets it wrong exactly where it matters.
//
// Nothing here is trimmed. The caller bounded every part; a second truncation
// would silently drop the tail of material that was measured to fit, and the
// prompt would look complete while missing its ending.
func (m Material) Prompt() string {
	var b strings.Builder
	b.WriteString(promptHeader)

	b.WriteString("\n\n## 材料\n")
	writePart(&b, "【工单标题（提问者写的原话）】", m.Title)
	writePart(&b, "【补充描述（提问者另外写的，通常为空）】", m.Description)
	writePart(&b, "【处理记录（值班人用 /mark 逐条记下的，最接近结论的一段）】", m.Notes)
	writePart(&b, "【工单群聊天记录（按时间顺序，可能被截断）】", m.Transcript)

	if len(m.Missing) > 0 {
		// Told to the model and not only logged, for the reason the reply path
		// appends its image-failure note: a write-up produced without a part it was
		// promised is indistinguishable from one that read that part and found it
		// unremarkable. Saying which parts are absent is what keeps 「群聊未明确」 (the group chat does not say)
		// honest instead of merely cautious.
		fmt.Fprintf(&b, "\n【未能取得、确实没有的材料】%s\n", strings.Join(m.Missing, "、"))
	}

	b.WriteString("\n## 你的输出\n")
	return b.String()
}

// writePart writes one labelled part, saying so when it is empty.
//
// An absent part is written as an explicit line rather than omitted, because an
// omitted heading is invisible: the model reads a prompt that simply has no
// handling log and has no way to tell that one existed. 「（空）」 (empty) is a fact it can
// work from.
func writePart(b *strings.Builder, label, body string) {
	if strings.TrimSpace(body) == "" {
		fmt.Fprintf(b, "\n%s（空）\n", label)
		return
	}
	fmt.Fprintf(b, "\n%s\n%s\n", label, strings.TrimSpace(body))
}
