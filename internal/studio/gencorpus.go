package studio

import (
	"encoding/json"
	"os"
)

// gencorpus.go is the generation-robustness eval corpus in NON-test code so both
// the unit test and `sy eval generation` can run it. Each sample is a raw draft
// as a builder model might emit it, plus whether the deterministic pipeline
// (NormalizeAndCheck) SHOULD be able to turn it into a valid, renderable flow.
// It runs with no LLM, so it is deterministic and CI-safe.

// GenSample is one raw generated draft plus its expected recoverability.
type GenSample struct {
	Name        string `json:"name"`
	Raw         string `json:"raw"`
	Recoverable bool   `json:"recoverable"`
}

// GenReport is the outcome of scoring a corpus.
type GenReport struct {
	RecoverableTotal int          `json:"recoverable_total"`
	Recovered        int          `json:"recovered"`
	Failures         []GenFailure `json:"failures,omitempty"`
}

// GenFailure records a sample whose actual validity didn't match expectation.
type GenFailure struct {
	Name     string   `json:"name"`
	Expected bool     `json:"expected_valid"`
	Errors   []string `json:"errors,omitempty"`
}

// Rate is the deterministic recovery percentage over recoverable samples.
func (r GenReport) Rate() float64 {
	if r.RecoverableTotal == 0 {
		return 100
	}
	return 100 * float64(r.Recovered) / float64(r.RecoverableTotal)
}

// RunGenerationCorpus scores each sample through NormalizeAndCheck: a recoverable
// sample must become valid; an unrecoverable one must still be flagged. Returns a
// report plus the mismatches.
func RunGenerationCorpus(samples []GenSample) GenReport {
	var rep GenReport
	for _, s := range samples {
		got := NormalizeAndCheck(s.Raw)
		if s.Recoverable {
			rep.RecoverableTotal++
			if got.Valid {
				rep.Recovered++
			} else {
				rep.Failures = append(rep.Failures, GenFailure{Name: s.Name, Expected: true, Errors: got.Errors})
			}
		} else if got.Valid {
			rep.Failures = append(rep.Failures, GenFailure{Name: s.Name, Expected: false})
		}
	}
	return rep
}

// LoadGenSamples parses a JSON array of GenSample (for user/CI-supplied corpora,
// e.g. cases distilled from accepted repairs).
func LoadGenSamples(data []byte) ([]GenSample, error) {
	var out []GenSample
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// AppendGenSample records a corpus case at path (a JSON array file), replacing
// any existing sample with the same Name and capping the file at maxKeep newest
// entries. Used to turn each accepted real-world repair into a durable
// regression case the generation eval can replay. Best-effort; a missing/corrupt
// file is treated as empty.
func AppendGenSample(path string, s GenSample, maxKeep int) error {
	if path == "" || s.Name == "" {
		return nil
	}
	var existing []GenSample
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &existing)
	}
	out := make([]GenSample, 0, len(existing)+1)
	for _, e := range existing {
		if e.Name != s.Name {
			out = append(out, e)
		}
	}
	out = append(out, s)
	if maxKeep > 0 && len(out) > maxKeep {
		out = out[len(out)-maxKeep:]
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// BuiltinGenerationCorpus is the field-failure-class corpus: fromJson in input,
// start-node kind, .trigger.message ref, channel.send-as-output, a clean
// baseline, and two unrecoverable controls (unclosed template, unknown kind).
func BuiltinGenerationCorpus() []GenSample {
	return []GenSample{
		{
			Name:        "clean_agent",
			Recoverable: true,
			Raw: `{"name":"Echo","trigger":{"type":"channel"},"flow":{
				"nodes":[{"id":"a","kind":"agent","agent":"helper","input":"{{ .trigger.text }}","output":"reply"}],
				"edges":[{"from":"a","to":"end"}],"entry":"a"}}`,
		},
		{
			Name:        "fromJson_in_input",
			Recoverable: true,
			Raw: `{"name":"F","trigger":{"type":"channel"},"flow":{
				"nodes":[
				  {"id":"e","kind":"llm","input":"{{ .trigger.text }}","output":"params"},
				  {"id":"p","kind":"python","code":"def run(inputs):\n    return inputs","input":"{{ fromJson .params }}","output":"out"}],
				"edges":[{"from":"e","to":"p"},{"from":"p","to":"end"}],"entry":"e"}}`,
		},
		{
			Name:        "start_node_kind",
			Recoverable: true,
			Raw: `{"name":"S","trigger":{"type":"channel"},"flow":{
				"nodes":[
				  {"id":"start1","kind":"start"},
				  {"id":"a","kind":"agent","agent":"h","input":"{{ .trigger.text }}","output":"r"}],
				"edges":[{"from":"start1","to":"a"},{"from":"a","to":"end"}],"entry":"start1"}}`,
		},
		{
			Name:        "trigger_message_ref",
			Recoverable: true,
			Raw: `{"name":"M","trigger":{"type":"channel"},"flow":{
				"nodes":[{"id":"a","kind":"agent","agent":"h","input":"{\"q\": {{ toJson .trigger.message }}}","output":"r"}],
				"edges":[{"from":"a","to":"end"}],"entry":"a"}}`,
		},
		{
			Name:        "channel_send_output",
			Recoverable: true,
			Raw: `{"name":"C","trigger":{"type":"channel"},"flow":{
				"nodes":[
				  {"id":"fmt","kind":"agent","agent":"f","input":"{{ .trigger.text }}","output":"msg"},
				  {"id":"send","kind":"tool","tool":"channel.send","input":"{\"text\": {{ toJson .msg }}}","output":"receipt"}],
				"edges":[{"from":"fmt","to":"send"},{"from":"send","to":"end"}],"entry":"fmt","output":"send"}}`,
		},
		{
			Name:        "unclosed_template",
			Recoverable: false,
			Raw: `{"name":"B","trigger":{"type":"channel"},"flow":{
				"nodes":[{"id":"a","kind":"agent","agent":"h","input":"{{ .trigger.text ","output":"r"}],
				"edges":[{"from":"a","to":"end"}],"entry":"a"}}`,
		},
		{
			Name:        "truly_unknown_kind",
			Recoverable: false,
			Raw: `{"name":"K","trigger":{"type":"channel"},"flow":{
				"nodes":[{"id":"a","kind":"sqlquery"}],
				"edges":[{"from":"a","to":"end"}],"entry":"a"}}`,
		},
	}
}
