package synonyms

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// Max number of characters when downloading synonyms before cutting it off
const SDQSynonymCap = 10000

// Name is a normalized name and its rank, 0 for the title and then PubChem's order
type Name struct {
	Key  string
	Rank int
}

var casPattern = regexp.MustCompile(`^(\d{2,7})-(\d{2})-(\d)$`)

// An HMDB metabolite ID as typed, like HMDB0001847, HMDB01847 or hmdb:01847.
// The prefix alone matches too, so "HMDB:" reads as an invalid HMDB ID 
var hmdbPattern = regexp.MustCompile(`(?i)^HMDB[-_:]?(\d*)$`)

// Database and registry IDs that are never names. The first match drops the synonym
var junkRules = []struct {
	name    string
	pattern *regexp.Regexp
}{
	{"ec_number", regexp.MustCompile(`(?i)^(EINECS|EC|ELINCS|NLP)?[ :]*\d{3}-\d{3}-\d$`)},
	{"unii", regexp.MustCompile(`^(UNII-)?[A-Z0-9]{10}$`)},
	{"solution_product", regexp.MustCompile(`(?i)\b(ug|µg|mg|ng|ig)/m?l\b|\bsolution in\b|\bin (acetonitrile|isooctane|methanol|hexane|cyclohexane|toluene|dichloromethane|acetone|nonane)\b`)},
	{"dsstox", regexp.MustCompile(`^DTX[SC]ID\d+$`)},
	{"chebi_chembl", regexp.MustCompile(`(?i)^(CHEBI|CHEMBL|SCHEMBL)[: ]?\d+$`)},
	{"registry_prefixed", regexp.MustCompile(`(?i)^(NSC|HSDB|CCRIS|BRN|AI3|RTECS|EPA PESTICIDE CHEMICAL CODE|CAS|FEMA( NO\.?)?|RefChem|Caswell No\.?|UN|DEA No\.?|KBio\w*|Prestwick\w*|SPECTRUM|SPBio\w*|BSPBio\w*|NCI\w*|Tox21|NCGC|BIDD|BDBM|HMDB|LMFA|KEGG|ZINC|AKOS|MFCD|STK|SY|CS|HY|AC|AS|BCP|BBL|DB|EN300|Q|SR|MLS|SMR|CBiol|Pharmakon\w*|IDI1|NINDS|DivK1c|SDCCGMLS|Lopac\w*|BRD|GTPL|s|AB|AM|AN|AKOS|FT|LS|MCULE|NS|BS|TRA|PS|SB|C|D|CCG|EBC|ALBB|DA|DS|BP|FS|VS|HS|J|GLXC|A1|TS|TYD|GEO|W|KS|WS|TCL|JS)[-_: .]*[A-Z]?\d[\d-_.]*[A-Z]?$`)},
	{"beilstein_ref", regexp.MustCompile(`(?i)beilstein handbook reference|^\d-\d{2}-\d{2}-\d{5}`)},
	{"inchi_like", regexp.MustCompile(`^(InChI=|[A-Z]{14}-[A-Z]{10}-[A-Z]$)`)},
	{"wln", regexp.MustCompile(`^WLN:`)},
	// Pieces of a name PubChem split at a mangled Greek letter, like "_,4-dihydroxy-" or
	// "A-phenyl-". Names ending in a hyphen with a space stay, they are derivatives written
	// inverted like "Hydroxyzine N-trimethylsilyl-"
	{"split_fragment", regexp.MustCompile(`^[_?]|^[^, ]*-$`)},
	// A mangled Greek letter left at the front of a split piece, like "A-D-glucopyranoside" split
	// off "n-Octyl β-D-glucopyranoside". Informal names like "a-ketoglutaric acid" stay
	{"mangled_greek", regexp.MustCompile(`^(A-[DL]-|A,)`)},
	// Patent and paper labels like "Example 1", "Compound V" or "CPD-183"
	{"patent_label", regexp.MustCompile(`(?i)^(example|compound|cpd)[\s-]*(\d+[a-z]?|[a-z])$`)},
	// One token with a run of 4+ digits, like MSK167300, oprea1_456186 or F2190-0369
	{"vendor_code", regexp.MustCompile(`^[A-Za-z0-9_-]*\d{4,}[A-Za-z0-9_-]*$`)},
}

// Drug development codes like WY-14643, BAY-K-8644, RO5-4864 or OPC-14597 are kept despite
// vendor_code. Registry prefixes of the same shape (SR-, CS-, NSC-) are dropped before this
var devCode = regexp.MustCompile(`^[A-Za-z]{1,5}\d?(-[A-Za-z]{1,3})?-\d{3,6}[A-Za-z]?$`)

// One-letter codes kept despite the two letter rule: E-numbers (e260), refrigerants (r 717,
// r1234yf) and lipid shorthand (c2:0). They also skip the junk rules, which vendor_code would
// otherwise apply to 4-digit ones like E1520
var shortCode = regexp.MustCompile(`^(e[ -]?\d{3,4}[a-z]?|r[ -]?\d{2,4}[a-z]{0,2}|c\d{1,2}:\d{1,2})$`)

