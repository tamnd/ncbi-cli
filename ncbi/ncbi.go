// Package ncbi is the library behind the ncbi command line:
// the HTTP client, request shaping, and the typed data models for the NCBI
// Entrez eUtils API.
//
// The Client here is the spine every command shares. It sets a real
// User-Agent, paces requests so a busy session stays polite, and retries the
// transient failures (429 and 5xx) that any public API throws under load.
// Build your endpoint calls and JSON decoding on top of it.
package ncbi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// DefaultUserAgent identifies the client to NCBI. A real, honest
// User-Agent is both polite and the thing most likely to keep you unblocked.
const DefaultUserAgent = "ncbi-cli/dev (+https://github.com/tamnd/ncbi-cli)"

// Host is the PubMed site, used for Locate / URI resolution.
const Host = "pubmed.ncbi.nlm.nih.gov"

// EUtilsBase is the root every eUtils request is built from.
const EUtilsBase = "https://eutils.ncbi.nlm.nih.gov/entrez/eutils"

// Config carries the tunable parameters for the NCBI client.
type Config struct {
	BaseURL   string
	APIKey    string
	UserAgent string
	Rate      time.Duration
	Timeout   time.Duration
	Retries   int
}

// DefaultConfig returns a Config with sensible defaults for the free-tier
// NCBI eUtils API (3 req/s without a key).
func DefaultConfig() Config {
	return Config{
		BaseURL:   EUtilsBase,
		APIKey:    os.Getenv("NCBI_API_KEY"),
		UserAgent: DefaultUserAgent,
		Rate:      400 * time.Millisecond,
		Timeout:   15 * time.Second,
		Retries:   3,
	}
}

// Client talks to the NCBI eUtils API over HTTP.
type Client struct {
	HTTP      *http.Client
	cfg       Config
	last      time.Time
}

// NewClient returns a Client with the default config.
func NewClient() *Client {
	return NewClientWithConfig(DefaultConfig())
}

// NewClientWithConfig returns a Client using the given config.
func NewClientWithConfig(cfg Config) *Client {
	if cfg.BaseURL == "" {
		cfg.BaseURL = EUtilsBase
	}
	return &Client{
		HTTP: &http.Client{Timeout: cfg.Timeout},
		cfg:  cfg,
	}
}

// Get fetches url and returns the response body. It paces and retries according
// to the client's settings. The caller owns nothing extra; the body is read
// fully and closed here.
func (c *Client) Get(ctx context.Context, rawURL string) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt <= c.cfg.Retries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff(attempt)):
			}
		}
		body, retry, err := c.do(ctx, rawURL)
		if err == nil {
			return body, nil
		}
		lastErr = err
		if !retry {
			return nil, err
		}
	}
	return nil, fmt.Errorf("get %s: %w", rawURL, lastErr)
}

func (c *Client) do(ctx context.Context, rawURL string) (body []byte, retry bool, err error) {
	c.pace()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("User-Agent", c.cfg.UserAgent)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, true, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return nil, true, fmt.Errorf("http %d", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("http %d", resp.StatusCode)
	}

	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, true, err
	}
	return b, false, nil
}

// pace blocks until at least Rate has passed since the previous request.
func (c *Client) pace() {
	if c.cfg.Rate <= 0 {
		return
	}
	if wait := c.cfg.Rate - time.Since(c.last); wait > 0 {
		time.Sleep(wait)
	}
	c.last = time.Now()
}

func backoff(attempt int) time.Duration {
	d := time.Duration(attempt) * 500 * time.Millisecond
	if d > 5*time.Second {
		d = 5 * time.Second
	}
	return d
}

// buildURL constructs an eUtils endpoint URL with the given parameters.
// It appends the API key if one is configured.
func (c *Client) buildURL(endpoint string, params url.Values) string {
	params.Set("retmode", "json")
	if c.cfg.APIKey != "" {
		params.Set("api_key", c.cfg.APIKey)
	}
	return c.cfg.BaseURL + "/" + endpoint + "?" + params.Encode()
}

