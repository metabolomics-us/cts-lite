package main

import (
	"bytes"
	"encoding/csv"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestParseSDQ(t *testing.T) {
	// The last substance has no CID
	sdq := "sid,cid,sidextid\n348274184,428,HMDB0000002\n348274190,892,HMDB0006088\n348274189,892,HMDB0000211\n348274191,,HMDB0099999\n"

	ids, err := parseSDQ(strings.NewReader(sdq))
	if err != nil {
		t.Fatalf("parseSDQ failed: %v", err)
	}
	want := map[string][]string{
		"428": {"HMDB0000002"},
		"892": {"HMDB0000211", "HMDB0006088"},
	}
	if diff := cmp.Diff(want, ids); diff != "" {
		t.Errorf("parseSDQ mismatch (-want +got):\n%s", diff)
	}

	// SDQ answers a bad query with an error message instead of CSV
	if _, err := parseSDQ(strings.NewReader("SDQOutputSet ::= {\n")); err == nil {
		t.Error("expected error for a non-CSV response, got nil")
	}
}

func TestJoinHMDB(t *testing.T) {
	reader := csv.NewReader(strings.NewReader("428,a\n7,b\n892,c\n"))
	var out bytes.Buffer
	ids := map[string][]string{"428": {"HMDB0000002"}, "892": {"HMDB0000211", "HMDB0006088"}}

	rows, withIDs, err := joinHMDB(reader, []string{"Identifier", "Name"}, csv.NewWriter(&out), ids)
	if err != nil {
		t.Fatalf("joinHMDB failed: %v", err)
	}
	if rows != 3 || withIDs != 2 {
		t.Errorf("got %d rows and %d with IDs, want 3 and 2", rows, withIDs)
	}
	want := "Identifier,Name,HMDB\n428,a,HMDB0000002\n7,b,\n892,c,HMDB0000211|HMDB0006088\n"
	if out.String() != want {
		t.Errorf("output mismatch:\ngot  %q\nwant %q", out.String(), want)
	}
}