// A trailing source tag like " [FHFI]" or " [INCI]" is stripped, not dropped
var sourceTag = regexp.MustCompile(`\s*\[[A-Z0-9 &/.,-]+\]$`)

var dashReplacer = strings.NewReplacer(
	"‐", "-", "‑", "-", "‒", "-", "–", "-", "—", "-",
	"―", "-", "−", "-", "﹣", "-", "－", "-",
)

var greekReplacer = strings.NewReplacer(
	"α", "alpha", "β", "beta", "γ", "gamma", "δ", "delta", "ε", "epsilon",
	"ζ", "zeta", "η", "eta", "θ", "theta", "ι", "iota", "κ", "kappa",
	"λ", "lambda", "μ", "mu", "ν", "nu", "ξ", "xi", "π", "pi", "ρ", "rho",
	"σ", "sigma", "τ", "tau", "υ", "upsilon", "φ", "phi", "χ", "chi",
	"ψ", "psi", "ω", "omega", "µ", "mu",
)

var dottedGreek = regexp.MustCompile(`\.(alpha|beta|gamma|delta|epsilon|omega|kappa|lambda|mu|pi|sigma|tau|xi|zeta|eta|theta|chi|psi|phi|rho|nu|iota|upsilon)\.`)

// Lipid classes written in shorthand like "PC 16:0_18:1", "PE(40:6)" or "Cer d18:1/24:0"
const lipidClasses = `pc|pe|pi|ps|pg|pa|lpc|lpe|lpi|lps|lpg|lpa|sm|cer|galcer|glccer|hexcer|laccer|tg|dg|mg|fa|ce|car|nae`

// The chain part of a lipid shorthand, like "16:0", "o-16:0" or "d18:1"
const lipidChains = `[dtmop]?-?\d+:\d+`

// "pe(40:6)" and "pe 40:6" are the same lipid, so the parentheses around the chains go.
// Inner ones stay, like the "(9z)" in "pc(16:0/18:1(9z))"
var lipidParens = regexp.MustCompile(`^(` + lipidClasses + `) ?\((` + lipidChains + `.*)\)$`)

var lipidShorthand = regexp.MustCompile(`^(` + lipidClasses + `) ` + lipidChains)

// Normalize is the lookup key: lowercase, collapsed whitespace, one dash character,
// Greek letters spelled out and lipid shorthand without the parentheses
func Normalize(s string) string {
	s = strings.ToLower(s)
	s = dashReplacer.Replace(s)
	s = greekReplacer.Replace(s)
	s = dottedGreek.ReplaceAllString(s, "$1")
	s = strings.Join(strings.Fields(s), " ")
	return lipidParens.ReplaceAllString(s, "$1 $2")
}

// Lab annotations that tell apart peaks of one compound, not part of its name.
// "dehydrated" and "branched" stay since they name a different compound
var annotation = regexp.MustCompile(`\s+(isomer [a-z]|minor)$`)

// A single trailing letter is an annotation only on lipid shorthand, "coenzyme a" keeps it
var lipidAnnotation = regexp.MustCompile(`\s+[a-z]$`)

// Lipid shorthand followed by its common name, like "fa 14:0 (myristic acid)"
var lipidWithName = regexp.MustCompile(`^(.+?) \(([^()]+)\)$`)

var acylcarnitine = regexp.MustCompile(`^acylcarnitine 0*(\d+:\d+)$`)

// CAS index names put the parent first, like "glycine, n-lauroyl-" or "phthalate, dioctyl-"
var inverted = regexp.MustCompile(`^([^,]+), ([^,]+)$`)

var aminoAcids = map[string]bool{
	"ala": true, "arg": true, "asn": true, "asp": true, "cys": true, "gln": true, "glu": true,
	"gly": true, "his": true, "ile": true, "leu": true, "lys": true, "met": true, "phe": true,
	"pro": true, "ser": true, "thr": true, "trp": true, "tyr": true, "val": true,
}

// Variants returns rewrites of a normalized key to try in order when the key itself
// misses. They turn common lab spellings into the ones PubChem uses, so they are guesses
// tried at query time and never stored
func Variants(key string) []string {
	var out []string
	seen := map[string]bool{key: true}
	add := func(s string) {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}

	base := annotation.ReplaceAllString(key, "")
	if lipidShorthand.MatchString(base) {
		base = lipidAnnotation.ReplaceAllString(base, "")
	}
	add(base)

	// "gly pro" is written "gly-pro" in PubChem. One-letter codes are left alone, "RET"
	// matches unrelated compounds
	if tokens := strings.Fields(base); len(tokens) >= 2 {
		allAminoAcids := true
		for _, t := range tokens {
			allAminoAcids = allAminoAcids && aminoAcids[t]
		}
		if allAminoAcids {
			add(strings.Join(tokens, "-"))
		}
	}

	if m := acylcarnitine.FindStringSubmatch(base); m != nil {
		add("car " + m[1])
	}

	// The common name is more specific than the shorthand, "fa 14:1 (physeteric acid)" is
	// one isomer of fa 14:1
	if m := lipidWithName.FindStringSubmatch(base); m != nil && lipidShorthand.MatchString(m[1]) {
		add(m[2])
		add(m[1])
	}

	if m := inverted.FindStringSubmatch(base); m != nil {
		parent, modifier := m[1], m[2]
		if parent == "acylcarnitine" {
			parent = "carnitine"
		}
		if prefix, ok := strings.CutSuffix(modifier, "-"); ok {
			// Substituents join the parent and esters stand apart from it
			add(prefix + parent)
			add(modifier + parent)
			add(prefix + " " + parent)
		} else {
			add(modifier + " " + parent)
		}
	}
	return out
}

