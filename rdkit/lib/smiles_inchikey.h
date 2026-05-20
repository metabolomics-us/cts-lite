#pragma once
#ifdef __cplusplus
extern "C" {
#endif

// Returns a malloc'd InChIKey string, or NULL on invalid SMILES or failure.
// Caller must free() the result.
char* smiles_to_inchikey(const char* smiles);

#ifdef __cplusplus
}
#endif
