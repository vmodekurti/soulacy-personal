# Soulacy Complete User & Conceptual Guide

Welcome to Soulacy! This guide is designed to help you understand the core concepts behind the framework, see how the execution flow works under the hood, and learn how to build your own agents with simple, practical steps.

---

## 1. Core Concepts (Explained Simply)

To understand Soulacy, think of it not as abstract lines of code, but as a **highly organized digital services agency**. Here is how the key components fit into this virtual office building.

```
                      ┌────────────────────────────────────────┐
                      │             THE GATEWAY                │
                      │   (The Office Building & Channels)     │
                      └───────────────────┬────────────────────┘
                                          │
                        ┌─────────────────┴─────────────────┐
                        ▼                                   ▼
              ┌───────────────────┐               ┌───────────────────┐
              │    THE AGENT      │               │   THE CHANNELS    │
              │  (The Employee &  │               │  (The Phone Lines/│
              │   their Desk/YAML)│               │   Telegram/Slack) │
              └─────────┬─────────┘               └───────────────────┘
                        │
         ┌──────────────┴──────────────┐
         ▼                             ▼
  ┌─────────────┐               ┌─────────────┐
  │  THE TOOLS  │               │ THE SKILLS  │
  │ (The Desk   │               │ (The Recipe │
  │  Utensils)  │               │   Manual)   │
  └─────────────┘               └─────────────┘
```

### What is a Channel? (The Phone Lines)
* **The Analogy**: Think of Channels as the **office phone lines**.
* **Conceptually**: A Channel is the communication medium where a human user interacts with an agent (e.g., Telegram, Slack, Discord, WhatsApp, or an HTTP web interface).
* **Under the Hood**: Each channel has a dedicated "receptionist" adapter. For example, the Telegram adapter is a background loop listening to Telegram. The moment you text your bot, the Telegram receptionist packages your text into a standard "Work Ticket" (`message.Message` containing your text, your username, and your chat ID) and drops it in the central office in-box.

### What is an Agent? (The Employee & their Folder)
* **The Analogy**: Think of the Agent as **an employee at a desk**. 
* **Conceptually**: An agent is a digital persona with a specific job description, rules of behavior, and authorized tools.
* **Under the Hood**: On disk, an agent is represented by a single directory containing a `SOUL.yaml` file (e.g. `examples/agents/writer/SOUL.yaml`). This file is the **employee folder** that tells Soulacy who the employee is (`system_prompt`), what LLM model they think with (`llm.model`), and what tools they are allowed to use.

### What is the Role of the LLM? (The Brain)
* **The Analogy**: Think of the LLM as the **employee's intelligence and ability to reason**.
* **Conceptually**: The LLM is not just a text generator. In Soulacy, the LLM is the **decision-maker** that coordinates the logical flow of the task.
* **Under the Hood**: The LLM reads the instructions, history, and available tools, and decides what to do next. It *cannot* search the web or write local files directly. Instead, when it needs external facts, it pauses and formats a JSON command telling the Soulacy framework: *"I need to run the `web_search` tool with the query 'tomorrow's weather'."*

### What is a Tool? (The Desk Utensils)
* **The Analogy**: Think of Tools as the **utensils or physical capabilities** at the employee's desk (e.g., a calculator, a web browser, a file reader).
* **Conceptually**: A Tool is a concrete action that connects the LLM's reasoning to the outside world.
* **Under the Hood**: When the LLM decides to invoke a tool, Soulacy pauses the LLM session, executes the action (either a fast in-process Go function, a sandboxed Python script, or an external MCP server call), gets the resulting text output, and feeds it back to the LLM.

### What is a Skill? (The Recipe Book)
* **The Analogy**: Think of a Skill as a **recipe book** sitting on the employee's desk.
* **Conceptually**: A Skill is a step-by-step instruction playbook (`SKILL.md` written under the `agentskills.io` standard) that teaches the agent how to do a specialized, complex workflow.
* **Under the Hood**: Instead of stuffing thousands of instructions into the agent's main prompt—which is expensive and makes the model lose focus—the agent gets a list of *available recipes*. If the task matches a recipe, the agent uses the `read_skill` tool to dynamically load that playbook into its context on-demand (progressive disclosure).

---

## 2. Step-by-Step Execution Flow

When you send a message, Soulacy processes it through the following highly optimized lifecycle:

```
[ Telegram Message In ]
       │ (Telegram Adapter converts text into a standard message ticket)
       ▼
[ Shared Inbox Queue ]
       │ (Drained by an idle thread in the Worker Pool)
       ▼
[ Engine.Handle() ]
       │ 1. Loads Agent's SOUL.yaml prompt & model configs
       │ 2. Pulls Session History from SQLite (Memory Archive)
       │ 3. Primes the Context Prefix (Prompt + Skill/KB/Agent catalogs)
       ▼
[ LLM Turn 1 ] ──► LLM reads context and original user question:
                   reasons: "I don't have this data. I must call a tool."
                   Outputs: ToolCall{ Name: "web_search", Args: {"query": "Apple price"} }
       ▼
[ Tool Execution ] ──► Soulacy pauses LLM, runs the tool, intercepts results,
                       and appends results to the session history.
       ▼
[ LLM Turn 2 ] ──► LLM reads original question + search results.
                   reasons: "I now have the facts to write the final reply."
                   Outputs: Text response: "Apple is trading at $180 today."
       ▼
[ Persistent File ] ──► Persists chat turns in SQLite memory databases.
       ▼
[ Outbound Reply ] ──► Telegram Adapter picks up reply and sends it to the user's phone.
```

---

## 3. How Multi-Agent Orchestration Works

In Soulacy, you can assemble a team of agents that talk to each other. **To a parent agent, a peer agent is just another tool.**

### The Analogy: The Manager and the Specialists
Think of a `writer` agent as a **Manager**, and `researcher` and `critic` agents as **Specialists**:

```
[ User Request ] ➔ (writer) Manager reads prompt.
                      │
                      ▼ Decides: "I need raw facts."
                [ Calls tool: agent__researcher ]
                      │
                      ├─► Soulacy pauses the (writer) session.
                      │
                      ▼ Recurses (Re-entrant call)
                [ Launches brand new execution for (researcher) Specialist ]
                      │
                      ├─► (researcher) runs its own search tools and LLM loops.
                      │   Outputs a completed text summary brief.
                      │
                      ▼ Returns brief to parent
                [ Result returned as tool output string: "Facts gathered..." ]
                      │
                      ▼ (writer) resumes session.
                    (writer) Manager writes draft, then delegates to (critic) for grading.
```

* **No Bloated Orchestration**: You configure the peer relationships under `agents:` in `SOUL.yaml`.
* **Standard Schemas**: Soulacy reads the peer definitions on disk and automatically advertises them to the parent LLM as standard tools named `agent__<id>`.
* **Deep Safeguards**: To prevent infinite cycles (Agent A calling B, who calls A), Soulacy tracks execution depth using a context counter (`agentCallDepth`). If a run goes **beyond 5 levels deep**, it halts the loop to protect your API budget.

---

## 4. How to Use Soulacy: Step-by-Step

### Step 1: Boot Soulacy & Access the Dashboard
1. Compile the framework in the root directory:
   ```bash
   make all
   ```
2. Start the gateway server:
   ```bash
   ./build-and-restart.command
   ```
3. Open [http://127.0.0.1:18789](http://127.0.0.1:18789) to view the live dashboard.

---

### Step 2: Configure Your API Keys
1. Open the file `config.yaml` in the root workspace.
2. In the `llm.providers` section, add your API keys. E.g., for OpenAI or Anthropic:
   ```yaml
   llm:
     default_provider: openai
     providers:
       openai:
         api_key: "sk-proj-YOUR-API-KEY"
         model: "gpt-4o"
       anthropic:
         api_key: "sk-ant-YOUR-API-KEY"
         model: "claude-3-5-sonnet"
   ```
3. Save the file.

---

### Step 3: Define Your Agent (`SOUL.yaml`)
Let's build a simple agent named `translator` that translates text.

1. Create a folder in your configured agent directory:
   ```bash
   mkdir -p examples/agents/translator
   ```
2. Create a file named `SOUL.yaml` inside that folder:
   ```yaml
   id: translator
   name: Translator Assistant
   description: Translates input text into Spanish.
   version: 1.0.0
   trigger: channel
   channels:
     - http
   system_prompt: |
     You are a helpful translation assistant.
     Translate the user's message into natural, fluent Spanish.
     Return ONLY the translation without any conversation.
   llm:
     provider: openai
     model: gpt-4o
     temperature: 0.1
   enabled: true
   ```

---

### Step 4: Test the Agent
1. Open the dashboard at [http://127.0.0.1:18789](http://127.0.0.1:18789).
2. Go to the **Agents** tab. You will see `Translator Assistant` loaded automatically (the file watcher picked it up instantly without a restart!).
3. Select the agent, open the Chat Tester, and type: *"Hello, what a beautiful day!"*
4. The agent will reply: *"¡Hola, qué hermoso día!"*

Enjoy building declarative, high-performance agent teams with Soulacy!