// IsCASShaped reports whether a string looks like a CAS ID, whatever its check digit
func IsCASShaped(s string) bool {
	return casPattern.MatchString(s)
}

// ValidCAS reports whether s is CAS-shaped and its check digit matches
func ValidCAS(s string) bool {
	m := casPattern.FindStringSubmatch(s)
	if m == nil {
		return false
	}
	digits := m[1] + m[2]
	sum := 0
	for i := 0; i < len(digits); i++ {
		sum += int(digits[len(digits)-1-i]-'0') * (i + 1)
	}
	check, _ := strconv.Atoi(m[3])
	return sum%10 == check
}

// IsHMDBShaped reports whether a string looks like an HMDB ID, regardless of digit count
func IsHMDBShaped(s string) bool {
	return hmdbPattern.MatchString(s)
}

// NormalizeHMDB returns the 7-digit, like HMDB0001847 for HMDB01847
func NormalizeHMDB(s string) (string, bool) {
	m := hmdbPattern.FindStringSubmatch(s)
	if m == nil || len(m[1]) == 0 || len(m[1]) > 7 {
		return "", false
	}
	return "HMDB" + strings.Repeat("0", 7-len(m[1])) + m[1], true
}

// SplitHMDB returns the normalized, unique HMDB IDs in a dataset's "|" separated HMDB column
func SplitHMDB(field string) []string {
	var ids []string
	seen := map[string]bool{}
	for _, s := range strings.Split(field, "|") {
		if id, ok := NormalizeHMDB(strings.TrimSpace(s)); ok && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids
}

// Filter returns the names and valid CAS IDs to store for one compound.
// The title is kept unless it's a CAS ID or has no letter
func Filter(title, field string) ([]Name, []string) {
	var names []Name
	var cas []string
	seenName := map[string]bool{}
	seenCAS := map[string]bool{}
	synonymCount := 0

	addCAS := func(s string) {
		if ValidCAS(s) && !seenCAS[s] {
			seenCAS[s] = true
			cas = append(cas, s)
		}
	}

	keep := func(name string, isTitle bool) {
		key := Normalize(name)
		if key == "" || seenName[key] {
			return
		}
		// A name with no letter would be read as a CID, so it can never be queried
		if strings.IndexFunc(key, unicode.IsLetter) < 0 {
			return
		}
		// The title is what PubChem shows for the compound, so it skips the fragment rules
		if !isTitle {
			// Fragments like "a," or "(3h" are pieces of broken names, not names
			letters := 0
			for _, r := range key {
				if unicode.IsLetter(r) {
					letters++
				}
			}
			if letters < 2 && !shortCode.MatchString(key) {
				return
			}
			// A trailing comma means PubChem split the name at a mangled Greek letter
			if strings.HasSuffix(key, ",") {
				return
			}
			synonymCount++
		}
		seenName[key] = true
		rank := 0
		if !isTitle {
			rank = synonymCount
		}
		names = append(names, Name{Key: key, Rank: rank})
	}

	title = strings.TrimSpace(title)
	if IsCASShaped(title) {
		addCAS(title)
	} else if title != "" {
		keep(title, true)
	}

	var synonyms []string
	if field != "" {
		synonyms = strings.Split(field, "|")
	}
	// SDQ cuts the field at its cap, so the last entry there is cut mid-name
	if len(field) >= SDQSynonymCap && len(synonyms) > 0 {
		synonyms = synonyms[:len(synonyms)-1]
	}

	for _, s := range synonyms {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if IsCASShaped(s) {
			addCAS(s)
			continue
		}
		if isJunk(s) {
			continue
		}
		keep(sourceTag.ReplaceAllString(s, ""), false)
	}
	return names, cas
}

func isJunk(s string) bool {
	if shortCode.MatchString(Normalize(s)) {
		return false
	}
	for _, rule := range junkRules {
		// A UNII always has a digit, so all-letter words like DICHLORVOS stay
		if rule.name == "unii" && !strings.ContainsAny(s, "0123456789") {
			continue
		}
		if rule.name == "vendor_code" && devCode.MatchString(s) {
			continue
		}
		if rule.pattern.MatchString(s) {
			return true
		}
	}
	return false
}
