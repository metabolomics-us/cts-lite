package rdkit

/*
#cgo CFLAGS: -I${SRCDIR}/lib
#cgo linux,amd64 LDFLAGS: -L${SRCDIR}/lib -lsmiles_inchikey -lm -lstdc++

#include <stdlib.h>
#include "smiles_inchikey.h"
*/
import "C"
import "unsafe"

// SmilesToInChIKey converts a SMILES string to an InChIKey.
// Returns ("", nil) if the SMILES is invalid or no InChIKey can be generated.
func SmilesToInChIKey(smiles string) (string, error) {
	cs := C.CString(smiles)
	defer C.free(unsafe.Pointer(cs))

	result := C.smiles_to_inchikey(cs)
	if result == nil {
		return "", nil
	}
	defer C.free(unsafe.Pointer(result))
	return C.GoString(result), nil
}
