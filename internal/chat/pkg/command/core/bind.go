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

package core

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/feizaonet/cobrax"
	"github.com/onexstack/onexstack/pkg/cli/templates"
	"github.com/spf13/cobra"

	"github.com/onexstack/onexai/internal/chat/pkg/runtime"
	chat "github.com/onexstack/onexai/pkg/known/chat"
)

var (
	bindLong = templates.LongDesc(`
        绑定飞书账号：把控制台「账号安全」页给出的绑定码发到这里，再把这里回给你的确认码填回控制台。

        【怎么用】：
        - 控制台 → 账号 → 安全设置 → 飞书 → 绑定，页面会给出这条 /bind-onex 命令；
        - 整条复制，在飞书里私聊发给 OneX 机器人。这条命令只在私聊里执行：群里所有人都能看到这串码，而绑定码是先说给机器人的那位算数，等于把自己的账号让给先动手的人；
        - 机器人会在这里回给你一串确认码。回到控制台那个弹窗把它填进去，绑定才算完成：两端各持一半证据，缺了确认码谁都绑不上；
        - 确认码只在这次私聊里给出，不要转发给任何人。

        【有效期】：绑定码十分钟内有效，过期请在控制台重新获取。
        【发错地方了？】：只要这串码被发进了群，就当作已经泄露，回控制台重新获取一个新的再私聊发送，别再发原来那串。`)

	bindExample = templates.Examples(`
        # 控制台给出绑定码后，整条复制发送
        /bind-onex <绑定码>`)
)

// defaultClaimTTL is the fallback used when the deployment configured no
// lifetime. It exists so an unconfigured server still completes bindings rather
// than writing claims that expire the instant they are written; the configured
// value is the one that counts, and Validate refuses a non-positive one.
const defaultClaimTTL = 15 * time.Minute

// BindInfoOptions defines configuration for the "bind-onex" command.
type BindInfoOptions struct {
	out runtime.Responder
	// code is the one-time code the caller carried out of the console.
	code string
}

// NewBindCmd creates the "bind-onex" command.
//
// # Why this is a slash command and not a bare message
//
// The bot's inbound ladder sends anything starting with "/" to this command
// tree and everything else to the model. A binding code sent bare is therefore
// a prompt — which is what it used to be, and the model answered it with "this
// looks like an API key". The "/" is what makes the intent explicit to the bot
// instead of a guess about a random-looking string.
func NewBindCmd(sr *runtime.StaticRuntime, out runtime.Responder) *cobra.Command {
	o := &BindInfoOptions{out: out}

	cmd := &cobra.Command{
		Use:     "bind-onex <code>",
		Short:   "绑定飞书账号：把控制台给出的绑定码发到这里",
		Long:    bindLong,
		Example: bindExample,

		SilenceUsage:          true,
		SilenceErrors:         true,
		DisableFlagsInUseLine: true,

		// DisableFlagParsing is load-bearing, not tidiness. A binding code is
		// 32 random bytes in base64url, whose alphabet includes "-": roughly one
		// code in 64 starts with one, and pflag would read `-AbC...` as a
		// cluster of shorthand flags and fail with "unknown shorthand flag".
		// That failure looks exactly like the bug this command exists to fix —
		// a bot that ignores you — and it would hit one user in 64. With
		// parsing off, cobra hands the raw arguments to RunE (see its execute:
		// `if c.DisableFlagParsing { argWoFlags = a }`).
		//
		// The cost is that `/bind-onex --help` is treated as an argument and refused
		// rather than printed. `/help` still lists the command, and this
		// command takes no flags for a help flag to be useful with.
		DisableFlagParsing: true,

		Annotations: map[string]string{
			cobrax.AnnotationArgPrefix + "0": "必填参数。控制台「账号安全」页给出的飞书绑定码（十分钟内有效）。",
			chat.ChatEnabled:                 chat.True,
			chat.ChatGroup:                   chat.ChatGroupCore,
			// The claim names a chat identity, which only a Lark message event
			// carries. Hiding is the only thing this annotation does (see
			// command.HideCommands) — the guard that actually refuses a caller
			// without an identity is in Run. It stays beside P2POnly rather than
			// being folded into it: this one says which kind of caller can be
			// bound, P2POnly says which kind of conversation may ask.
			chat.LarkOnly: chat.True,
			// The value is the refusal, not chat.True, because the generic
			// "only in a private chat" would leave the caller with the one
			// sentence that must not be given here: the code they just spoke in
			// a group is one every member can claim, so repeating it privately
			// is exactly what hands the binding to whoever read it first.
			chat.P2POnly: "该命令只能在OneX私聊中使用，绑定码已泄露，请回控制台「账号安全」重新获取并绑定。",
		},

		RunE: func(cmd *cobra.Command, args []string) error {
			if err := o.Complete(cmd.Context(), args); err != nil {
				return err
			}
			if err := o.Validate(cmd.Context(), args); err != nil {
				return err
			}
			return o.Run(cmd.Context(), sr, runtime.From(cmd.Context()))
		},
	}

	return cmd
}

