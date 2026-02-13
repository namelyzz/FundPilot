package main

import (
	"strings"
	"testing"
)

func TestParseCSV_MinimalAndPrevOptional(t *testing.T) {
	body := `trade_date,is_open,prev_trade_date
2026-02-09,true,
2026-02-10,true,2026-02-09
2026-02-11,false,2026-02-10
2026-02-12,1,2026-02-10
2026-02-13,yes,2026-02-12
`
	entries, err := parseCSV(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 5 {
		t.Fatalf("len = %d", len(entries))
	}
	if entries[0].PrevTradeDate != nil {
		t.Error("first row prev should be nil")
	}
	if entries[1].PrevTradeDate == nil {
		t.Error("second row prev should be set")
	}
	if entries[2].IsOpen {
		t.Error("holiday should not be open")
	}
	if !entries[3].IsOpen {
		t.Error("'1' should parse as true")
	}
	if !entries[4].IsOpen {
		t.Error("'yes' should parse as true")
	}
}

func TestParseCSV_HeaderInDifferentOrder(t *testing.T) {
	body := `is_open,prev_trade_date,trade_date
true,,2026-02-09
`
	entries, err := parseCSV(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if !entries[0].IsOpen || entries[0].TradeDate.IsZero() {
		t.Errorf("entry parsed wrong: %+v", entries[0])
	}
}

func TestParseCSV_MissingRequiredColumn(t *testing.T) {
	body := "trade_date\n2026-02-09\n"
	_, err := parseCSV(strings.NewReader(body))
	if err == nil {
		t.Fatal("expected missing-column error")
	}
}

func TestParseCSV_InvalidDate(t *testing.T) {
	body := "trade_date,is_open\nnot-a-date,true\n"
	_, err := parseCSV(strings.NewReader(body))
	if err == nil {
		t.Fatal("expected date parse error")
	}
}

func TestParseCSV_InvalidBool(t *testing.T) {
	body := "trade_date,is_open\n2026-02-09,maybe\n"
	_, err := parseCSV(strings.NewReader(body))
	if err == nil {
		t.Fatal("expected bool parse error")
	}
}

func TestParseBool_Variants(t *testing.T) {
	trues := []string{"true", "True", "1", "YES", "y", "t"}
	for _, s := range trues {
		got, err := parseBool(s)
		if err != nil || !got {
			t.Errorf("parseBool(%q) = %v,%v want true", s, got, err)
		}
	}
	falses := []string{"false", "0", "no", "n", "f", ""}
	for _, s := range falses {
		got, err := parseBool(s)
		if err != nil || got {
			t.Errorf("parseBool(%q) = %v,%v want false", s, got, err)
		}
	}
	if _, err := parseBool("maybe"); err == nil {
		t.Error("invalid value should error")
	}
}
