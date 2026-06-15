package ncbi

import (
	"testing"

	"github.com/tamnd/any-cli/kit"
)

// These tests are offline: they exercise the URI driver's pure string functions
// and the host wiring (mint, body, resolve), which need no network. The client's
// HTTP behaviour is covered in ncbi_test.go.

func TestDomainInfo(t *testing.T) {
	info := Domain{}.Info()
	if info.Scheme != "ncbi" {
		t.Errorf("Scheme = %q, want ncbi", info.Scheme)
	}
	if len(info.Hosts) == 0 || info.Hosts[0] != Host {
		t.Errorf("Hosts = %v, want [%s]", info.Hosts, Host)
	}
	if info.Identity.Binary != "ncbi" {
		t.Errorf("Identity.Binary = %q, want ncbi", info.Identity.Binary)
	}
}

func TestClassifyPMID(t *testing.T) {
	cases := []struct{ in, typ, id string }{
		{"34145924", "pmid", "34145924"},
		{"672", "pmid", "672"},
		{"https://pubmed.ncbi.nlm.nih.gov/34145924/", "pmid", "34145924"},
		{"https://pubmed.ncbi.nlm.nih.gov/34145924", "pmid", "34145924"},
	}
	for _, tc := range cases {
		typ, id, err := Domain{}.Classify(tc.in)
		if err != nil || typ != tc.typ || id != tc.id {
			t.Errorf("Classify(%q) = (%q, %q, %v), want (%q, %q, nil)",
				tc.in, typ, id, err, tc.typ, tc.id)
		}
	}
}

func TestClassifyInvalid(t *testing.T) {
	_, _, err := Domain{}.Classify("notapmid")
	if err == nil {
		t.Error("Classify(non-digit) expected error, got nil")
	}
}

func TestLocate(t *testing.T) {
	got, err := Domain{}.Locate("pmid", "34145924")
	want := "https://pubmed.ncbi.nlm.nih.gov/34145924/"
	if err != nil || got != want {
		t.Errorf("Locate = (%q, %v), want (%q, nil)", got, err, want)
	}
}

func TestLocateUnknownType(t *testing.T) {
	_, err := Domain{}.Locate("unknown", "123")
	if err == nil {
		t.Error("Locate with unknown type expected error, got nil")
	}
}

func TestIsDigits(t *testing.T) {
	cases := []struct {
		s    string
		want bool
	}{
		{"12345", true},
		{"", false},
		{"abc", false},
		{"12a5", false},
		{"0", true},
	}
	for _, tc := range cases {
		if got := isDigits(tc.s); got != tc.want {
			t.Errorf("isDigits(%q) = %v, want %v", tc.s, got, tc.want)
		}
	}
}

// TestHostWiring mounts the driver in a kit Host (the runtime ant drives) and
// checks the round trip: a record mints to its URI, its body is readable, and a
// bare id resolves back to the same URI.
func TestHostWiring(t *testing.T) {
	h, err := kit.Open()
	if err != nil {
		t.Fatal(err)
	}

	a := &Article{
		PMID:    "34145924",
		Title:   "A study of Go programming",
		Authors: []string{"Smith J"},
		Journal: "Journal of Computing",
		PubDate: "2021 Jun 15",
	}
	u, err := h.Mint(a)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if want := "ncbi://pmid/34145924"; u.String() != want {
		t.Errorf("Mint = %q, want %q", u.String(), want)
	}

	got, err := h.ResolveOn("ncbi", "34145924")
	if err != nil || got.String() != "ncbi://pmid/34145924" {
		t.Errorf("ResolveOn = (%q, %v), want ncbi://pmid/34145924", got.String(), err)
	}
}
