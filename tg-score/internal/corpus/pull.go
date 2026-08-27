package corpus

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// DefaultHost is Telegraph's public node. Overridable via `tg-score pull -host`.
const DefaultHost = "http://13.237.89.59:7044"

// pageLimit is the largest page the server will actually return. Asking for more
// is not an error — the server silently caps the response at 500 — so a caller
// that assumed its requested limit was honoured would skip rows on every page.
const pageLimit = 500

// PullOptions configures a corpus pull.
type PullOptions struct {
	Host     string
	Max      int    // stop after this many rows; 0 means all available
	Intent   string // restrict to one intent; empty means all
	Timeout  time.Duration
	Progress func(fetched, total int)
}

// envelope is the /scores response shape, including the pagination fields the
// Row-only Payload ignores.
type envelope struct {
	Scores []Row `json:"scores"`
	Total  int   `json:"total"`
	Limit  int   `json:"limit"`
	Offset int   `json:"offset"`
}

// Pull fetches rows from GET /scores, paginating until the corpus is exhausted
// or opts.Max is reached.
//
// The endpoint reports a `total` (19,526 at epoch 285) far larger than one page,
// and the published research corpus was a single 500-row page. Pulling the full
// set is what lets gate 3 be measured across every intent the network has scored
// rather than the six that happened to land in the first page — which is the
// difference between knowing the fork passes and hoping it does.
func Pull(opts PullOptions) ([]Row, error) {
	if opts.Host == "" {
		opts.Host = DefaultHost
	}
	if opts.Timeout == 0 {
		opts.Timeout = 60 * time.Second
	}
	client := &http.Client{Timeout: opts.Timeout}

	var out []Row
	seen := map[string]bool{} // guards against overlapping pages if rows shift mid-pull
	total := -1

	for offset := 0; ; offset += pageLimit {
		env, err := fetchPage(client, opts, offset)
		if err != nil {
			// A partial corpus is still usable; surface what we got alongside the
			// error so the caller can decide rather than losing 30 pages of work.
			if len(out) > 0 {
				return out, fmt.Errorf("pull stopped at offset %d after %d rows: %w", offset, len(out), err)
			}
			return nil, err
		}
		if total < 0 {
			total = env.Total
		}
		if len(env.Scores) == 0 {
			break
		}
		for _, r := range env.Scores {
			if r.ID != "" && seen[r.ID] {
				continue
			}
			if r.ID != "" {
				seen[r.ID] = true
			}
			out = append(out, r)
		}
		if opts.Progress != nil {
			opts.Progress(len(out), total)
		}
		if opts.Max > 0 && len(out) >= opts.Max {
			out = out[:opts.Max]
			break
		}
		if total >= 0 && offset+pageLimit >= total {
			break
		}
		if len(env.Scores) < pageLimit {
			break
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("pull returned no rows from %s", opts.Host)
	}
	return out, nil
}

func fetchPage(client *http.Client, opts PullOptions, offset int) (*envelope, error) {
	q := url.Values{}
	q.Set("limit", strconv.Itoa(pageLimit))
	q.Set("offset", strconv.Itoa(offset))
	if opts.Intent != "" {
		q.Set("intent", opts.Intent)
	}
	endpoint := opts.Host + "/scores?" + q.Encode()

	resp, err := client.Get(endpoint)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", endpoint, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("GET %s: HTTP %d: %s", endpoint, resp.StatusCode, body)
	}
	var env envelope
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return nil, fmt.Errorf("decode %s: %w", endpoint, err)
	}
	return &env, nil
}

// Registry is one record from GET /engine/v1/intents/{intent}/wasm — the public
// script registry, and the only feedback channel a submitted script has.
type Registry struct {
	RegistrationID   int     `json:"RegistrationID"`
	AuthorAddress    string  `json:"AuthorAddress"`
	WasmURL          string  `json:"WasmURL"`
	WasmHash         string  `json:"WasmHash"`
	ActivationStatus string  `json:"ActivationStatus"`
	IntentID         string  `json:"IntentID"`
	BondAmount       float64 `json:"BondAmount"`
	RejectionReason  string  `json:"RejectionReason"`
	EvalErrorCount   int     `json:"EvalErrorCount"`
	RegisteredAt     string  `json:"RegisteredAt"`
	UpdatedAt        string  `json:"UpdatedAt"`
}

type registryEnvelope struct {
	Count  int        `json:"count"`
	Intent string     `json:"intent_id"`
	Wasm   []Registry `json:"wasm"`
}

// PullRegistry fetches the script registry for one intent.
//
// Note that the endpoint returns every registration network-wide rather than
// only those for the named intent — the five known records come back whichever
// intent is queried, and each record carries its own IntentID.
func PullRegistry(host, intent string, timeout time.Duration) ([]Registry, error) {
	if host == "" {
		host = DefaultHost
	}
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	client := &http.Client{Timeout: timeout}
	endpoint := fmt.Sprintf("%s/engine/v1/intents/%s/wasm", host, url.PathEscape(intent))

	resp, err := client.Get(endpoint)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", endpoint, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("GET %s: HTTP %d: %s", endpoint, resp.StatusCode, body)
	}
	var env registryEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return nil, fmt.Errorf("decode %s: %w", endpoint, err)
	}
	return env.Wasm, nil
}
