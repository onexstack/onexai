package biz

import (
	"github.com/google/wire"

	"github.com/onexstack/onexai/internal/chat/biz/v1/agentcommand"
	"github.com/onexstack/onexai/internal/chat/biz/v1/auth"
	"github.com/onexstack/onexai/internal/chat/biz/v1/bot"
	"github.com/onexstack/onexai/internal/chat/biz/v1/chat"
	"github.com/onexstack/onexai/internal/chat/biz/v1/command"
	"github.com/onexstack/onexai/internal/chat/biz/v1/dutyschedule"
	"github.com/onexstack/onexai/internal/chat/biz/v1/oncall"
	"github.com/onexstack/onexai/internal/chat/biz/v1/oncallgroup"
	"github.com/onexstack/onexai/internal/chat/biz/v1/ticket"
	"github.com/onexstack/onexai/internal/chat/biz/v1/user"
	"github.com/onexstack/onexai/internal/chat/store"
)

// ProviderSet defines dependency injection rules for the business logic layer.
var ProviderSet = wire.NewSet(NewBiz, wire.Bind(new(IBiz), new(*biz)))

// IBiz is the top-level business logic interface.
type IBiz interface {
	BotV1() bot.BotBiz
	ChatV1() chat.ChatBiz
	AuthV1() auth.AuthBiz
	UserV1() user.UserBiz
	OncallGroupV1() oncallgroup.OncallGroupBiz
	DutyScheduleV1() dutyschedule.DutyScheduleBiz
	TicketV1() ticket.TicketBiz
	OncallV1() oncall.OncallBiz
	AgentCommandV1() agentcommand.AgentCommandBiz
	// CommandV1 returns the read-only platform command catalogue.
	CommandV1() command.CommandBiz
	// Store returns the persistence layer (sessions + tickets).
	Store() store.IStore
}

// biz is the concrete implementation of the business logic IBiz.
type biz struct {
	botBiz         bot.BotBiz
	chatBiz        chat.ChatBiz
	authBiz        auth.AuthBiz
	userBiz        user.UserBiz
	oncallGroupBiz oncallgroup.OncallGroupBiz
	dutySchedBiz   dutyschedule.DutyScheduleBiz
	ticketBiz      ticket.TicketBiz
	oncallBiz      oncall.OncallBiz
	agentCmdBiz    agentcommand.AgentCommandBiz
	commandBiz     command.CommandBiz
	st             store.IStore
}

// Ensure biz implements IBiz at compile time.
var _ IBiz = (*biz)(nil)

// NewBiz constructs a biz instance from its sub-biz dependencies.
func NewBiz(botBiz bot.BotBiz, chatBiz chat.ChatBiz, authBiz auth.AuthBiz, userBiz user.UserBiz, oncallGroupBiz oncallgroup.OncallGroupBiz, dutySchedBiz dutyschedule.DutyScheduleBiz, ticketBiz ticket.TicketBiz, oncallBiz oncall.OncallBiz, agentCmdBiz agentcommand.AgentCommandBiz, commandBiz command.CommandBiz, st store.IStore) *biz {
	return &biz{
		botBiz:         botBiz,
		chatBiz:        chatBiz,
		authBiz:        authBiz,
		userBiz:        userBiz,
		oncallGroupBiz: oncallGroupBiz,
		dutySchedBiz:   dutySchedBiz,
		ticketBiz:      ticketBiz,
		oncallBiz:      oncallBiz,
		agentCmdBiz:    agentCmdBiz,
		commandBiz:     commandBiz,
		st:             st,
	}
}

// BotV1 returns an instance that implements the BotBiz interface.
func (b *biz) BotV1() bot.BotBiz { return b.botBiz }

// ChatV1 returns an instance that implements the ChatBiz interface.
func (b *biz) ChatV1() chat.ChatBiz { return b.chatBiz }

// AuthV1 returns an instance that implements the AuthBiz interface.
func (b *biz) AuthV1() auth.AuthBiz { return b.authBiz }

// UserV1 returns an instance that implements the UserBiz interface.
func (b *biz) UserV1() user.UserBiz { return b.userBiz }

// OncallGroupV1 returns an instance that implements the OncallGroupBiz interface.
func (b *biz) OncallGroupV1() oncallgroup.OncallGroupBiz { return b.oncallGroupBiz }

// DutyScheduleV1 returns an instance that implements the DutyScheduleBiz interface.
func (b *biz) DutyScheduleV1() dutyschedule.DutyScheduleBiz { return b.dutySchedBiz }

// TicketV1 returns an instance that implements the TicketBiz interface.
func (b *biz) TicketV1() ticket.TicketBiz { return b.ticketBiz }

// OncallV1 returns an instance that implements the OncallBiz interface.
func (b *biz) OncallV1() oncall.OncallBiz { return b.oncallBiz }

// AgentCommandV1 returns an instance that implements the AgentCommandBiz interface.
func (b *biz) AgentCommandV1() agentcommand.AgentCommandBiz { return b.agentCmdBiz }

// CommandV1 returns the read-only platform command catalogue.
func (b *biz) CommandV1() command.CommandBiz { return b.commandBiz }

// Store returns the persistence layer.
func (b *biz) Store() store.IStore { return b.st }
