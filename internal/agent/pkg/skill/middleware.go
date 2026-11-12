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

package skill

import (
	"context"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/adk"
	adkskill "github.com/cloudwego/eino/adk/middlewares/skill"

	"github.com/onexstack/onexai/internal/agent/model"
)

// skillSystemPrompt is the instruction that tells the model how to use the
// skill tool. It is rendered per agent set because the project root differs
// between them, and a model told to read a relative path would either fail or
// — worse — read a file belonging to another agent set.
//
// The anti-loop section is not decoration: asking for the same skill again
// after it has already been loaded is the one failure mode this prompt
// reliably prevents, and it costs a full extra tool round each time.
const skillSystemPrompt = `
# Skill 系统 (Skill System)

**如何使用 Skill（技能）（渐进式展示）：**

Skill 遵循**渐进式展示**模式 - 你可以在上方看到 Skill 的名称和描述，但只在需要时才阅读完整说明。请严格遵循以下防循环工作流：

1. **识别适用场景**：检查用户的任务是否匹配某个 Skill 的描述。
2. **【关键】检查上下文记忆 (Check Memory)**：在准备调用工具前，**必须**回顾对话历史。如果当前会话中**已经**成功调用过 '{tool_name}' 工具并获取了该 Skill，**绝对禁止**再次调用该工具获取相同内容。
3. **按需加载 (仅限首次)**：仅在当前会话中**首次**处理该领域任务时，使用 '{tool_name}' 工具读取完整说明。
4. **立即执行 (Transition to Action)**：一旦 Skill 的完整说明被加载到上下文中，必须立即停止使用 '{tool_name}' 工具，直接根据已加载的指令和最佳实践开始处理用户的任务。
5. **访问支持文件**：Skill 可能包含辅助脚本、配置或参考文档 - 使用绝对路径访问。

## Skill 调用硬性限制 (Anti-Loop Restrictions)
- **严禁死循环调用**：绝对不允许在同一个会话中，使用完全相同的参数多次调用 '{tool_name}' 工具。
- **强制推进**：只要 '{tool_name}' 工具成功返回了内容，你的下一步动作必须是具体的分析、计划或执行其他实质性工具，禁止再次请求 Skill 帮助。

**何时使用 Skill：**
- 用户请求匹配某个 Skill 的领域
- 并且，你当前上下文中**尚未**拥有该 Skill 的详细说明
- 某个 Skill 为复杂任务提供了经过验证的模式

## 重要提示

在使用文件系统工具（如 ls, read_file, glob, grep 等）时，你必须使用绝对路径。

项目的根目录是：{project_root}

- 当用户要求列出"当前目录"下的文件时，请使用路径：{project_root}
- 当用户要求读取基于相对路径的文件时，请在其前方拼接 {project_root} 以将其转换为绝对路径
- 示例：如果用户指令为"读取 main.go"，你应该调用 read_file 工具，并将 file_path 参数设置为："{project_root}/main.go"

调用文件系统工具时，请始终使用绝对路径。

记住：Skill 让你更加强大和稳定。切勿陷入重复阅读 Skill 的死循环！
`

// Middleware builds the skill middleware for one agent set, implementing
// agentbuilder.SkillMiddleware.
//
// It returns nil — not an error — when no record produced a usable backend:
// an agent set that declares no skills (or only unusable ones) simply has no
// skill tool, which is a normal agent rather than a broken one.
func (m *Manager) Middleware(
	ctx context.Context, agentKey string, skills []*model.SkillM,
) (adk.ChatModelAgentMiddleware, error) {
	backends := make([]adkskill.Backend, 0, len(skills))
	for _, s := range skills {
		if s == nil {
			continue
		}
		if b := m.GetOrCreateBackend(ctx, agentKey, *s); b != nil {
			backends = append(backends, b)
		}
	}
	if len(backends) == 0 {
		return nil, nil
	}

	var backend adkskill.Backend
	if len(backends) == 1 {
		backend = backends[0]
	} else {
		backend = NewCombinedBackend(backends...)
	}

	projectRoot := IsolatedSkillsDir(m.skillsDir, agentKey)
	middleware, err := adkskill.NewMiddleware(ctx, &adkskill.Config{
		Backend:            backend,
		UseChinese:         true,
		CustomSystemPrompt: m.systemPromptRenderer(projectRoot),
	})
	if err != nil {
		return nil, fmt.Errorf("skill: build middleware for agent set %q: %w", agentKey, err)
	}
	return middleware, nil
}

// systemPromptRenderer returns the prompt builder bound to one agent set's
// project root. The skill prefix, when configured, replaces the built-in
// template entirely rather than being prepended: an operator who supplies one
// is stating the whole contract, and silently keeping the default alongside it
// would produce two sets of overlapping instructions.
func (m *Manager) systemPromptRenderer(projectRoot string) func(context.Context, string) string {
	template := skillSystemPrompt
	if m.skillPrefix != "" {
		template = m.skillPrefix
	}
	return func(_ context.Context, toolName string) string {
		return renderSkillPrompt(template, toolName, projectRoot)
	}
}

// renderSkillPrompt substitutes the placeholders the skill template uses.
// It is a plain replacement rather than a template engine because the prompt is
// operator-supplied text: a stray brace in it must not become a rendering
// error that silently drops the whole instruction.
func renderSkillPrompt(template, toolName, projectRoot string) string {
	replacer := strings.NewReplacer(
		"{tool_name}", toolName,
		"{project_root}", projectRoot,
	)
	return replacer.Replace(template)
}
