// Copyright (c) 2026 Neomantra Corp

// Package llm is the model-backed half of the personal mode: a small client
// that asks Claude for one JSON document conforming to a schema.
//
// It is deliberately narrow. csq does not stream model prose to the user, does
// not hold a conversation, and never lets a model touch a database — the model
// writes a mode file, csq validates it, and every number the user sees comes
// from DuckDB executing SQL the user can read. That boundary is what keeps a
// generated profile auditable: the artefact is text on disk, not a claim.
package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// DefaultModel is the model used unless CSQ_LLM_MODEL or --model says otherwise.
const DefaultModel = "claude-opus-5"

// DefaultEffort trades tokens for care. Authoring a mode means reading a
// schema, a table inventory, and a question, then emitting SQL that must be
// valid on the first try against columns the model cannot probe — the failure
// mode of thinking too little is a file that does not load, so this leans high.
const DefaultEffort = "high"

// maxTokens is generous because a mode document carries several queries, each
// with real SQL and a description, plus caveats. Truncation here is not a
// degraded answer but an unparseable one, so the request streams (see JSON).
const maxTokens = 32000

// Options configure a Client.
type Options struct {
	// Model is the Claude model id. Empty means DefaultModel.
	Model string
	// Effort is one of low, medium, high, xhigh, max. Empty means DefaultEffort.
	Effort string
	// APIKey overrides ambient credentials. Empty uses the SDK's own
	// resolution: ANTHROPIC_API_KEY, then ANTHROPIC_AUTH_TOKEN, then a profile
	// written by `ant auth login`.
	APIKey string
}

// Config is the model selection, resolved from options, the environment, and
// the defaults, in that order.
//
// It is separate from Client because it needs no credentials. The draft cache
// keys on the model and effort, so csq has to know them to decide whether a
// cached draft applies — and it must be able to answer that question, and serve
// the draft, without an API key. A cache hit makes no network call, so
// demanding a credential for one would be asking for something it will not use.
type Config struct {
	Model  string
	Effort string
}

