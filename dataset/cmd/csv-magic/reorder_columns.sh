#!/usr/bin/env bash
# Reorders and renames CSV header columns

set -euo pipefail

# Check args
if [[ $# -ne 2 ]]; then
    echo "Usage: $0 <input.csv> <output.csv>" >&2
    exit 1
fi

IN="$1"
OUT="$2"

# Reorder columns
csvcut -c \
Compound_CID,Linked_PubChem_Literature_Count,Linked_PubChem_Patent_Count,Molecular_Formula,SMILES,InChI,InChIKey,Exact_Mass,Name \
"$IN" | \

# Rename columns
awk 'BEGIN{OFS=","}
NR==1 {
  print "Identifier,Literature_Count,Patent_Count,MolecularFormula,SMILES,InChI,InChIKey,ExactMass,CompoundName"
  next
}
{ print }' > "$OUT"

echo "Wrote reordered file to: $OUT"