// --- wire types (unexported) ---

type wireSearch struct {
	ESearchResult struct {
		Count  string   `json:"count"`
		RetMax string   `json:"retmax"`
		IDList []string `json:"idlist"`
	} `json:"esearchresult"`
}

type wireSummaryResponse struct {
	Result map[string]json.RawMessage `json:"result"`
}

type wireArticle struct {
	UID         string `json:"uid"`
	Title       string `json:"title"`
	Source      string `json:"source"`
	PubDate     string `json:"pubdate"`
	Volume      string `json:"volume"`
	Issue       string `json:"issue"`
	Pages       string `json:"pages"`
	PMID        string `json:"pmid"`
	ELocationID []struct {
		EIdType string `json:"eidtype"`
		ID      string `json:"id"`
	} `json:"elocationid"`
	Authors []struct {
		Name     string `json:"name"`
		AuthType string `json:"authtype"`
	} `json:"authors"`
}

type wireGene struct {
	UID         string `json:"uid"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Organism    struct {
		ScientificName string `json:"scientificname"`
	} `json:"organism"`
	Summary      string `json:"summary"`
	Chromosome   string `json:"chromosome"`
	GeneticSource string `json:"geneticsource"`
}

// --- public output types ---

// Article is a PubMed article record.
type Article struct {
	PMID    string   `json:"pmid" kit:"id"`
	Title   string   `json:"title"`
	Authors []string `json:"authors"`
	Journal string   `json:"journal"`
	PubDate string   `json:"pub_date"`
	Volume  string   `json:"volume,omitempty"`
	Pages   string   `json:"pages,omitempty"`
}

// Gene is a record from the NCBI Gene database.
type Gene struct {
	ID         string `json:"id" kit:"id"`
	Symbol     string `json:"symbol"`
	Name       string `json:"name"`
	Organism   string `json:"organism"`
	Chromosome string `json:"chromosome,omitempty"`
	Summary    string `json:"summary,omitempty"`
}

// SearchResult is a generic search result: IDs and count from any NCBI DB.
type SearchResult struct {
	DB    string   `json:"db" kit:"id"`
	Count int      `json:"count"`
	IDs   []string `json:"ids"`
}

// --- client methods ---

// Search runs an esearch query against the named database and returns a
// SearchResult with the matching IDs (up to limit).
func (c *Client) Search(ctx context.Context, db, query string, limit int) (*SearchResult, error) {
	if limit <= 0 {
		limit = 10
	}
	params := url.Values{
		"db":     {db},
		"term":   {query},
		"retmax": {strconv.Itoa(limit)},
	}
	body, err := c.Get(ctx, c.buildURL("esearch.fcgi", params))
	if err != nil {
		return nil, err
	}
	var ws wireSearch
	if err := json.Unmarshal(body, &ws); err != nil {
		return nil, fmt.Errorf("esearch parse: %w", err)
	}
	count, _ := strconv.Atoi(ws.ESearchResult.Count)
	ids := ws.ESearchResult.IDList
	if ids == nil {
		ids = []string{}
	}
	return &SearchResult{DB: db, Count: count, IDs: ids}, nil
}

// SearchPubMed searches PubMed and returns full Article records for the top
// results (up to limit).
func (c *Client) SearchPubMed(ctx context.Context, query string, limit int) ([]*Article, error) {
	sr, err := c.Search(ctx, "pubmed", query, limit)
	if err != nil {
		return nil, err
	}
	if len(sr.IDs) == 0 {
		return nil, nil
	}
	return c.FetchArticles(ctx, sr.IDs)
}

// GetArticle fetches a single PubMed article by PMID.
func (c *Client) GetArticle(ctx context.Context, pmid string) (*Article, error) {
	arts, err := c.FetchArticles(ctx, []string{pmid})
	if err != nil {
		return nil, err
	}
	if len(arts) == 0 {
		return nil, fmt.Errorf("pmid %s: not found", pmid)
	}
	return arts[0], nil
}

// FetchArticles fetches esummary for a batch of PMIDs and returns Article records.
func (c *Client) FetchArticles(ctx context.Context, pmids []string) ([]*Article, error) {
	params := url.Values{
		"db": {"pubmed"},
		"id": {strings.Join(pmids, ",")},
	}
	body, err := c.Get(ctx, c.buildURL("esummary.fcgi", params))
	if err != nil {
		return nil, err
	}
	var wsr wireSummaryResponse
	if err := json.Unmarshal(body, &wsr); err != nil {
		return nil, fmt.Errorf("esummary parse: %w", err)
	}
	// get ordered UIDs
	uids, err := parseUIDs(wsr.Result)
	if err != nil {
		return nil, err
	}
	var out []*Article
	for _, uid := range uids {
		raw, ok := wsr.Result[uid]
		if !ok {
			continue
		}
		var wa wireArticle
		if err := json.Unmarshal(raw, &wa); err != nil {
			continue
		}
		out = append(out, articleFromWire(uid, wa))
	}
	return out, nil
}

// SearchGenes searches the Gene database and returns full Gene records.
func (c *Client) SearchGenes(ctx context.Context, query string, limit int) ([]*Gene, error) {
	sr, err := c.Search(ctx, "gene", query, limit)
	if err != nil {
		return nil, err
	}
	if len(sr.IDs) == 0 {
		return nil, nil
	}
	return c.FetchGenes(ctx, sr.IDs)
}

// FetchGenes fetches esummary for a batch of Gene IDs.
func (c *Client) FetchGenes(ctx context.Context, ids []string) ([]*Gene, error) {
	params := url.Values{
		"db": {"gene"},
		"id": {strings.Join(ids, ",")},
	}
	body, err := c.Get(ctx, c.buildURL("esummary.fcgi", params))
	if err != nil {
		return nil, err
	}
	var wsr wireSummaryResponse
	if err := json.Unmarshal(body, &wsr); err != nil {
		return nil, fmt.Errorf("esummary parse: %w", err)
	}
	uids, err := parseUIDs(wsr.Result)
	if err != nil {
		return nil, err
	}
	var out []*Gene
	for _, uid := range uids {
		raw, ok := wsr.Result[uid]
		if !ok {
			continue
		}
		var wg wireGene
		if err := json.Unmarshal(raw, &wg); err != nil {
			continue
		}
		out = append(out, geneFromWire(uid, wg))
	}
	return out, nil
}

// --- helpers ---

// parseUIDs extracts the ordered list of UIDs from the esummary result map.
func parseUIDs(result map[string]json.RawMessage) ([]string, error) {
	raw, ok := result["uids"]
	if !ok {
		return nil, nil
	}
	var uids []string
	if err := json.Unmarshal(raw, &uids); err != nil {
		return nil, fmt.Errorf("parse uids: %w", err)
	}
	return uids, nil
}

// articleFromWire converts the wire representation to the public Article type.
func articleFromWire(uid string, wa wireArticle) *Article {
	pmid := wa.PMID
	if pmid == "" {
		pmid = wa.UID
	}
	if pmid == "" {
		pmid = uid
	}
	var authors []string
	for _, a := range wa.Authors {
		if a.AuthType == "Author" || a.AuthType == "" {
			if a.Name != "" {
				authors = append(authors, a.Name)
			}
		}
	}
	if authors == nil {
		authors = []string{}
	}
	return &Article{
		PMID:    pmid,
		Title:   wa.Title,
		Authors: authors,
		Journal: wa.Source,
		PubDate: wa.PubDate,
		Volume:  wa.Volume,
		Pages:   wa.Pages,
	}
}

// geneFromWire converts the wire representation to the public Gene type.
func geneFromWire(uid string, wg wireGene) *Gene {
	id := wg.UID
	if id == "" {
		id = uid
	}
	return &Gene{
		ID:         id,
		Symbol:     wg.Name,
		Name:       wg.Description,
		Organism:   wg.Organism.ScientificName,
		Chromosome: wg.Chromosome,
		Summary:    wg.Summary,
	}
}
