package opennotebookmcp

func toolSpecs() []map[string]any {
	stringProp := func(description string) map[string]any {
		return map[string]any{"type": "string", "description": description}
	}
	boolProp := func(description string) map[string]any {
		return map[string]any{"type": "boolean", "description": description}
	}
	stringsProp := func(description string) map[string]any {
		return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": description}
	}
	return []map[string]any{
		tool("open_notebook_health", "Check whether the local Open Notebook API is healthy.", nil, nil),
		tool("open_notebook_list_notebooks", "List Open Notebook notebooks.", nil, nil),
		tool("open_notebook_get_notebook", "Get one Open Notebook notebook by ID.", map[string]any{"notebook_id": stringProp("Notebook ID.")}, []string{"notebook_id"}),
		tool("open_notebook_create_notebook", "Create a notebook for a research or publishing workflow.", map[string]any{"name": stringProp("Notebook name."), "description": stringProp("Optional description.")}, []string{"name"}),
		tool("open_notebook_list_sources", "List sources, optionally within a notebook.", map[string]any{"notebook_id": stringProp("Optional notebook ID."), "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 100}}, nil),
		tool("open_notebook_get_source", "Read one source and its processed content.", map[string]any{"source_id": stringProp("Source ID.")}, []string{"source_id"}),
		tool("open_notebook_add_url_source", "Add a public URL as a source. For a signed-in webpage, first use Soulacy's authenticated website tools to retrieve the permitted content, then add it with open_notebook_add_text_source.", map[string]any{"url": stringProp("Public source URL."), "title": stringProp("Optional title."), "notebooks": stringsProp("Notebook IDs."), "transformations": stringsProp("Transformation IDs."), "embed": boolProp("Create vector embeddings."), "async_processing": boolProp("Process asynchronously.")}, []string{"url"}),
		tool("open_notebook_add_text_source", "Add supplied text as an Open Notebook source. Use this for content retrieved through a Soulacy authenticated website connection.", map[string]any{"content": stringProp("Text content, up to 2 MiB."), "title": stringProp("Optional title."), "notebooks": stringsProp("Notebook IDs."), "transformations": stringsProp("Transformation IDs."), "embed": boolProp("Create vector embeddings."), "async_processing": boolProp("Process asynchronously.")}, []string{"content"}),
		tool("open_notebook_get_source_status", "Check asynchronous source processing status.", map[string]any{"source_id": stringProp("Source ID.")}, []string{"source_id"}),
		tool("open_notebook_search", "Search Open Notebook sources and notes.", map[string]any{"query": stringProp("Search query."), "type": map[string]any{"type": "string", "enum": []string{"text", "vector"}}, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 1000}, "search_sources": boolProp("Search sources."), "search_notes": boolProp("Search notes."), "minimum_score": map[string]any{"type": "number", "minimum": 0, "maximum": 1}}, []string{"query"}),
		tool("open_notebook_list_models", "List configured Open Notebook models. Language model IDs are required by open_notebook_ask.", map[string]any{"type": stringProp("Optional model type, such as language or embedding.")}, nil),
		tool("open_notebook_ask", "Ask Open Notebook a question using configured language models.", map[string]any{"question": stringProp("Question to answer."), "strategy_model": stringProp("Language model ID for query strategy."), "answer_model": stringProp("Language model ID for source answers."), "final_answer_model": stringProp("Language model ID for the final answer.")}, []string{"question", "strategy_model", "answer_model", "final_answer_model"}),
		tool("open_notebook_list_notes", "List notes, optionally within a notebook.", map[string]any{"notebook_id": stringProp("Optional notebook ID.")}, nil),
		tool("open_notebook_create_note", "Create a human or AI note in Open Notebook.", map[string]any{"title": stringProp("Optional title."), "content": stringProp("Note content, up to 2 MiB."), "note_type": map[string]any{"type": "string", "enum": []string{"human", "ai"}}, "notebook_id": stringProp("Optional notebook ID.")}, []string{"content"}),
		tool("open_notebook_list_episode_profiles", "List podcast episode profiles and their IDs.", nil, nil),
		tool("open_notebook_list_speaker_profiles", "List podcast speaker profiles and their IDs.", nil, nil),
		tool("open_notebook_generate_podcast", "Start podcast generation from notebook sources or supplied content.", map[string]any{"episode_profile": stringProp("Episode profile ID."), "speaker_profile": stringProp("Speaker profile ID."), "episode_name": stringProp("Episode name."), "content": stringProp("Optional source content."), "notebook_id": stringProp("Optional notebook ID."), "briefing_suffix": stringProp("Optional extra production direction.")}, []string{"episode_profile", "speaker_profile", "episode_name"}),
		tool("open_notebook_get_podcast_job", "Get podcast generation job status.", map[string]any{"job_id": stringProp("Podcast job ID.")}, []string{"job_id"}),
		tool("open_notebook_list_podcast_episodes", "List generated podcast episodes.", nil, nil),
		tool("open_notebook_get_podcast_episode", "Get podcast episode metadata, transcript, outline, status, and error details.", map[string]any{"episode_id": stringProp("Episode ID.")}, []string{"episode_id"}),
		tool("open_notebook_get_podcast_audio", "Return the host-local audio download URL for a podcast episode.", map[string]any{"episode_id": stringProp("Episode ID.")}, []string{"episode_id"}),
	}
}

func tool(name, description string, properties map[string]any, required []string) map[string]any {
	if properties == nil {
		properties = map[string]any{}
	}
	schema := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
	if len(required) > 0 {
		schema["required"] = required
	}
	return map[string]any{"name": name, "description": description, "inputSchema": schema}
}
