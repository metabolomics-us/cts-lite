#include "lib/smiles_inchikey.h"
#include <cstring>
#include <cstdlib>
#include <string>

namespace RDKit {
std::string InchiToInchiKey(const std::string &inchi);
}

extern "C" char *inchi_to_inchikey(const char *inchi) {
	if (inchi == nullptr) {
		return nullptr;
	}
	try {
		std::string key = RDKit::InchiToInchiKey(std::string(inchi));
		if (key.empty()) {
			return nullptr;
		}
		char *out = static_cast<char *>(malloc(key.size() + 1));
		if (out == nullptr) {
			return nullptr;
		}
		std::memcpy(out, key.c_str(), key.size() + 1);
		return out;
	} catch (...) {
		return nullptr;
	}
}
