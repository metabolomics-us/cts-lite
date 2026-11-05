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
		"PE(40:6)":               "pe 40:6",
		"PC(16:0/18:1(9Z))":      "pc 16:0/18:1(9z)",
		"Cer(d18:1/24:0)":        "cer d18:1/24:0",
		"(R)-limonene":           "(r)-limonene",
	} {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestVariants(t *testing.T) {
	for in, want := range map[string][]string{
		"arg pro phe":                     {"arg-pro-phe"},
		"acylcarnitine 08:0":              {"car 8:0"},
		"fa 14:1 (physeteric acid)":       {"physeteric acid", "fa 14:1"},
		"9,10-epoxystearic acid isomer a": {"9,10-epoxystearic acid"},
		"pc 16:0_20:5 a":                  {"pc 16:0_20:5"},
		"glycine, n-lauroyl-":             {"n-lauroylglycine", "n-lauroyl-glycine", "n-lauroyl glycine"},
		"acylcarnitine, isovaleryl-":      {"isovalerylcarnitine", "isovaleryl-carnitine", "isovaleryl carnitine"},
		"dimethylarginine, symmetric":     {"symmetric dimethylarginine"},
		// Not rewritten
		"coenzyme a":                   nil,
		"acylcarnitine 04:0, branched": {"branched acylcarnitine 04:0"},
		"ret":                          nil,
	} {
		if diff := cmp.Diff(want, Variants(in)); diff != "" {
			t.Errorf("Variants(%q) mismatch (-want +got):\n%s", in, diff)
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

func TestNormalizeHMDB(t *testing.T) {
	for in, want := range map[string]string{
		"HMDB0001847":   "HMDB0001847",
		"HMDB01847":     "HMDB0001847",
		"hmdb:01847":    "HMDB0001847",
		"HMDB 01847":    "",
		"HMDB: 0011628": "",
		"HMDB004253":    "HMDB0004253",
		"HMDB123":       "HMDB0000123",
		"HMDB00018470":  "",
		"HMDBP00001":    "",
		"HMDB:":         "",
		"aspirin":       "",
	} {
		if got, _ := NormalizeHMDB(in); got != want {
			t.Errorf("NormalizeHMDB(%q) = %q, want %q", in, got, want)
		}
	}
	if diff := cmp.Diff([]string{"HMDB0002111", "HMDB0062587"}, SplitHMDB("HMDB0002111|HMDB02111|HMDB0062587|junk")); diff != "" {
		t.Errorf("SplitHMDB mismatch (-want +got):\n%s", diff)
	}
	if got := SplitHMDB(""); got != nil {
		t.Errorf("SplitHMDB(\"\") = %v, want nil", got)
	}
}

func TestIsJunk(t *testing.T) {
	for in, want := range map[string]bool{
		// One per junk rule
		"EINECS 200-064-1":              true, // ec_number
		"R16CO5Y76E":                    true, // unii
		"Aspirin 100 ug/ml in methanol": true, // solution_product
		"DTXCID90123":                   true, // dsstox
		"CHEMBL25":                      true, // chebi_chembl
		"NSC 27223":                     true, // registry_prefixed
		"4-10-00-00138 (Beilstein Handbook Reference)": true, // beilstein_ref
		"BSYNRYMUTXBXSQ-UHFFFAOYSA-N":                  true, // inchi_like
		"WLN: QVR BOV1":                                true, // wln
		"_,4-dihydroxy-":                               true, // split_fragment
		"A-phenyl-":                                    true, // split_fragment
		"A-D-glucopyranoside":                          true, // mangled_greek
		"Example 12":                                   true, // patent_label
		"F2190-0369":                                   true, // vendor_code
		// Exceptions that must stay
		"aspirin":                       false,
		"DICHLORVOS":                    false, // all letters, not a UNII
		"Hydroxyzine N-trimethylsilyl-": false, // inverted derivative, not a fragment
		"a-ketoglutaric acid":           false, // informal Greek, not mangled
		"WY-14643":                      false, // devCode
		"E1520":                         false, // shortCode
	} {
		if got := isJunk(in); got != want {
			t.Errorf("isJunk(%q) = %v, want %v", in, got, want)
		}
	}

	// A trailing comma is a split fragment, dropped in keep rather than isJunk
	if names, _ := Filter("", "4-dihydroxy,"); len(names) != 0 {
		t.Errorf("trailing comma fragment kept: %v", names)
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

	// Every synonym is kept, ranked in PubChem's order
	field := "n1|n2|n3|n4|n5|n6|n7|n8|n9|n10|n11|n12|64-17-5"
	names, cas = Filter("title", strings.ReplaceAll(field, "n", "name"))
	if len(names) != 13 || names[12] != (Name{"name12", 12}) || len(cas) != 1 {
		t.Errorf("all synonyms: got %d names and %d CAS numbers", len(names), len(cas))
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
