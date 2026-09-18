package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The LLM is the sole ORIGINATOR of every directive_interpretation entry.
// The deterministic code in this file may only normalize, correct one axis of,
// or suppress an LLM-originated directive - never originate one itself, except
// in the explicit last-resort safe-failure path when every provider is down.

type Provider struct {
	Name    string
	BaseURL string
	APIKey  string
	Model   string
	// ReasoningEffort is sent only to providers that accept it (Groq gpt-oss).
	// Sending it to Gemini is rejected, so it stays empty there.
	ReasoningEffort string
}

type Interpreter struct {
	providers []Provider
	client    *http.Client
	timeout   time.Duration
	cache     sync.Map // string -> DirectiveInterpretation
}

func env(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

func NewInterpreter() *Interpreter {
	avail := map[string]Provider{}
	if k := env("GEMINI_API_KEY", ""); k != "" {
		avail["gemini"] = Provider{"gemini",
			env("GEMINI_BASE_URL", "https://generativelanguage.googleapis.com/v1beta/openai/"), k,
			env("GEMINI_MODEL", "gemini-2.5-flash"),
			// Gemini 2.5 Flash thinks by default; this extraction task does not
			// need it and disabling it is ~3x faster for identical output.
			env("GEMINI_REASONING_EFFORT", "none")}
	}
	if k := env("GROQ_API_KEY", ""); k != "" {
		m := env("GROQ_MODEL", "openai/gpt-oss-120b")
		eff := ""
		if strings.Contains(m, "gpt-oss") {
			eff = env("GROQ_REASONING_EFFORT", "low")
		}
		avail["groq"] = Provider{"groq",
			env("GROQ_BASE_URL", "https://api.groq.com/openai/v1"), k, m, eff}
	}
	if k := env("XAI_API_KEY", ""); k != "" {
		avail["xai"] = Provider{"xai", env("XAI_BASE_URL", "https://api.x.ai/v1"), k,
			env("XAI_MODEL", "grok-4-fast"), ""}
	}
	// Order matters: the first provider carries normal traffic. Gemini leads
	// because Groq's free tier caps at 8000 tokens/min, which this ~3k-token
	// system prompt exhausts after two concurrent notes.
	var ps []Provider
	for _, name := range strings.Split(env("LLM_PROVIDER_ORDER", "gemini,groq,xai"), ",") {
		if p, ok := avail[strings.TrimSpace(name)]; ok {
			ps = append(ps, p)
			delete(avail, strings.TrimSpace(name))
		}
	}
	for _, p := range avail {
		ps = append(ps, p)
	}
	secs, _ := strconv.Atoi(env("LLM_TIMEOUT_SECONDS", "6"))
	if secs <= 0 {
		secs = 6
	}
	return &Interpreter{providers: ps, timeout: time.Duration(secs) * time.Second,
		client: &http.Client{Timeout: time.Duration(secs+2) * time.Second}}
}

func (it *Interpreter) Ready() bool { return len(it.providers) > 0 }

func (it *Interpreter) ProviderNames() []string {
	out := make([]string, len(it.providers))
	for i, p := range it.providers {
		out[i] = p.Name + ":" + p.Model
	}
	return out
}

type chatReq struct {
	Model          string            `json:"model"`
	Messages       []chatMsg         `json:"messages"`
	Temperature    float64           `json:"temperature"`
	MaxTokens      int               `json:"max_tokens"`
	ResponseFormat map[string]string `json:"response_format,omitempty"`
	ReasoningEffort string           `json:"reasoning_effort,omitempty"`
}

// errAdvance marks a failure where retrying the same provider is pointless.
type errAdvance struct{ error }

type chatMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

var fenceRe = regexp.MustCompile("(?s)```(?:json)?(.*?)```")

// extractJSON recovers the first balanced JSON object from model output.
func extractJSON(s string) map[string]any {
	try := func(t string) map[string]any {
		var m map[string]any
		if json.Unmarshal([]byte(t), &m) == nil {
			return m
		}
		return nil
	}
	if m := try(strings.TrimSpace(s)); m != nil {
		return m
	}
	if g := fenceRe.FindStringSubmatch(s); len(g) > 1 {
		if m := try(strings.TrimSpace(g[1])); m != nil {
			return m
		}
	}
	depth, start := 0, -1
	for i, r := range s {
		if r == '{' {
			if depth == 0 {
				start = i
			}
			depth++
		} else if r == '}' {
			depth--
			if depth == 0 && start >= 0 {
				if m := try(s[start : i+1]); m != nil {
					return m
				}
				start = -1
			}
		}
	}
	return nil
}

func (it *Interpreter) call(ctx context.Context, p Provider, note string, bat Battery, nudge string) (map[string]any, error) {
	sys := SystemPrompt
	if nudge != "" {
		sys += "\n\n" + nudge
	}
	body, _ := json.Marshal(chatReq{
		Model: p.Model, Temperature: 0, MaxTokens: 1500,
		ResponseFormat:  map[string]string{"type": "json_object"},
		ReasoningEffort: p.ReasoningEffort,
		Messages:        []chatMsg{{"system", sys}, {"user", UserMessage(note, bat)}},
	})
	url := strings.TrimRight(p.BaseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.APIKey)

	resp, err := it.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != 200 {
		// Never surface provider bodies (may echo credentials) - status only.
		// 429/5xx mean this provider cannot serve us now: advancing to the next
		// one is strictly faster than retrying the same one.
		if resp.StatusCode == 429 || resp.StatusCode >= 500 {
			return nil, errAdvance{fmt.Errorf("%s http %d", p.Name, resp.StatusCode)}
		}
		return nil, fmt.Errorf("%s http %d", p.Name, resp.StatusCode)
	}
	var out struct {
		Choices []struct {
			Message struct{ Content string } `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || len(out.Choices) == 0 {
		return nil, fmt.Errorf("%s: unparseable envelope", p.Name)
	}
	m := extractJSON(out.Choices[0].Message.Content)
	if m == nil {
		return nil, fmt.Errorf("%s: no JSON object in content", p.Name)
	}
	return m, nil
}

// interpretOne walks the repair ladder for a single note.
func (it *Interpreter) interpretOne(ctx context.Context, idx int, note string, bat Battery) DirectiveInterpretation {
	norm := NormalizeNote(note)
	if norm == "" {
		return NoOpEntry(idx, "")
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%g|%g", norm, bat.CapacityKwh, bat.MinimumEnergyKwh)))
	key := hex.EncodeToString(sum[:])
	if v, ok := it.cache.Load(key); ok {
		e := v.(DirectiveInterpretation)
		e.NoteIndex = idx
		return e
	}

	var raw map[string]any
	for _, p := range it.providers {
		for attempt := 0; attempt < 2; attempt++ {
			cctx, cancel := context.WithTimeout(ctx, it.timeout)
			nudge := ""
			if attempt == 1 {
				nudge = "Your previous output was not valid JSON. Return ONLY the structured object."
			}
			m, err := it.call(cctx, p, norm, bat, nudge)
			cancel()
			if err == nil && m != nil {
				raw = m
				break
			}
			log.Printf("interpret: provider=%s attempt=%d err=%v", p.Name, attempt, err)
			if _, skip := err.(errAdvance); skip {
				break // rate limited or provider down - try the next provider now
			}
		}
		if raw != nil {
			break
		}
	}

	var entry DirectiveInterpretation
	if raw == nil {
		// Every provider failed. Safe failure: never crash, never invent.
		entry = fallbackExtract(idx, norm, bat)
	} else {
		raw = crossCheck(raw, norm) // deterministic single-axis correction
		var flags []string
		entry, flags = BuildEntry(idx, raw, bat)
		if len(flags) > 0 {
			log.Printf("interpret: note=%d flags=%v", idx, flags)
		}
	}
	it.cache.Store(key, entry)
	return entry
}

// InterpretAll runs one call per note concurrently. note_index comes from
// position in this slice, so ordering and completeness are structural.
func (it *Interpreter) InterpretAll(ctx context.Context, notes []string, bat Battery) []DirectiveInterpretation {
	out := make([]DirectiveInterpretation, len(notes))
	var wg sync.WaitGroup
	for i, n := range notes {
		wg.Add(1)
		go func(i int, n string) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					log.Printf("interpret: panic on note %d: %v", i, r)
					out[i] = NoOpEntry(i, "")
				}
			}()
			out[i] = it.interpretOne(ctx, i, n, bat)
		}(i, n)
	}
	wg.Wait()
	return out
}