// Complete records the code. The arity check belongs to Validate: cobra's own
// arg validators report in English and are not UserErrors, so their message
// would reach the user wrapped in a trace id.
func (o *BindInfoOptions) Complete(_ context.Context, args []string) error {
	if len(args) == 1 {
		o.code = args[0]
	}
	return nil
}

// Validate reports whether the caller sent exactly one non-empty code.
func (o *BindInfoOptions) Validate(_ context.Context, args []string) error {
	switch {
	case len(args) == 0:
		return runtime.NewUserError("缺少绑定码。请把控制台「账号安全」页给出的整条 /bind-onex 命令复制过来。")
	case len(args) > 1:
		return runtime.NewUserError("参数过多。绑定码里不含空格，请整条复制控制台给出的 /bind-onex 命令。")
	case strings.TrimSpace(o.code) == "":
		return runtime.NewUserError("绑定码不能为空。")
	}
	return nil
}

// Run parks the claim, hands back the confirmation code, and tells the caller
// what that did and did not prove.
//
// # What this can and cannot say
//
// The chat side never learns which business account a code belongs to — that is
// the fact it is waiting to be told (see model.PeerBindClaimM). It cannot
// validate the code either: a fabricated string wins the insert race exactly as
// a real one does. So the one thing the reply must never claim is that the
// binding succeeded; the console, which holds the account's token and polls the
// business side, is the only party that can say that.
//
// # What the confirmation code is for
//
// It is the half of a binding this side is the only one able to supply: proof
// that whoever is finishing the binding was also the one who spoke. The business
// side knows which of its accounts is asking and has no way to know who spoke;
// this side knows who spoke and no way to know the account. Handing the speaker
// a string and requiring the account holder to type it is what joins the two.
//
// So it goes to the winner of the insert and to nobody else, and it goes into
// the conversation the code was spoken in — which, because of x-p2p-only, is a
// conversation with one reader.
func (o *BindInfoOptions) Run(ctx context.Context, sr *runtime.StaticRuntime, dr *runtime.DynamicRuntime) error {
	if sr == nil || sr.Store == nil {
		return runtime.NewUserError("绑定功能暂不可用：本服务未配置数据库，请联系管理员。")
	}
	// The identity is the whole point of the command: a claim records who spoke
	// the code, and there is nobody to record without an open_id. A card action
	// and a REST call both arrive without one.
	if dr == nil || dr.Handle() == "" {
		return runtime.NewUserError("无法识别你的飞书身份。请在飞书中私聊机器人后重试。")
	}

	claims := sr.Store.PeerBindClaims()
	if claims == nil {
		return runtime.NewUserError("绑定功能暂不可用：本服务未配置数据库，请联系管理员。")
	}

	ttl := sr.PeerBindClaimTTL
	if ttl <= 0 {
		ttl = defaultClaimTTL
	}

	// The triple is not a choice: it is the key the message path reads back
	// when it decides which agents a speaker may use (see
	// biz/v1/bot/schedule.go). A claim written under any other key would bind
	// nothing.
	won, confirm, err := claims.Claim(ctx, o.code, chat.BackendLark, sr.AppID, dr.Handle(), ttl)
	if err != nil {
		// Not a UserError: this is our failure, and it should carry a trace id.
		return err
	}
	if won {
		return o.reply(ctx, fmt.Sprintf(bindSpoken, confirm), true)
	}

	// The insert lost. Three different situations answer `false`, and only one
	// of them is about the code itself.
	row, err := claims.GetLive(ctx, o.code)
	if err != nil {
		return err
	}
	switch {
	case row == nil:
		// Nothing live under this code. Could be a code that expired, or one
		// somebody else claimed and that has since lapsed; the chat side cannot
		// tell those apart and does not pretend to.
		return o.reply(ctx, bindStale, false)
	case row.PeerID == dr.Handle():
		// This same person said it before and the console has not committed
		// yet. Repeating must not read as a refusal — but the code they were
		// sent cannot be said again either, because only its digest was kept.
		// So rotate: a new code, and the old one stops working. The reply has to
		// say so, or a user holding two codes and filling in the first would read
		// the mismatch as a broken feature.
		reissued, ok, err := claims.Reissue(ctx, o.code, dr.Handle())
		if err != nil {
			return err
		}
		if !ok {
			// The row lapsed between the read and the update, or was swept.
			// Either way there is nothing left to confirm against, which is the
			// same answer the branch above gives.
			return o.reply(ctx, bindStale, false)
		}
		return o.reply(ctx, fmt.Sprintf(bindRepeated, reissued), false)
	default:
		// First writer wins, and the first writer was somebody else. The code
		// itself may be perfectly valid — that is exactly why this case is
		// worded as "already used" rather than "invalid".
		//
		// This branch must never carry a confirmation code. The claim it read
		// back names somebody else, and a code handed out here would be one for
		// their attempt, spoken into the wrong person's conversation.
		return o.reply(ctx, bindTaken, false)
	}
}

