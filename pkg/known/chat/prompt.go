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

package chat

// OncallPrompt is the welcome message shown by the SRE duty bot.
const OncallPrompt = "SRE 值班机器人欢迎您！需要处理问题请发送 /oncall；查看功能请发送 /help。更详细的使用方法见 [Helper 使用手册](https://larkfive.sg.larksuite.com/wiki/S44pwiQ7KiXgH4kseAmlJRWSgkh)"

// A SystemSkillPrompt const used to live here, describing a progressive Skill
// loading system driven by a '{tool_name}' tool. It was removed rather than
// wired up: nothing referenced it, and no such tool exists in this repo's agent
// agent, so adopting it would have meant instructing the model to call a tool it
// does not have — with '{project_root}' left unsubstituted as a literal in the
// prompt. What was worth keeping from it was the anti-loop rule, and that belongs
// to whichever backend actually loads skills, not to a const every chat
// turn can reach.
