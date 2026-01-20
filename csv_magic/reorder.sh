#!/usr/bin/env bash
#  1
#  2  reorder.sh
#  3  Reorder CSV columns with csvcut and rename header
#  4
#  5  Usage:
#  6      ./reorder.sh input.csv output.csv
#  7

set -euo pipefail

#  8
#  9  Check args
# 10
if [[ $# -ne 2 ]]; then
    echo "Usage: $0 input.csv output.csv" >&2
    exit 1
fi

# 11
# 12 Inputs
# 13
IN="$1"
OUT="$2"

# 14
# 15 Reorder columns by name
# 16
csvcut -c \
Compound_CID,FirstBlock,Linked_PubChem_Literature_Count,Linked_PubChem_Patent_Count,Molecular_Formula,SMILES,InChI,InChIKey,Exact_Mass,Name \
"$IN" | \
# 17
# 18 Replace header
# 19
awk 'BEGIN{OFS=","}
NR==1 {
  print "Identifier,FirstBlock,PubMed_Count,Patent_Count,MolecularFormula,SMILES,InChI,InChIKey,MonoisotopicMass,CompoundName"
  next
}
{ print }' > "$OUT"

# 20
# 21 Done
# 22
echo "Wrote reordered file to: $OUT"

