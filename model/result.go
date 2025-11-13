package model

type SingleResult struct {
	Query      string      `json:"query"`
	QueryType  string      `json:"query_type"`
	MatchFound bool        `json:"found_match"`
	MatchLevel string      `json:"match_level"`
	Matches    []*Compound `json:"matches"`
	ErrMsg     string      `json:"error_message"`
}