// ResolveConfig determines the model and effort without contacting anything or
// requiring a credential.
func ResolveConfig(opts Options) (Config, error) {
	cfg := Config{
		Model:  firstNonEmpty(opts.Model, os.Getenv("CSQ_LLM_MODEL"), DefaultModel),
		Effort: firstNonEmpty(opts.Effort, os.Getenv("CSQ_LLM_EFFORT"), DefaultEffort),
	}
	if _, err := parseEffort(cfg.Effort); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Client wraps the Anthropic API for csq's one use of it.
type Client struct {
	api    anthropic.Client
	model  string
	effort anthropic.OutputConfigEffort
}

// New builds a client, resolving the model and effort from options, then the
// environment, then the defaults.
func New(opts Options) (*Client, error) {
	cfg, err := ResolveConfig(opts)
	if err != nil {
		return nil, err
	}
	e, err := parseEffort(cfg.Effort)
	if err != nil {
		return nil, err
	}

	var sdkOpts []option.RequestOption
	if opts.APIKey != "" {
		sdkOpts = append(sdkOpts, option.WithAPIKey(opts.APIKey))
	} else if !HaveCredentials() {
		return nil, ErrNoCredentials
	}

	return &Client{
		api:    anthropic.NewClient(sdkOpts...),
		model:  cfg.Model,
		effort: e,
	}, nil
}

// ErrNoCredentials reports that no Anthropic credential could be found. The
// message names both ways to supply one, because the SDK reads a profile that
// an unset ANTHROPIC_API_KEY does not rule out.
var ErrNoCredentials = fmt.Errorf(
	"no Anthropic credentials found.\n" +
		"  csq needs them only for the personal mode, which asks Claude to draft a\n" +
		"  mode file; every other mode runs entirely locally.\n" +
		"  Either:\n" +
		"    export ANTHROPIC_API_KEY=sk-ant-...\n" +
		"  or log in once, which stores a profile the SDK finds on its own:\n" +
		"    ant auth login")

// HaveCredentials reports whether some Anthropic credential is reachable.
//
// This deliberately mirrors the SDK's resolution order rather than checking
// ANTHROPIC_API_KEY alone: a user who ran `ant auth login` has working
// credentials and no env var, and telling them to export a key would be wrong.
func HaveCredentials() bool {
	for _, env := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN"} {
		if strings.TrimSpace(os.Getenv(env)) != "" {
			return true
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	// Written by `ant auth login`; presence is a good enough signal, and a
	// stale profile surfaces as an API error with the real reason.
	if _, err := os.Stat(filepath.Join(home, ".config", "anthropic")); err == nil {
		return true
	}
	return false
}

// Model reports the model this client will call, for provenance records.
func (c *Client) Model() string { return c.model }

// Effort reports the configured effort level, for provenance records.
func (c *Client) Effort() string { return string(c.effort) }

// JSONRequest is one ask: a system prompt, a user prompt, and the schema the
// answer must satisfy.
type JSONRequest struct {
	System string
	User   string
	// Schema is a JSON Schema. The API constrains the response to it, which is
	// why the caller can unmarshal the result without defensive parsing.
	Schema map[string]any
}

// Usage is what one call cost, as the API reported it.
//
// It is carried back so the cache can record it, which is the only way the
// saving from a cache hit can be stated as a number rather than asserted. A
// cache nobody can measure is a cache nobody can size.
type Usage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
	// CacheReadTokens is Anthropic's own prompt cache, which is a different
	// mechanism from csq's draft cache: it discounts the repeated system prompt
	// within a session, where the draft cache skips the request entirely.
	CacheReadTokens int64 `json:"cache_read_tokens,omitempty"`
}

// Total is the tokens billed for one call, ignoring the prompt-cache discount.
func (u Usage) Total() int64 { return u.InputTokens + u.OutputTokens }

// Result is one model reply and what it cost.
type Result struct {
	Bytes []byte
	Usage Usage
}

// JSON asks the model for a single JSON document matching req.Schema.
//
// The request streams. That is not for display — nothing is shown as it
// arrives — but because a long structured response on a non-streaming request
// can exceed the SDK's HTTP timeout, and a timeout here costs the whole
// authoring run.
func (c *Client) JSON(ctx context.Context, req JSONRequest) (*Result, error) {
	if len(req.Schema) == 0 {
		return nil, fmt.Errorf("llm: JSON requires a schema")
	}

	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(c.model),
		MaxTokens: maxTokens,
		System: []anthropic.TextBlockParam{{
			Text: req.System,
			// The system prompt carries the schema and the authoring rules and
			// does not vary between the questions a user asks in a session, so
			// it is worth caching across repeated `csq modes personal` runs.
			CacheControl: anthropic.NewCacheControlEphemeralParam(),
		}},
		Thinking: anthropic.ThinkingConfigParamUnion{
			OfAdaptive: &anthropic.ThinkingConfigAdaptiveParam{},
		},
		OutputConfig: anthropic.OutputConfigParam{
			Effort: c.effort,
			Format: anthropic.JSONOutputFormatParam{Schema: req.Schema},
		},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(req.User)),
		},
	}

	stream := c.api.Messages.NewStreaming(ctx, params)
	var msg anthropic.Message
	for stream.Next() {
		if err := msg.Accumulate(stream.Current()); err != nil {
			return nil, fmt.Errorf("llm: accumulate response: %w", err)
		}
	}
	if err := stream.Err(); err != nil {
		return nil, fmt.Errorf("llm: %w", err)
	}

	// A refusal is a successful HTTP call with no usable content. Reporting it
	// as "empty response" would send the user hunting for a bug that is not
	// there, so it is named.
	if msg.StopReason == anthropic.StopReasonRefusal {
		return nil, fmt.Errorf("llm: the model declined this request (%s). "+
			"Rephrase the question, or write the mode file by hand — "+
			"see 'csq modes schema'", refusalDetail(msg))
	}
	if msg.StopReason == anthropic.StopReasonMaxTokens {
		return nil, fmt.Errorf("llm: the draft was cut off at the token limit. " +
			"Ask for fewer queries, or narrow the question")
	}

	var text strings.Builder
	for _, block := range msg.Content {
		if t, ok := block.AsAny().(anthropic.TextBlock); ok {
			text.WriteString(t.Text)
		}
	}
	out := strings.TrimSpace(text.String())
	if out == "" {
		return nil, fmt.Errorf("llm: the model returned no content")
	}
	if !json.Valid([]byte(out)) {
		return nil, fmt.Errorf("llm: the model returned content that is not JSON")
	}
	return &Result{
		Bytes: []byte(out),
		Usage: Usage{
			InputTokens:     msg.Usage.InputTokens,
			OutputTokens:    msg.Usage.OutputTokens,
			CacheReadTokens: msg.Usage.CacheReadInputTokens,
		},
	}, nil
}

func refusalDetail(msg anthropic.Message) string {
	if d := strings.TrimSpace(msg.StopDetails.Explanation); d != "" {
		return d
	}
	if c := strings.TrimSpace(string(msg.StopDetails.Category)); c != "" {
		return c
	}
	return "no reason given"
}

func parseEffort(s string) (anthropic.OutputConfigEffort, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "low":
		return anthropic.OutputConfigEffortLow, nil
	case "medium":
		return anthropic.OutputConfigEffortMedium, nil
	case "high":
		return anthropic.OutputConfigEffortHigh, nil
	case "xhigh":
		return anthropic.OutputConfigEffortXhigh, nil
	case "max":
		return anthropic.OutputConfigEffortMax, nil
	}
	return "", fmt.Errorf("unknown effort %q (use low, medium, high, xhigh, or max)", s)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}
