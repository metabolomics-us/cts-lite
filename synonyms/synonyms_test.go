package synonyms

import (
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestNormalize(t *testing.T) {
	for in, want := range map[string]string{
		"  Acetic   Acid ":       "acetic acid",
		"α-Pinene":               "alpha-pinene",
		".alpha.-Pinene":         "alpha-pinene",
		"2‐chloro–ethanol":       "2-chloro-ethanol",
		"µ-conotoxin":            "mu-conotoxin",
		"Beta-D-glucopyranoside": "beta-d-glucopyranoside",
	} {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestValidCAS(t *testing.T) {
	for in, want := range map[string]bool{
		"141-14-0":  true,
		"50-78-2":   true,
		"141-40-0":  false,
		"7732-18-5": true,
		"aspirin":   false,
	} {
		if got := ValidCAS(in); got != want {
			t.Errorf("ValidCAS(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestFilter(t *testing.T) {
	names, cas := Filter("Aspirin", "aspirin|ACETYLSALICYLIC ACID|50-78-2|UNII-R16CO5Y76E|CHEBI:15365|DTXSID5020108|WY-14643|E 330|Acetylsalicylic acid [INCI]|MFCD00002430|a,|141-40-0")
	want := []Name{{"aspirin", 0}, {"acetylsalicylic acid", 1}, {"wy-14643", 2}, {"e 330", 3}}
	if diff := cmp.Diff(want, names); diff != "" {
		t.Errorf("names mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{"50-78-2"}, cas); diff != "" {
		t.Errorf("cas mismatch (-want +got):\n%s", diff)
	}

	// A title is kept even when a synonym rule would drop it, and a CAS title goes to cas
	if names, _ := Filter("U-48800", ""); len(names) != 1 || names[0] != (Name{"u-48800", 0}) {
		t.Errorf("code title not kept: %v", names)
	}
	if names, cas := Filter("7732-18-5", "water"); len(cas) != 1 || names[0] != (Name{"water", 1}) {
		t.Errorf("CAS title: names %v, cas %v", names, cas)
	}

	// The title plus MaxSynonyms, with CAS numbers past the cap still collected
	field := "n1|n2|n3|n4|n5|n6|n7|n8|n9|n10|n11|n12|64-17-5"
	names, cas = Filter("title", strings.ReplaceAll(field, "n", "name"))
	if len(names) != MaxSynonyms+1 || len(cas) != 1 {
		t.Errorf("cap: got %d names and %d CAS numbers", len(names), len(cas))
	}

	// 4-digit E-numbers and refrigerants are short codes, not vendor codes
	names, _ = Filter("", "E1520|R1234yf|R-1150")
	if diff := cmp.Diff([]Name{{"e1520", 1}, {"r1234yf", 2}, {"r-1150", 3}}, names); diff != "" {
		t.Errorf("short codes mismatch (-want +got):\n%s", diff)
	}

	// At the SDQ cap the last entry was cut mid-name and is dropped
	cut := "ethanol|" + strings.Repeat("x", SDQSynonymCap-len("ethanol|")-len("|cut name")) + "|cut name"
	names, _ = Filter("", cut)
	if names[len(names)-1].Key == "cut name" {
		t.Errorf("entry cut at the SDQ cap was kept")
	}
}
