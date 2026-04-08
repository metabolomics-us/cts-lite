# This script creates an updated dataset for CTS-Lite using the latest data from PubChemLite and PubChem

divider() {
  echo -e "\n---------------------------------------------\n"
}

set -eou pipefail

START_TIMER=$(date +%s)

# Download latest PubChemLite dataset via Zenodo API (resolves latest version automatically using concept_id)
# If the pubchemlite download fails, check that the concept_id is still correct by inspecting the "conceptrecid" field from https://zenodo.org/api/records/19346260
CONCEPT_ID=4081056 # should remain constant
PUBCHEMLITE_URL=$(curl -s "https://zenodo.org/api/records?q=conceptrecid:${CONCEPT_ID}&sort=mostrecent&size=1" \
  | jq -r '.hits.hits[0].files[0].links.self')
PUBCHEMLITE_DATE=$(echo "${PUBCHEMLITE_URL}" | grep -oP '(?<=CCSbase_)[0-9]{8}')
echo "Downloading: ${PUBCHEMLITE_URL}"
wget "${PUBCHEMLITE_URL}" -O "pubchemlite_${PUBCHEMLITE_DATE}.csv"
divider

# Trim PubChemLite headers that we don't need
./csv_magic/pubchemlite_trimmer.sh "pubchemlite_${PUBCHEMLITE_DATE}.csv"
TRIMMED="pubchemlite_${PUBCHEMLITE_DATE}_trimmed.csv"
rm "pubchemlite_${PUBCHEMLITE_DATE}.csv"
divider

# Download PubChem MS Data
# Fetch ephemeral cache keys fresh at runtime via the PubChem classification API
# The classification_2.fcgi endpoint returns a CacheKey for a given hierarchy node (hnid).
# hid=72 is the PubChem Compound TOC "Mass Spectrometry" hierarchy
PUBCHEM_CACHE_URL="https://pubchem.ncbi.nlm.nih.gov/classification_2/classification_2.fcgi?hid=72&cache_uid_type=Compound&format=json"
pubchem_download() {
  local hnid="$1"
  local outfile="$2"
  local key
  key=$(curl -s "${PUBCHEM_CACHE_URL}&hnid=${hnid}" | jq -r '.Hierarchies.CacheKey')
  wget "https://pubchem.ncbi.nlm.nih.gov/sdq/sphinxql.cgi?infmt=json&outfmt=csv&query={%22download%22:%20%22cid,cmpdname,inchikey,inchi,smiles,mf,exactmass,gpidcnt,pclidcnt%22,%22collection%22:%22compound%22,%22order%22:[%22relevancescore,desc%22],%22start%22:1,%22limit%22:10000000,%22where%22:{%22ands%22:[{%22input%22:{%22type%22:%22netcachekey%22,%22idtype%22:%22cid%22,%22key%22:%22${key}%22}}]}}&showcolumndisplayname=1" -O "${outfile}"
}

# Names & Identifiers (hnid=1856948)
pubchem_download 1856948 "names-identifiers.csv"
divider
# Literature (hnid=1857367)
pubchem_download 1857367 "literature.csv"
divider
# Other MS (hnid=3857762)
pubchem_download 3857762 "other-ms.csv"
divider
# GC-MS (hnid=1856940)
pubchem_download 1856940 "gc-ms.csv"
divider
# LC-MS (hnid=3857761)
pubchem_download 3857761 "lc-ms.csv"
divider
# MS-MS (hnid=1857020)
pubchem_download 1857020 "ms-ms.csv"
divider

# Merge all pubchem csvs
csvstack ms-ms.csv lc-ms.csv gc-ms.csv other-ms.csv > pubchem.csv
rm ms-ms.csv lc-ms.csv gc-ms.csv other-ms.csv

# Adjust headers
go run ./csv_magic/firstblock/firstblock.go pubchem.csv
divider
./csv_magic/reorder.sh firstblocks_pubchem.csv reordered_pubchem.csv
divider
rm pubchem.csv firstblocks_pubchem.csv

# Merge PubChem and PubChemLite
csvstack reordered_pubchem.csv "$TRIMMED" > total.csv
rm "$TRIMMED" reordered_pubchem.csv

# Remove duplicates
go run ./csv_magic/dedupe/dedupe.go total.csv
DATASET_NAME="${1:-cts-lite_$(date +%Y%m%d).csv}"
mv deduped_total.csv "$DATASET_NAME"
rm total.csv
divider

echo "Dataset '$DATASET_NAME' created successfully!"
echo "Took $(($(date +%s) - START_TIMER)) seconds"

