package ncbi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// pubmedSummaryJSON is a minimal esummary response for PubMed article 34145924.
const pubmedSummaryJSON = `{
	"result": {
		"uids": ["34145924"],
		"34145924": {
			"uid": "34145924",
			"pmid": "34145924",
			"title": "A study of Go programming",
			"source": "Journal of Computing",
			"pubdate": "2021 Jun 15",
			"volume": "12",
			"issue": "3",
			"pages": "100-110",
			"authors": [
				{"name": "Smith J", "authtype": "Author"},
				{"name": "Jones M", "authtype": "Author"},
				{"name": "Corp X", "authtype": "CollectiveName"}
			],
			"elocationid": [{"eidtype": "doi", "id": "10.1000/test"}]
		}
	}
}`

// esearchJSON is a minimal esearch response.
const esearchJSON = `{
	"esearchresult": {
		"count": "42",
		"retmax": "2",
		"idlist": ["34145924", "12345678"]
	}
}`

// geneSummaryJSON is a minimal esummary response for Gene 672 (BRCA1).
const geneSummaryJSON = `{
	"result": {
		"uids": ["672"],
		"672": {
			"uid": "672",
			"name": "BRCA1",
			"description": "BRCA1 DNA repair associated",
			"organism": {"scientificname": "Homo sapiens"},
			"summary": "This gene encodes a tumour suppressor protein.",
			"chromosome": "17",
			"geneticsource": "genomic"
		}
	}
}`

func newTestServer(t *testing.T, routes map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" {
			t.Error("request carried no User-Agent")
		}
		path := r.URL.Path
		for route, body := range routes {
			if strings.Contains(path, route) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(body))
				return
			}
		}
		http.NotFound(w, r)
	}))
	return srv
}

func clientFor(srv *httptest.Server) *Client {
	cfg := DefaultConfig()
	cfg.BaseURL = srv.URL
	cfg.Rate = 0 // no pacing in tests
	cfg.Retries = 0
	return NewClientWithConfig(cfg)
}

func TestGet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" {
			t.Error("request carried no User-Agent")
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	c := NewClient()
	c.cfg.Rate = 0
	c.cfg.Retries = 0

	body, err := c.Get(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "ok" {
		t.Errorf("body = %q, want %q", body, "ok")
	}
}

func TestGetRetriesOn503(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if hits < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("recovered"))
	}))
	defer srv.Close()

	cfg := DefaultConfig()
	cfg.Rate = 0
	cfg.Retries = 5
	c := NewClientWithConfig(cfg)

	start := time.Now()
	body, err := c.Get(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "recovered" {
		t.Errorf("body = %q after retries", body)
	}
	if hits != 3 {
		t.Errorf("server saw %d hits, want 3", hits)
	}
	if time.Since(start) < 500*time.Millisecond {
		t.Error("retries did not back off")
	}
}

func TestFetchArticles(t *testing.T) {
	srv := newTestServer(t, map[string]string{
		"esummary.fcgi": pubmedSummaryJSON,
	})
	defer srv.Close()

	c := clientFor(srv)
	arts, err := c.FetchArticles(context.Background(), []string{"34145924"})
	if err != nil {
		t.Fatal(err)
	}
	if len(arts) != 1 {
		t.Fatalf("got %d articles, want 1", len(arts))
	}
	a := arts[0]
	if a.PMID != "34145924" {
		t.Errorf("PMID = %q, want 34145924", a.PMID)
	}
	if a.Title != "A study of Go programming" {
		t.Errorf("Title = %q", a.Title)
	}
	if a.Journal != "Journal of Computing" {
		t.Errorf("Journal = %q, want Journal of Computing", a.Journal)
	}
	if a.PubDate != "2021 Jun 15" {
		t.Errorf("PubDate = %q, want 2021 Jun 15", a.PubDate)
	}
	// Only AuthType=="Author" rows should be included; CollectiveName excluded
	if len(a.Authors) != 2 {
		t.Errorf("Authors = %v, want 2 entries", a.Authors)
	}
	if a.Authors[0] != "Smith J" {
		t.Errorf("Authors[0] = %q, want Smith J", a.Authors[0])
	}
}

