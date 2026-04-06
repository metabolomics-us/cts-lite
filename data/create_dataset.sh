# This script creates an updated dataset for CTS-Lite using the latest data from PubChemLite and PubChem

divider() {
  echo -e "\n---------------------------------------------\n"
}

set -eou pipefail

START_TIMER=$(date +%s)

# Download latest PubChemLite dataset via Zenodo API (resolves latest version automatically using concept_id)
# If the script fails, check that the concept_id is still correct by inspecting the "conceptrecid" field from https://zenodo.org/api/records/19346260
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
# Other MS
wget 'https://pubchem.ncbi.nlm.nih.gov/sdq/sphinxql.cgi?infmt=json&outfmt=csv&query={%22download%22:%20%22cid,inchikey,inchi,cmpdname,smiles,mf,exactmass,pclidcnt,gpidcnt%22,%22collection%22:%22compound%22,%22order%22:[%22relevancescore,desc%22],%22start%22:1,%22limit%22:10000000,%22downloadfilename%22:%22PubChem_compound_CID:_czTUKt9HuvuN0bjIOrDx7xTtU4339TJ6SF8pNlNOOzdTVwc%22,%22where%22:{%22ands%22:[{%22input%22:{%22type%22:%22netcachekey%22,%22idtype%22:%22cid%22,%22key%22:%22czTUKt9HuvuN0bjIOrDx7xTtU4339TJ6SF8pNlNOOzdTVwc%22}}]}}&showcolumndisplayname=1' -O "other-ms.csv"
divider
# GC-MS
wget 'https://pubchem.ncbi.nlm.nih.gov/sdq/sphinxql.cgi?infmt=json&outfmt=csv&query={%22download%22:%20%22cid,cmpdname,inchikey,inchi,smiles,mf,exactmass,gpidcnt,pclidcnt%22,%22collection%22:%22compound%22,%22order%22:[%22relevancescore,desc%22],%22start%22:1,%22limit%22:10000000,%22downloadfilename%22:%22PubChem_compound_CID:_ImWFR2XCAH43UIhJCjHBbiRsfQxizOjxktTzvYnF4byJ3N0%22,%22where%22:{%22ands%22:[{%22input%22:{%22type%22:%22netcachekey%22,%22idtype%22:%22cid%22,%22key%22:%22ImWFR2XCAH43UIhJCjHBbiRsfQxizOjxktTzvYnF4byJ3N0%22}}]}}&showcolumndisplayname=1' -O "gc-ms.csv"
divider
# LC-MS
wget 'https://pubchem.ncbi.nlm.nih.gov/sdq/sphinxql.cgi?infmt=json&outfmt=csv&query={%22download%22:%20%22cid,cmpdname,inchikey,inchi,smiles,mf,exactmass,gpidcnt,pclidcnt%22,%22collection%22:%22compound%22,%22order%22:[%22relevancescore,desc%22],%22start%22:1,%22limit%22:10000000,%22downloadfilename%22:%22PubChem_compound_CID:_4aZGhKYNw7H0n0uGyf4Coeejv8MJkItR8XSQHeplghzqfL4%22,%22where%22:{%22ands%22:[{%22input%22:{%22type%22:%22netcachekey%22,%22idtype%22:%22cid%22,%22key%22:%224aZGhKYNw7H0n0uGyf4Coeejv8MJkItR8XSQHeplghzqfL4%22}}]}}&showcolumndisplayname=1' -O lc-ms.csv
divider
# MS-MS
wget 'https://pubchem.ncbi.nlm.nih.gov/sdq/sphinxql.cgi?infmt=json&outfmt=csv&query={%22download%22:%20%22cid,cmpdname,inchikey,inchi,smiles,mf,exactmass,gpidcnt,pclidcnt%22,%22collection%22:%22compound%22,%22order%22:[%22relevancescore,desc%22],%22start%22:1,%22limit%22:10000000,%22downloadfilename%22:%22PubChem_compound_CID:_mN8__d9cuuCNzjLXsK978J7yxpLiBa0O1yu2Qsw6pEPMI5g%22,%22where%22:{%22ands%22:[{%22input%22:{%22type%22:%22netcachekey%22,%22idtype%22:%22cid%22,%22key%22:%22mN8__d9cuuCNzjLXsK978J7yxpLiBa0O1yu2Qsw6pEPMI5g%22}}]}}&showcolumndisplayname=1' -O ms-ms.csv
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
csvstack "$TRIMMED" reordered_pubchem.csv > total.csv
rm "$TRIMMED" reordered_pubchem.csv

# Remove duplicates
go run ./csv_magic/dedupe/dedupe.go total.csv
DATASET_NAME="cts-lite_$(date +%Y%m%d)".csv
mv deduped_total.csv "$DATASET_NAME"
rm total.csv
divider

echo "Dataset '$DATASET_NAME' created successfully!"
echo "Took  $(($(date +%s) - START_TIMER)) seconds"

