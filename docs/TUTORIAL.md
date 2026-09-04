# Soulacy Getting-Started Tutorial

Welcome to Soulacy! This tutorial breaks down the absolute basics of building with agents, explains key terms with clear analogies, and guides you through step-by-step instructions and practical examples.

---

## 1. Back to Basics: The Building Blocks

Before looking at configurations or code, let's define the three core concepts in the AI agent space and see how they work together.

```
       ┌──────────────────────────────────────────────────────────┐
       │                       THE AGENT                          │
       │  (The Brain: system instructions + LLM reasoning loop)   │
       └────────────────────────────┬─────────────────────────────┘
                                    │
                  ┌─────────────────┴─────────────────┐
                  ▼                                   ▼
        ┌───────────────────┐               ┌───────────────────┐
        │     THE TOOLS     │               │    THE SKILLS     │
        │ (Physical Actions │               │ (Playbooks & SOPs │
        │  & Capabilities)  │               │   to study from)  │
        └───────────────────┘               └───────────────────┘
```

### What is an Agent?
* **In Simple Terms**: An **Agent** is an independent digital worker. It has a specific job description, a thinking process, and a set of instructions.
* **Under the Hood**: In Soulacy, an agent is represented by a single declarative file called `SOUL.yaml`. This file configures:
  - **Who it is**: The `system_prompt` defines its role, behavior, rules, and persona.
  - **What it thinks with**: The `llm:` block configures the language model (e.g., Llama 3, Claude, GPT-4) that powers its reasoning.
  - **What channels it listens on**: The `channels:` block binds it to platforms (HTTP, Slack, Telegram).

### What is a Tool?
* **In Simple Terms**: A **Tool** is a concrete capability that allows an agent to interact with the external world.
* **Why it's needed**: Large Language Models (LLMs) are great at text reasoning, but they are isolated. By default, they cannot surf the live web, read files from a hard drive, execute a database query, or run a python script. Tools are the bridges.
* **How it works**: During its conversation loop, if the agent decides it needs to perform an action, it pauses its reply and says: *"I need to run the tool `web_search` with the query 'today's stock prices'"*. Soulacy runs the tool, gathers the results, and feeds the text back to the agent so it can continue writing its answer.

### What is a Skill?
* **In Simple Terms**: A **Skill** is a step-by-step instruction manual or playbook that teaches an agent *how* to perform a complex, specialized workflow.
* **The Difference between a Tool and a Skill**:
  - A **Tool** is a raw capability (e.g., *"Here is a wrench to turn a bolt"* or *"Here is a function to read a file"*).
  - A **Skill** is an expert playbook (e.g., *"Here is a 10-step guide on how to audit code for security flaws"*).
* **How Soulacy handles skills**: Instead of cramming thousands of lines of guidelines into an agent's main prompt—which is expensive and makes the model lose focus—Soulacy stores skills in modular markdown files (`SKILL.md`). The agent only sees a cheap "index" of skill names. When a task requires specialized expertise, the agent invokes the `read_skill` tool to dynamically load the full playbook into memory only when needed (progressive disclosure).

---

### How They All Come Together: The Analogy

Think of building an agent application like setting up a **Baking Kitchen**:

| Component | Analogy | Description |
| :--- | :--- | :--- |
| **The Agent** | **The Baker** | The person with the brain, coordination, and general instructions to *"bake a dessert."* |
| **The Tools** | **The Kitchen Utensils** | The physical capabilities needed to execute actions (e.g., the oven, the electric mixer, the measuring cups). |
| **The Skills** | **The Recipe Book** | The detailed, step-by-step guides for specialized outcomes (e.g., a recipe for *Chocolate Soufflé*). The baker doesn't memorize the whole recipe book; they open it to the right page only when they start baking. |

**The Workflow**:
1. You text the **Baker (Agent)**: *"Please bake a chocolate soufflé."*
2. The Baker looks at its list of recipes and sees it has a **Soufflé Recipe (Skill)**.
3. The Baker uses its hands to open and **read the recipe (dynamic skill tool)**.
4. The recipe instructs the Baker to whisk egg whites. The Baker turns on the **electric mixer (Tool)** to get it done.
5. The Baker synthesizes the finished dessert and presents it to you (Final Output).