// The four answers, as consts because two of them are reachable from more than
// one branch and the user must read the same words whichever branch it was.
//
// Two of them carry a %s for the confirmation code. The other two — bindTaken
// and bindStale — carry no code and must never be given one: they are the
// branches where this caller did not make the claim that is on the row, and a
// code spoken here would be one for somebody else's attempt, handed to the
// wrong person, in the wrong conversation. That invariant is what the package's
// tests assert directly.
const (
	bindSpoken = "✓ 已收到绑定码。\n\n" +
		"你的确认码是：%s\n\n" +
		"请回到控制台的「账号安全」页，把这串确认码填进飞书绑定的弹窗 —— 绑定是否成功以那里为准，这里能证明的只是这串码已被记录。" +
		"确认码只在这次私聊里给出，不要转发给任何人。\n\n" +
		"（有效期十分钟。若超时仍未完成，请回控制台重新获取。）"

	bindRepeated = "这串绑定码你之前已经发送过了，正在等待控制台确认。\n\n" +
		"确认码已更新为：%s\n\n" +
		"请回控制台填入这串新的 —— 之前那串已作废。确认码只在这次私聊里给出，不要转发给任何人。" +
		"若长时间未完成，可重新获取一个新的绑定码。"

	bindTaken = "这串绑定码已经被另一个飞书账号使用了，无法再绑定到你的账号。\n\n" +
		"请回到控制台的「账号安全」页重新获取一个新的绑定码。"

	bindStale = "这串绑定码已失效或已过期（有效期十分钟）。\n\n" +
		"请回到控制台的「账号安全」页重新获取一个新的绑定码。"
)

// reply sends one of the four answers. The confirmation code is the only thing
// any of them adds that the caller did not send: the binding code itself is
// never echoed, because the message it came from is already the one carrying
// it, and repeating it would only put it in front of readers twice.
//
// There used to be a fifth answer — the same words with a warning appended when
// the code had been spoken where others could read it. That branch is gone
// because the case is: x-p2p-only refuses a group caller in the root's
// PersistentPreRunE, before Run is reached, so every answer sent from here goes
// to the one person who will read it.
//
// spoken says whether *this* call is the one that recorded the code, which is
// distinct from whether the words are the success words — a repeat reads as
// success to the user and is not a new claim.
func (o *BindInfoOptions) reply(ctx context.Context, text string, spoken bool) error {
	return o.out.SendResult(ctx, &runtime.CommandResult{
		Raw:       map[string]any{"claimed": spoken},
		PlainText: text,
	})
}
