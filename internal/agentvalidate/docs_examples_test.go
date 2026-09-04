package agentvalidate

import "testing"

func TestCanonicalDocsExamplesValidate(t *testing.T) {
	examples := map[string]string{
		"minimal agent": `
id: assistant
name: Assistant
description: General-purpose helper
trigger: channel
channels:
  - http
llm:
  provider: openai
  model: gpt-4o-mini
  temperature: 0.2
system_prompt: |
  You are concise, helpful, and careful.
max_turns: 6
enabled: true
`,
		"first agent full example": `
id: concierge
name: Concierge
description: Full-featured concierge agent
trigger: channel
channels:
  - http
  - telegram
  - slack
llm:
  provider: openai
  model: gpt-4o
  temperature: 0.2
  max_tokens: 1024
system_prompt: |
  You are a friendly concierge at Acme Inc.
  Help users with questions, research, and task management.
  Be concise. Use bullet points for lists.
builtins:
  - web_search
memory:
  read_scopes: [agent, session]
  write_scopes: [agent]
  max_tokens: 2000
max_turns: 8
enabled: true
`,
		"scheduled output": `
id: daily-finance
name: Daily Finance
trigger: cron
schedule:
  cron: "0 8 * * *"
  output:
    channel: telegram-financial-agent
    to: "123456789"
    bot_name: "Finance Bot"
    template: "{reply}"
llm:
  provider: openai
  model: gpt-4o-mini
system_prompt: Send the daily finance brief.
enabled: true
`,
		"workflow tool steps": `
id: report-writer
name: Report Writer
description: Run a repeatable reporting pipeline
trigger: channel
channels: [http]
llm:
  provider: openai
  model: gpt-4o
system_prompt: Run the reporting workflow.
workflow:
  steps:
    - id: search
      tool: web_search
      input: '{"query":"{{.trigger}}"}'
      output: search_results
      on_error: retry
    - id: summarize
      tool: summarize_report
      input: '{"search_results":{{.search_results}}}'
      output: report
      on_error: abort
enabled: true
`,
	}

	for name, yaml := range examples {
		t.Run(name, func(t *testing.T) {
			report := Bytes([]byte(yaml), "SOUL.yaml", Options{})
			if !report.Valid {
				t.Fatalf("docs example should validate: %+v", report)
			}
		})
	}
}
