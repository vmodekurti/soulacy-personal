package reasoning

func phaseParamsWithDefaults(in PhaseParams, maxTokens int, temperature float64, responseFormat string) PhaseParams {
	out := in
	if out.MaxTokens <= 0 {
		out.MaxTokens = maxTokens
	}
	if out.Temperature <= 0 && out.TopP <= 0 {
		out.Temperature = temperature
	}
	if out.ResponseFormat == "" {
		out.ResponseFormat = responseFormat
	}
	return out
}
