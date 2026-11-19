package api

import (
	"regexp"
	"strconv"
	"strings"

	"ctslite/synonyms"
)

var inchikeyPattern = regexp.MustCompile(`^[A-Z]{14}-[A-Z]{10}-[A-Z]$`)
var badInchikeyPattern = regexp.MustCompile(`^[a-zA-Z]{12,16}-[a-zA-Z]{9,11}-[a-zA-Z]{0,2}$`)

func isAllDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return len(s) > 0
}

// maxPubChemID is the highest CID possible with plenty of wiggle room (real max is 180M in 2026)
const maxPubChemID = 500_000_000

// isValidPubChemID reports whether an all-digit query can be a real CID
func isValidPubChemID(s string) bool {
	// reject leading zeros or 0 itself
	if s == "" || s[0] == '0' {
		return false
	}
	n, err := strconv.ParseUint(s, 10, 64)
	return err == nil && n <= maxPubChemID
}

// smilesPunctuation is the set of characters that appear in SMILES but never in a
// molecular formula, so any query containing one is unambiguously SMILES.
// Plus (+) and minus (-) are left out because formulas carry them as a charge suffix
const smilesPunctuation = "=#/\\:.@[]()"

// Element symbols are one uppercase letter plus an optional lowercase letter,
// so they pack into an index: the uppercase letter picks the row and the
// lowercase letter the column, with the last column meaning "single letter"
const (
	elementCols      = 27
	elementTableSize = 26 * elementCols
	noLowercase      = elementCols - 1
	hydrogenCode     = int('H'-'A')*elementCols + noLowercase
)

// maxDistinctElements bounds the repeat-detection scratch array. Overflowing
// it is harmless: a query with more distinct elements than this cannot be
// SMILES anyway, since only ten elements may appear unbracketed
const maxDistinctElements = 12

const periodicTable = "H He Li Be B C N O F Ne Na Mg Al Si P S Cl Ar K Ca Sc Ti V Cr Mn Fe Co Ni Cu Zn " +
	"Ga Ge As Se Br Kr Rb Sr Y Zr Nb Mo Tc Ru Rh Pd Ag Cd In Sn Sb Te I Xe Cs Ba La Ce Pr Nd Pm Sm Eu " +
	"Gd Tb Dy Ho Er Tm Yb Lu Hf Ta W Re Os Ir Pt Au Hg Tl Pb Bi Po At Rn Fr Ra Ac Th Pa U Np Pu Am Cm " +
	"Bk Cf Es Fm Md No Lr Rf Db Sg Bh Hs Mt Ds Rg Cn Nh Fl Mc Lv Ts Og"

// organicSubset are the elements SMILES writes without brackets.
// Every other element must appear as [Xx]
const organicSubset = "B C N O P S F Cl Br I"

// smilesAlphabet is every character a punctuation free SMILES can contain
const smilesAlphabet = "BCNOPSFIbclnoprs0123456789%-"

var isElementSymbol [elementTableSize]bool
var isOrganicSubset [elementTableSize]bool
var isSmilesChar [256]bool

func init() {
	for _, symbol := range strings.Fields(periodicTable) {
		isElementSymbol[packElementSymbol(symbol)] = true
	}
	for _, symbol := range strings.Fields(organicSubset) {
		isOrganicSubset[packElementSymbol(symbol)] = true
	}
	for i := 0; i < len(smilesAlphabet); i++ {
		isSmilesChar[smilesAlphabet[i]] = true
	}
}

// classifyUnreadable handles queries the formula grammar could not read. A
// string built only from SMILES characters is handed to SMILES so RDKit can
// make the final call, and anything else is not a chemical identifier at all
func classifyUnreadable(q string) string {
	if q == "" {
		return "unidentified"
	}
	for i := 0; i < len(q); i++ {
		if !isSmilesChar[q[i]] {
			return "unidentified"
		}
	}
	return "smiles"
}

func packElementSymbol(symbol string) int {
	column := noLowercase
	if len(symbol) == 2 {
		column = int(symbol[1] - 'a')
	}
	return int(symbol[0]-'A')*elementCols + column
}