---

## 2. Key Technical Concepts in Soulacy

### The Agentic Loop
When you send a message, the Engine runs this loop:
`LLM Analysis ➔ Tool/Skill Invocations (in parallel) ➔ LLM Analysis ➔ Final Reply`

### Multi-Agent Peer Delegation
Agents can also act as **tools for other agents**.
* If a `writer` agent needs deep-dive research, it doesn't do it itself. It invokes a tool named `agent__researcher`.
* The writer acts as a **manager** delegating a ticket to a **specialist**. Soulacy pauses the writer, executes the researcher agent, and hands the final essay back to the writer as a tool output.

---

## 3. Step-by-Step How-To Guides

### How to Install and Run Soulacy
Soulacy compiles into a single binary that bundles the HTTP server, the runtime engine, and an embedded Svelte dashboard.

1. **Build the binary**:
   ```bash
   make all
   ```
2. **Run the gateway**:
   ```bash
   ./build-and-restart.command
   ```
3. **Verify running status**:
   Open [http://127.0.0.1:18789](http://127.0.0.1:18789) to view the live dashboard.

---

### How to Create Your First Agent
Let's build a simple translation agent called `translator` that speaks English and Spanish.

1. Create a new directory inside your configured `agent_dirs` path:
   ```bash
   mkdir -p examples/agents/translator
   ```
2. Save the following content to `examples/agents/translator/SOUL.yaml`:
   ```yaml
   id: translator
   name: Translator
   description: Translates input text into Spanish or English.
   version: 1.0.0
   trigger: channel
   channels:
     - http
   system_prompt: |
     You are a professional, bilingual translator.
     Detect the language of the user's message.
     - If it is in English, translate it to natural, fluent Spanish.
     - If it is in Spanish, translate it to natural, fluent English.
     Do not provide any conversational filler or meta-commentary—reply ONLY with the translation.
   llm:
     provider: ollama
     model: llama3:latest
     temperature: 0.1
   enabled: true
   ```
3. **Test the agent**:
   Open [http://127.0.0.1:18789](http://127.0.0.1:18789), navigate to the **Agents** tab, choose the `translator` agent, and type: *"Hello, how are you doing today?"*

---

### How to Add a Custom Python Tool
Let's build a tool that calculates Fibonacci numbers and hook it to an agent.

1. **Create the Python script**:
   Save this to `tools/fibonacci.py` (ensure `python3` is available on the system PATH):
   ```python
   import sys
   import json

   def calculate(n):
       if n < 0:
           return "Invalid input"
       if n == 0:
           return 0
       elif n == 1:
           return 1
       a, b = 0, 1
       for _ in range(2, n + 1):
           a, b = b, a + b
       return b

   if __name__ == "__main__":
       # Soulacy passes arguments via stdin JSON
       try:
           args = json.load(sys.stdin)
           n = int(args.get("n", 0))
           result = calculate(n)
           print(json.dumps({"result": result}))
       except Exception as e:
           print(json.dumps({"error": str(e)}), file=sys.stderr)
           sys.exit(1)
   ```
2. **Bind the tool to an agent**:
   Add the tool schema declaration to your agent's `SOUL.yaml`:
   ```yaml
   id: math-wizard
   name: Math Wizard
   trigger: channel
   channels:
     - http
   system_prompt: |
     You are an assistant skilled in mathematics.
     Use your tools to solve user queries.
   llm:
     provider: ollama
     model: llama3:latest
   tools:
     - name: get_fibonacci
       description: Calculates the Nth Fibonacci number.
       python_file: ./tools/fibonacci.py
       parameters:
         type: object
         properties:
           n:
             type: integer
             description: The position in the sequence (0-indexed).
         required: [n]
   enabled: true
   ```
   *Note: Soulacy's file watcher will automatically pick up changes to `.py` scripts and update the tool-catalog on the fly.*

---

### How to Connect an MCP Server
You can inject third-party tool sets (like filesystem access or GitHub integrations) using Model Context Protocol (MCP) servers.

1. Open `config.yaml` in the root of your project.
2. Under `mcp.servers`, add your server. Here is an example of adding local filesystem access:
   ```yaml
   mcp:
     servers:
       filesystem:
         transport: stdio
         command: npx
         args:
           - "-y"
           - "@modelcontextprotocol/server-filesystem"
           - "/Users/clawagent/Documents"
   ```
3. Restart the gateway.
4. The filesystem tools will automatically map to **every** agent in the workspace, exposed as `mcp__filesystem__read_file`, `mcp__filesystem__write_file`, and more.

---

### How to Configure Channel Integrations (Slack Socket Mode)
To host your agents on channels like Slack without needing a public HTTPS webhook URL, you can configure **Slack Socket Mode**:

1. Register an application in the [Slack Developer Console](https://api.slack.com/apps).
2. Enable **Socket Mode** and **Event Subscriptions** (subscribe to `message.im` and `message.channels`).
3. Generate an **App-Level Token** (starts with `xapp-`) and a **Bot User OAuth Token** (starts with `xoxb-`).
4. Update `config.yaml`:
   ```yaml
   channels:
     slack:
       enabled: true
       app_token: "xapp-1-..."
       bot_token: "xoxb-..."
       agent_id: "translator" # Default agent that replies to Slack events
   ```
5. Restart the gateway. The agent will connect over WebSockets and listen for messages directly.

---

## 4. Practical Blueprints

Below are complete agent combinations illustrating simple configurations to advanced orchestrators.

### Blueprint A: The Structured RAG Document Reviewer
This blueprint searches uploaded corporate PDFs to perform structured information extraction.

`examples/agents/doc-reviewer/SOUL.yaml`:
```yaml
id: doc-reviewer
name: Doc Reviewer
description: Reviews local files and returns highly structured facts.
trigger: channel
channels:
  - http
system_prompt: |
  You are an expert corporate auditor.
  Use the `kb_search` tool to look up details in the 'employee-handbook' knowledge base.
  Extract:
  - The name of the organization.
  - The weekly holiday allowance policy.
  
  Do not guess. Cite the source files (e.g. handbook_v2.pdf) alongside facts.
llm:
  provider: openai
  model: gpt-4o
  temperature: 0.0
knowledge:
  - employee-handbook
builtins:
  - kb_search
max_turns: 5
enabled: true
```

---

### Blueprint B: The Writer-Editor Agent Team (Multi-Agent)
This advanced team divides labor between a researcher who pulls raw facts, a composer who writes the draft, and a editor who grades and revises the draft.

#### 1. The Expert Researcher
`examples/agents/researcher/SOUL.yaml`:
```yaml
id: researcher
name: Fact Researcher
trigger: internal # Can only be invoked as a peer agent tool
system_prompt: |
  You are a deep-dive researcher.
  Search the live web to gather concrete statistics and timelines for the user's request.
  Format your reply as a structured bulleted summary.
llm:
  provider: ollama
  model: llama3:latest
builtins:
  - web_search
max_turns: 5
enabled: true
```

#### 2. The Editorial Critic
`examples/agents/editor/SOUL.yaml`:
```yaml
id: editor
name: Editorial Critic
trigger: internal
system_prompt: |
  You are an editor. Grade the text draft.
  Return structured output:
  - Status: (SHIP IT / REVISE)
  - Criticisms: List of improvements needed.
llm:
  provider: ollama
  model: llama3:latest
max_turns: 2
enabled: true
```

#### 3. The Orchestrator Writer
`examples/agents/writer/SOUL.yaml`:
```yaml
id: writer
name: Writer Orchestrator
trigger: channel
channels:
  - http
system_prompt: |
  You are an orchestrating author. Follow this exact process:
  1. Call `agent__researcher` to pull facts on the topic.
  2. Write a professional draft using those facts.
  3. Call `agent__editor` to review the draft.
  4. Revise the draft to address the editor's criticisms, then print the final version.
llm:
  provider: ollama
  model: llama3:latest
agents:
  - researcher
  - editor
builtins: [] # Isolates the orchestrator to its peer tools only
max_turns: 10
enabled: true
```

With this team deployed, when you text the `writer` agent, Soulacy coordinates three recursive sub-runs behind a single seamless conversation loop. Enjoy building with Soulacy!