func TestSearch(t *testing.T) {
	srv := newTestServer(t, map[string]string{
		"esearch.fcgi": esearchJSON,
	})
	defer srv.Close()

	c := clientFor(srv)
	sr, err := c.Search(context.Background(), "pubmed", "cancer", 5)
	if err != nil {
		t.Fatal(err)
	}
	if sr.DB != "pubmed" {
		t.Errorf("DB = %q, want pubmed", sr.DB)
	}
	if sr.Count != 42 {
		t.Errorf("Count = %d, want 42", sr.Count)
	}
	if len(sr.IDs) != 2 {
		t.Errorf("IDs len = %d, want 2", len(sr.IDs))
	}
	if sr.IDs[0] != "34145924" {
		t.Errorf("IDs[0] = %q, want 34145924", sr.IDs[0])
	}
}

func TestSearchPubMed(t *testing.T) {
	var reqCount int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqCount++
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "esearch") {
			_, _ = w.Write([]byte(`{"esearchresult":{"count":"1","retmax":"1","idlist":["34145924"]}}`))
		} else {
			_, _ = w.Write([]byte(pubmedSummaryJSON))
		}
	}))
	defer srv.Close()

	cfg := DefaultConfig()
	cfg.BaseURL = srv.URL
	cfg.Rate = 0
	cfg.Retries = 0
	c := NewClientWithConfig(cfg)

	arts, err := c.SearchPubMed(context.Background(), "Go programming", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(arts) != 1 {
		t.Fatalf("got %d articles, want 1", len(arts))
	}
	if arts[0].PMID != "34145924" {
		t.Errorf("PMID = %q, want 34145924", arts[0].PMID)
	}
	if reqCount != 2 {
		t.Errorf("expected 2 requests (esearch + esummary), got %d", reqCount)
	}
}

func TestFetchGenes(t *testing.T) {
	srv := newTestServer(t, map[string]string{
		"esummary.fcgi": geneSummaryJSON,
	})
	defer srv.Close()

	c := clientFor(srv)
	genes, err := c.FetchGenes(context.Background(), []string{"672"})
	if err != nil {
		t.Fatal(err)
	}
	if len(genes) != 1 {
		t.Fatalf("got %d genes, want 1", len(genes))
	}
	g := genes[0]
	if g.ID != "672" {
		t.Errorf("ID = %q, want 672", g.ID)
	}
	if g.Symbol != "BRCA1" {
		t.Errorf("Symbol = %q, want BRCA1", g.Symbol)
	}
	if g.Name != "BRCA1 DNA repair associated" {
		t.Errorf("Name = %q", g.Name)
	}
	if g.Organism != "Homo sapiens" {
		t.Errorf("Organism = %q, want Homo sapiens", g.Organism)
	}
	if g.Chromosome != "17" {
		t.Errorf("Chromosome = %q, want 17", g.Chromosome)
	}
}

func TestGetArticle(t *testing.T) {
	srv := newTestServer(t, map[string]string{
		"esummary.fcgi": pubmedSummaryJSON,
	})
	defer srv.Close()

	c := clientFor(srv)
	art, err := c.GetArticle(context.Background(), "34145924")
	if err != nil {
		t.Fatal(err)
	}
	if art.PMID != "34145924" {
		t.Errorf("PMID = %q, want 34145924", art.PMID)
	}
	if art.Volume != "12" {
		t.Errorf("Volume = %q, want 12", art.Volume)
	}
	if art.Pages != "100-110" {
		t.Errorf("Pages = %q, want 100-110", art.Pages)
	}
}

func TestBackoff(t *testing.T) {
	cases := []struct {
		attempt int
		want    time.Duration
	}{
		{1, 500 * time.Millisecond},
		{5, 2500 * time.Millisecond},
		{15, 5 * time.Second},
	}
	for _, tc := range cases {
		if got := backoff(tc.attempt); got != tc.want {
			t.Errorf("backoff(%d) = %v, want %v", tc.attempt, got, tc.want)
		}
	}
}

func TestParseUIDs(t *testing.T) {
	m := map[string]json.RawMessage{
		"uids": json.RawMessage(`["1","2","3"]`),
		"1":    json.RawMessage(`{}`),
	}
	uids, err := parseUIDs(m)
	if err != nil {
		t.Fatal(err)
	}
	if len(uids) != 3 || uids[0] != "1" {
		t.Errorf("parseUIDs = %v", uids)
	}
}
