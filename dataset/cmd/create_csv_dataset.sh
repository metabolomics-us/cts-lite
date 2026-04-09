# This script creates an updated dataset for CTS-Lite using the latest data from PubChem

divider() {
  echo -e "\n---------------------------------------------\n"
}

set -eou pipefail

# Map of PubChem categories to their corresponding ids
declare -A pubchem_categories=(                                                                                                                 
  ["names-and-identifiers"]=1856948
  ["literature"]=1857367                                                                                                                        
  ["other-ms"]=3857762                              
  ["gc-ms"]=1856940                                                                                                                             
  ["lc-ms"]=3857761                                 
  ["ms-ms"]=1857020
)

printf "Downloading the following categories from PubChem:\n"
for category in "${!pubchem_categories[@]}"; do
  printf " - %s\n" "${category}"
done

START_TIMER=$(date +%s)

# Fetch ephemeral cache keys fresh at runtime via the PubChem classification API
# The classification_2.fcgi endpoint returns a CacheKey for a given hierarchy node (hnid)
PUBCHEM_CACHE_URL="https://pubchem.ncbi.nlm.nih.gov/classification_2/classification_2.fcgi?hid=72&cache_uid_type=Compound&format=json"
pubchem_download() {
  local hnid="$1"
  local outfile="$2"
  local key
  key=$(curl -s "${PUBCHEM_CACHE_URL}&hnid=${hnid}" | jq -r '.Hierarchies.CacheKey')
  wget "https://pubchem.ncbi.nlm.nih.gov/sdq/sphinxql.cgi?infmt=json&outfmt=csv&query={%22download%22:%20%22cid,cmpdname,inchikey,inchi,smiles,mf,exactmass,gpidcnt,pclidcnt%22,%22collection%22:%22compound%22,%22order%22:[%22relevancescore,desc%22],%22start%22:1,%22limit%22:10000000,%22where%22:{%22ands%22:[{%22input%22:{%22type%22:%22netcachekey%22,%22idtype%22:%22cid%22,%22key%22:%22${key}%22}}]}}&showcolumndisplayname=1" -O "${outfile}"
}

for category in "${!pubchem_categories[@]}"; do
  pubchem_download "${pubchem_categories[$category]}" "${category}.csv"
  divider
done

# Merge all pubchem csvs
csvstack "${!pubchem_categories[@]/%/.csv}" > pubchem.csv                                                                                            
rm "${!pubchem_categories[@]/%/.csv}"

# Adjust headers
go run ./csv-magic/firstblock/firstblock.go pubchem.csv
divider
./csv-magic/reorder_columns.sh firstblocks_pubchem.csv reordered_pubchem.csv
divider
rm pubchem.csv firstblocks_pubchem.csv

# Remove duplicates
go run ./csv-magic/dedupe/dedupe.go reordered_pubchem.csv
DATASET_NAME="${1:-cts-lite_$(date +%Y%m%d).csv}"
mv deduped_reordered_pubchem.csv "$DATASET_NAME"
rm reordered_pubchem.csv
divider

echo "Dataset '$DATASET_NAME' created successfully!"
echo "Took $(($(date +%s) - START_TIMER)) seconds"

