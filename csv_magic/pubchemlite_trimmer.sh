#!/bin/bash
set -e

# This script trims a given PubChemLite CSV file to only include the columns used by CTS-Lite

if [ "$#" -ne 1 ] && [ "$#" -ne 2 ]; then
    echo "Usage: $0 <pubchemlite_csv> [output_csv]"
    exit 1
fi

input_file="$1"
output_file="${2:-$(basename "$input_file" .csv)_trimmed.csv}"

# Ensure input file exists
if [ ! -f "$input_file" ]; then
  echo "Error: File '$input_file' not found."
  exit 1
fi

# Ensure csvcut is available
if ! command -v csvcut >/dev/null 2>&1; then
    echo "Error: csvcut is not installed. Install it with 'sudo apt install csvkit'"
    exit 1
fi

echo "Trimming PubChemLite CSV file: $input_file"

csvcut -c Identifier,FirstBlock,PubMed_Count,Patent_Count,MolecularFormula,SMILES,InChI,InChIKey,MonoisotopicMass,CompoundName \
  "$input_file" > "$output_file"

echo "The trimmed dataset has been saved as: $output_file"
