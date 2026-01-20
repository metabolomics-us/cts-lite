## CSV-Magic
This directory is used to manipulate the dataset csv files into the format we want them

### Expected Header
CTS-Lite expects the following header for its dataset:  
- `Identifier,FirstBlock,PubMed_Count,Patent_Count,MolecularFormula,SMILES,InChI,InChIKey,MonoisotopicMass,CompoundName`

### Workflow
Here is a step-by-step guide to creating the dataset for CTS-Lite

1. Download the most recent PubChemLite csv from [zenodo](https://zenodo.org/records/18169629)
2. Trim the csv using `pubchemlite_trimmer.sh`. This will remove all unnecessary columns
3. Download additional data from PubChem [here](https://pubchem.ncbi.nlm.nih.gov/classification/#hid=72)
4. Select the queries you are interested in adding to the dataset (CTS-Lite uses `Spectral Information -> Mass Spectrometry`)
5. Query the categories and click download on the right-hand side
    - Downloads are limited to queries of ~600,000 compounds. You will have to query the subcategories individually and merge their csvs manually to download the larger categories. If they don't have subcategories, you might be out of luck, I couldn't find a reliable way of downloading larger categories.
6. Select the following 9 fields for your download: Name, InChIKey, InChI, SMILES, Molecular Formula, Exact Mass, Linked PubChem Literature Count, Linked PubChem Patent Count
7. Download each as a csv and combine them using csvkit's `csvstack` command like so:
    - `csvstack gc-ms.csv lc-ms.csv > gc-lc-ms.csv`
    - `csvstack gc-lc-ms.csv other-ms.csv > all-ms.csv`
8. Before merging `all-ms.csv` with the PubChemLite csv, you will have to rename and reorder the columns of the header
    - First, use the `./firstblock` module to generate the First Block column for a csv: `go run ./firstblock/firstblock.go all-ms.csv`
    - Second, use `./reorder.sh` to reorder and rename the columns of the csv to be aligned with PubChemLite: `./reorder.sh firstblocks_all-ms.csv reordered-ms.csv`
9. Merge the reordered csv with the PubChemLite csv: `csvstack PubChemLite_trimmed.csv reordered-ms.csv > total.csv`
    - Note: the order matters, put the PubChemLite csv first to prioritize keeping its compounds when removing duplicates in the next step
9. Once merged, use the `./dedupe` module to remove any duplicate compounds from the dataset: `go run ./dedupe/dedupe.go total.csv`
