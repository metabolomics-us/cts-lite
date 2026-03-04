## CTS-Lite Data
This directory holds all relevant information regarding the dataset used by CTS-Lite

### Dockerfile
The Dockerfile in this directory is for the base image, containing only the dataset

### CSV Magic
The csv_magic directory is used to manipulate the datasets (csv) files into the format CTS-Lite expects

### Expected Header
CTS-Lite expects the following header for its dataset:  
- `Identifier,FirstBlock,PubMed_Count,Patent_Count,MolecularFormula,SMILES,InChI,InChIKey,MonoisotopicMass,CompoundName`

### Dataset Creation Workflow
Here is a step-by-step guide to creating the dataset for CTS-Lite

1. Download the most recent PubChemLite csv from [zenodo](https://zenodo.org/records/18169629)
2. Trim the csv using "pubchemlite_trimmer.sh". This will remove all unnecessary columns:
    - `./pubchemlite_trimmer.sh pubchemlite.csv`
3. Download additional data from PubChem [here](https://pubchem.ncbi.nlm.nih.gov/classification/#hid=72)
    - Select the queries you are interested in adding to the dataset (CTS-Lite uses "Spectral Information -> Mass Spectrometry")
    - Query the categories and click download on the right-hand side
        - Downloads are limited to queries of ~600,000 compounds. You will have to query the subcategories individually and merge their csvs manually to download the larger categories. If they don't have subcategories, you might be out of luck, I couldn't find a reliable way of downloading larger categories.
    - Select the following 9 fields for your download:
        - Name, InChIKey, InChI, SMILES, Molecular Formula, Exact Mass, Linked PubChem Literature Count, Linked PubChem Patent Count
4. Download each PubChem query as a csv and combine them using csvkit's "csvstack" command:
    - `csvstack gc-ms.csv lc-ms.csv > gc-lc-ms.csv`
    - `csvstack gc-lc-ms.csv other-ms.csv > all-ms.csv`
5. Before csvstacking "all-ms.csv" with the PubChemLite data, you will have to rename and reorder the columns of "all-ms.csv"
    - First, use the "firstblock" module to generate the First Block column for a csv:
        - `go run ./firstblock/firstblock.go all-ms.csv`
    - Second, use "reorder.sh" to reorder and rename the columns of the csv to be aligned with PubChemLite:
        - `./reorder.sh firstblocks_all-ms.csv reordered-ms.csv`
6. Merge the reordered csv with the PubChemLite csv:
    - `csvstack PubChemLite_trimmed.csv reordered-ms.csv > total.csv`
    - Note: order matters. Put the PubChemLite csv first to prioritize keeping its compounds when removing duplicates in the next step
7. Once merged, use the "dedupe" module to remove any duplicate compounds from the dataset:
    - `go run ./dedupe/dedupe.go total.csv`
