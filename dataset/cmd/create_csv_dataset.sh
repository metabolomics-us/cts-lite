#!/bin/bash
# This script creates an updated dataset for CTS-Lite using the latest data from PubChem

set -euo pipefail

divider() {
  echo -e "\n---------------------------------------------\n"
}

# Ensure all required tools are available
for cmd in csvstack jq curl go wget; do
  command -v "$cmd" >/dev/null || { echo "Required tool not found: $cmd" >&2; exit 1; }
done

# Map of PubChem categories to their corresponding ids
declare -A pubchem_categories=(                                                                                                                 
  ["names-and-identifiers"]=1856948
  ["literature"]=1857367                                                                                                                        
  ["other-ms"]=3857762                              
  ["gc-ms"]=1856940                                                                                                                             
  ["lc-ms"]=3857761                                 
  ["ms-ms"]=1857020
  ["agrochemical"]=1857282
  ["pathways"]=3647702
  ["drug-and-medic-info"]=1857071
  ["food-additives"]=1857308
  ["pharma-biochem"]=3647584
  ["safety-hazards"]=3647601
  ["toxicity"]=3647656
  ["manufacturing"]=3647592
  ["disorders"]=1857178
  ["identification"]=1857246
  ["chemical-classes"]=1857014
)

printf "Downloading the following categories from PubChem:\n"
for category in "${!pubchem_categories[@]}"; do
  printf " - %s\n" "${category}"
done
divider

START_TIMER=$(date +%s)

# Fetch ephemeral cache keys fresh at runtime via the PubChem classification API
# The classification_2.fcgi endpoint returns a CacheKey for a given hierarchy node (hnid)
PUBCHEM_CACHE_URL="https://pubchem.ncbi.nlm.nih.gov/classification_2/classification_2.fcgi?hid=72&cache_uid_type=Compound&format=json"
pubchem_download() {
  local hnid="$1"
  local outfile="$2"
  local key
  key=$(curl -s "${PUBCHEM_CACHE_URL}&hnid=${hnid}" | jq -r '.Hierarchies.CacheKey')
  if [[ -z "${key}" || "${key}" == "null" ]]; then
    echo "Failed to fetch cache key for ${hnid}"
    exit 1
  fi
  wget "https://pubchem.ncbi.nlm.nih.gov/sdq/sphinxql.cgi?infmt=json&outfmt=csv&query={%22download%22:%20%22cid,cmpdname,inchikey,inchi,smiles,mf,exactmass,gpidcnt,pclidcnt%22,%22collection%22:%22compound%22,%22order%22:[%22relevancescore,desc%22],%22start%22:1,%22limit%22:10000000,%22where%22:{%22ands%22:[{%22input%22:{%22type%22:%22netcachekey%22,%22idtype%22:%22cid%22,%22key%22:%22${key}%22}}]}}&showcolumndisplayname=1" -O "${outfile}"
}

for category in "${!pubchem_categories[@]}"; do
  pubchem_download "${pubchem_categories[$category]}" "${category}.csv"
  divider
done

# Merge all pubchem csvs
echo "Merging all csvs..."
keys=("${!pubchem_categories[@]}")                  
csvstack "${keys[@]/%/.csv}" > pubchem.csv
rm "${keys[@]/%/.csv}"
divider

# Adjust headers
echo "Adjusting headers..."
go run ./csv-magic/firstblock/firstblock.go pubchem.csv
divider
./csv-magic/reorder_columns.sh firstblocks_pubchem.csv reordered_pubchem.csv
divider
rm pubchem.csv firstblocks_pubchem.csv

# Remove duplicates
echo "Removing duplicates..."
go run ./csv-magic/dedupe/dedupe.go reordered_pubchem.csv
DATASET_NAME="${1:-cts-lite_$(date +%Y%m%d).csv}"
mv deduped_reordered_pubchem.csv "$DATASET_NAME"
rm reordered_pubchem.csv
divider

echo "Dataset '$DATASET_NAME' created successfully!"
echo "Took $(($(date +%s) - START_TIMER)) seconds"