// classifyElementString splits a query carrying no SMILES punctuation between
// formula and SMILES, which is the only case where both grammars can describe
// the same string. It reads the query as element symbols with optional counts
// and applies four rules in order:
//
//	a bare H is a syntax error in SMILES, so the query is a formula
//	an element repeating without hydrogen never happens in a formula
//	an element outside the organic subset would need brackets in SMILES
//	an unpaired digit is an unclosed ring bond, so it is a formula subscript
//
// Queries satisfying both grammars stay ambiguous. Anything the formula
// grammar cannot read goes to classifyUnreadable
func classifyElementString(q string) string {
	var seenCodes [maxDistinctElements]int16
	var digitCounts [10]int
	seenCount, tokenCount := 0, 0
	hasHydrogen, repeatsElement, allOrganic := false, false, true

	for i := 0; i < len(q); {
		if q[i] < 'A' || q[i] > 'Z' {
			return classifyUnreadable(q)
		}
		code := int(q[i]-'A') * elementCols
		i++
		if i < len(q) && q[i] >= 'a' && q[i] <= 'z' {
			code += int(q[i] - 'a')
			i++
		} else {
			code += noLowercase
		}
		if !isElementSymbol[code] {
			return classifyUnreadable(q)
		}
		for i < len(q) && q[i] >= '0' && q[i] <= '9' {
			digitCounts[q[i]-'0']++
			i++
		}

		tokenCount++
		if code == hydrogenCode {
			hasHydrogen = true
		}
		if !isOrganicSubset[code] {
			allOrganic = false
		}
		for s := 0; s < seenCount; s++ {
			if seenCodes[s] == int16(code) {
				repeatsElement = true
				break
			}
		}
		if seenCount < len(seenCodes) {
			seenCodes[seenCount] = int16(code)
			seenCount++
		}
	}
	if tokenCount == 0 {
		return classifyUnreadable(q)
	}

	// Ring closure labels always pair up, so an odd count means the digit is a
	// formula subscript rather than a ring bond
	unpairedDigit := false
	for _, n := range digitCounts {
		if n%2 != 0 {
			unpairedDigit = true
			break
		}
	}

	switch {
	case hasHydrogen:
		return "formula"
	case repeatsElement:
		return "smiles"
	case allOrganic && !unpairedDigit:
		return "smiles_or_formula"
	default:
		return "formula"
	}
}

// splitChargeSuffix splits a trailing sign and optional count such as -, +2 or
// -3 off a query, reporting false when the query does not end in one or the
// sign is all there is
func splitChargeSuffix(q string) (body string, sign byte, digits string, ok bool) {
	i := len(q)
	for i > 0 && q[i-1] >= '0' && q[i-1] <= '9' {
		i--
	}
	if i < 2 || (q[i-1] != '+' && q[i-1] != '-') {
		return "", 0, "", false
	}
	// no leading zeros or more than 2 digits
	if count := q[i:]; len(count) > 2 || (count != "" && count[0] == '0') {
		return "", 0, "", false
	}
	return q[:i-1], q[i-1], q[i:], true
}

// classifyChargeSuffix handles a query ending in a charge suffix such as
// C7H5O2-, Ca+2 or O4P-3, reporting false when the query has no suffix or the
// part before it is not formula shaped. A plus never appears outside brackets
// in SMILES and a bare trailing minus is a dangling bond, so both settle the
// query as a formula. A minus followed by digits can also be a bond into ring
// closures, as in C1CCCCC-1, so the query is reread without the minus and
// stays ambiguous when that reading still allows SMILES
func classifyChargeSuffix(q string) (string, bool) {
	body, sign, digits, ok := splitChargeSuffix(q)
	if !ok || strings.ContainsAny(body, "+-") {
		return "", false
	}
	if bodyType := classifyElementString(body); bodyType != "formula" && bodyType != "smiles_or_formula" {
		return "", false
	}
	if sign == '+' || digits == "" {
		return "formula", true
	}
	if reread := classifyElementString(body + digits); reread == "smiles" || reread == "smiles_or_formula" {
		return "smiles_or_formula", true
	}
	return "formula", true
}

func parseQueryType(q string) string {
	// Order of cases matters here
	switch {
	case inchikeyPattern.MatchString(q):
		return "inchikey"

	case badInchikeyPattern.MatchString(q):
		return "bad_inchikey"

	case strings.HasPrefix(q, "InChI="):
		return "inchi"

	case len(q) >= 6 && strings.EqualFold(q[:6], "inchi="):
		return "bad_inchi"

	case isAllDigits(q) && isValidPubChemID(q):
		return "pubchem_id"

	case isAllDigits(q):
		return "bad_pubchem_id"

	// A fast-fail for names not to be misclassified as SMILES (Like "PC 16:0_18:1")
	case strings.ContainsAny(q, " \t"):
		return "unidentified"

	case synonyms.IsCASShaped(q) && synonyms.ValidCAS(q):
		return "cas"

	case synonyms.IsCASShaped(q):
		return "bad_cas"

	case synonyms.IsHMDBShaped(q):
		if _, ok := synonyms.NormalizeHMDB(q); ok {
			return "hmdb"
		}
		return "bad_hmdb"

	case strings.ContainsAny(q, smilesPunctuation):
		return "smiles"

	default:
		if queryType, ok := classifyChargeSuffix(q); ok {
			return queryType
		}
		// Everything left is letters and digits, which both grammars can use
		return classifyElementString(q)
	}
}
