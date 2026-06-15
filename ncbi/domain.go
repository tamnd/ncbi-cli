package ncbi

import (
	"context"
	"strings"
	"unicode"

	"github.com/tamnd/any-cli/kit"
	"github.com/tamnd/any-cli/kit/errs"
)

// domain.go exposes NCBI as a kit Domain: a driver that a multi-domain
// host (ant) enables with a single blank import,
//
//	import _ "github.com/tamnd/ncbi-cli/ncbi"
//
// exactly as a database/sql program enables a driver with `import _
// "github.com/lib/pq"`. The init below registers it; the host then dereferences
// ncbi:// URIs by routing to the operations Register installs. The same
// Domain also builds the standalone ncbi binary (see cli.NewApp), so the
// binary and a host share one source of truth.
func init() { kit.Register(Domain{}) }

// Domain is the NCBI driver. It carries no state; the per-run client is
// built by the factory Register hands kit.
type Domain struct{}

// Info describes the scheme, the hostnames a pasted link is matched against, and
// the identity reused for the binary's help and version.
func (Domain) Info() kit.DomainInfo {
	return kit.DomainInfo{
		Scheme: "ncbi",
		Hosts:  []string{Host},
		Identity: kit.Identity{
			Binary: "ncbi",
			Short:  "Read public NCBI data: PubMed articles, genes, and more.",
			Long: `Read public NCBI data: PubMed articles, genes, and more.

ncbi reads from the NCBI Entrez eUtils API, shapes it into clean records,
and prints output that pipes into the rest of your tools. No API key required
for up to 3 req/s; set NCBI_API_KEY for higher limits.`,
			Site: Host,
			Repo: "https://github.com/tamnd/ncbi-cli",
		},
	}
}

// Register installs the client factory and every operation onto app.
func (Domain) Register(app *kit.App) {
	app.SetClient(newClient)

	// pubmed: search PubMed and return full Article records.
	kit.Handle(app, kit.OpMeta{Name: "pubmed", Group: "read", List: true,
		Summary: "Search PubMed and return article records",
		Args:    []kit.Arg{{Name: "query", Help: "search terms", Variadic: true}}}, searchPubMed)

	// gene: search NCBI Gene DB and return full Gene records.
	kit.Handle(app, kit.OpMeta{Name: "gene", Group: "read", List: true,
		Summary: "Search the Gene database and return gene records",
		Args:    []kit.Arg{{Name: "query", Help: "search terms", Variadic: true}}}, searchGene)

	// search: generic search across any NCBI database; returns IDs and count.
	kit.Handle(app, kit.OpMeta{Name: "search", Group: "read", Single: true,
		Summary: "Generic search across an NCBI database",
		Args:    []kit.Arg{{Name: "query", Help: "search terms", Variadic: true}}}, genericSearch)

	// article: fetch a single PubMed article by PMID.
	kit.Handle(app, kit.OpMeta{Name: "article", Group: "read", Single: true,
		Summary: "Fetch a PubMed article by PMID", URIType: "pmid", Resolver: true,
		Args: []kit.Arg{{Name: "pmid", Help: "PubMed ID"}}}, getArticle)
}

// newClient builds the NCBI client from the host-resolved config.
func newClient(_ context.Context, cfg kit.Config) (any, error) {
	ncfg := DefaultConfig()
	if cfg.UserAgent != "" {
		ncfg.UserAgent = cfg.UserAgent
	}
	if cfg.Rate > 0 {
		ncfg.Rate = cfg.Rate
	}
	if cfg.Retries > 0 {
		ncfg.Retries = cfg.Retries
	}
	if cfg.Timeout > 0 {
		ncfg.Timeout = cfg.Timeout
	}
	return NewClientWithConfig(ncfg), nil
}

// --- inputs ---

type pubmedQuery struct {
	Query  []string `kit:"arg,variadic" help:"search terms"`
	Limit  int      `kit:"flag,inherit" help:"max results"`
	Client *Client  `kit:"inject"`
}

type geneQuery struct {
	Query  []string `kit:"arg,variadic" help:"search terms"`
	Limit  int      `kit:"flag,inherit" help:"max results"`
	Client *Client  `kit:"inject"`
}

type searchQuery struct {
	Query  []string `kit:"arg,variadic" help:"search terms"`
	DB     string   `kit:"flag" help:"NCBI database" default:"pubmed"`
	Limit  int      `kit:"flag,inherit" help:"max results"`
	Client *Client  `kit:"inject"`
}

type articleRef struct {
	PMID   string  `kit:"arg" help:"PubMed ID"`
	Client *Client `kit:"inject"`
}

// --- handlers ---

func searchPubMed(ctx context.Context, in pubmedQuery, emit func(*Article) error) error {
	limit := in.Limit
	if limit <= 0 {
		limit = 10
	}
	arts, err := in.Client.SearchPubMed(ctx, strings.Join(in.Query, " "), limit)
	if err != nil {
		return mapErr(err)
	}
	for _, a := range arts {
		if err := emit(a); err != nil {
			return err
		}
	}
	return nil
}

func searchGene(ctx context.Context, in geneQuery, emit func(*Gene) error) error {
	limit := in.Limit
	if limit <= 0 {
		limit = 10
	}
	genes, err := in.Client.SearchGenes(ctx, strings.Join(in.Query, " "), limit)
	if err != nil {
		return mapErr(err)
	}
	for _, g := range genes {
		if err := emit(g); err != nil {
			return err
		}
	}
	return nil
}

func genericSearch(ctx context.Context, in searchQuery, emit func(*SearchResult) error) error {
	db := in.DB
	if db == "" {
		db = "pubmed"
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 10
	}
	sr, err := in.Client.Search(ctx, db, strings.Join(in.Query, " "), limit)
	if err != nil {
		return mapErr(err)
	}
	return emit(sr)
}

func getArticle(ctx context.Context, in articleRef, emit func(*Article) error) error {
	art, err := in.Client.GetArticle(ctx, in.PMID)
	if err != nil {
		return mapErr(err)
	}
	return emit(art)
}

// --- Resolver: the URI-native string functions, pure and network-free ---

// Classify turns any accepted input into the canonical (type, id).
// A bare string of digits is a PMID; a full pubmed URL is also recognised.
func (Domain) Classify(input string) (uriType, id string, err error) {
	input = strings.TrimSpace(input)
	// Full URL: https://pubmed.ncbi.nlm.nih.gov/<pmid>/
	if strings.Contains(input, "pubmed.ncbi.nlm.nih.gov") {
		parts := strings.Split(strings.Trim(input, "/"), "/")
		for i := len(parts) - 1; i >= 0; i-- {
			if isDigits(parts[i]) {
				return "pmid", parts[i], nil
			}
		}
	}
	// Bare PMID (all digits)
	if isDigits(input) {
		return "pmid", input, nil
	}
	return "", "", errs.Usage("unrecognized NCBI reference: %q (expected a PMID or pubmed URL)", input)
}

// Locate is the inverse: the live https URL for a (type, id).
func (Domain) Locate(uriType, id string) (string, error) {
	switch uriType {
	case "pmid":
		return "https://pubmed.ncbi.nlm.nih.gov/" + id + "/", nil
	default:
		return "", errs.Usage("ncbi has no resource type %q", uriType)
	}
}

// --- helpers ---

// isDigits reports whether s is a non-empty string of ASCII digits.
func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

// mapErr converts a library error into the kit error kind that carries the right
// exit code.
func mapErr(err error) error {
	return err
}
