package failure

import (
	"testing"
	"time"
)

func TestStaleness_StringAndUsable(t *testing.T) {
	cases := []struct {
		s        Staleness
		name     string
		usable   bool
	}{
		{Fresh, "fresh", true},
		{Stale, "stale", true},
		{Expired, "expired", false},
	}
	for _, c := range cases {
		if got := c.s.String(); got != c.name {
			t.Errorf("%v String() = %q, want %q", c.s, got, c.name)
		}
		if got := c.s.IsUsable(); got != c.usable {
			t.Errorf("%v IsUsable() = %v, want %v", c.s, got, c.usable)
		}
	}
}

func TestConfidence_StringAndZeroValueIsUnsupported(t *testing.T) {
	var zero Confidence
	if zero != ConfidenceUnsupported {
		t.Fatalf("Confidence zero value should be Unsupported, got %v", zero)
	}
	cases := map[Confidence]string{
		ConfidenceHigh:        "high",
		ConfidenceMid:         "mid",
		ConfidenceLow:         "low",
		ConfidenceUnsupported: "unsupported",
	}
	for c, want := range cases {
		if got := c.String(); got != want {
			t.Errorf("%v String() = %q, want %q", c, got, want)
		}
	}
}

func TestComputeStaleness_Boundaries(t *testing.T) {
	now := time.Date(2026, 5, 24, 10, 0, 0, 0, time.UTC)
	ttl := 5 * time.Minute

	cases := []struct {
		name      string
		fetchedAt time.Time
		want      Staleness
	}{
		{"just_now", now, Fresh},
		{"within_ttl", now.Add(-4 * time.Minute), Fresh},
		{"exactly_ttl", now.Add(-5 * time.Minute), Fresh}, // age == ttl 算 Fresh
		{"1_5x_ttl", now.Add(-7*time.Minute - 30*time.Second), Stale},
		{"exactly_2x", now.Add(-10 * time.Minute), Stale},
		{"over_2x", now.Add(-11 * time.Minute), Expired},
	}
	for _, c := range cases {
		got := ComputeStaleness(c.fetchedAt, ttl, now)
		if got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestComputeStaleness_ZeroTTLAlwaysFresh(t *testing.T) {
	now := time.Now()
	old := now.Add(-365 * 24 * time.Hour)
	if got := ComputeStaleness(old, 0, now); got != Fresh {
		t.Errorf("ttl=0 should always Fresh, got %v", got)
	}
}

func TestComputeStalenessWithExpire(t *testing.T) {
	now := time.Date(2026, 5, 24, 10, 0, 0, 0, time.UTC)
	ttl := 5 * time.Minute
	expire := 30 * time.Minute

	cases := []struct {
		name      string
		fetchedAt time.Time
		want      Staleness
	}{
		{"fresh", now.Add(-3 * time.Minute), Fresh},
		{"stale_window_low", now.Add(-10 * time.Minute), Stale},
		{"stale_window_high", now.Add(-29 * time.Minute), Stale},
		{"expired", now.Add(-31 * time.Minute), Expired},
	}
	for _, c := range cases {
		got := ComputeStalenessWithExpire(c.fetchedAt, ttl, expire, now)
		if got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestComputeStalenessWithExpire_InvalidExpireFallsBack(t *testing.T) {
	now := time.Now()
	// expire <= ttl 时应当退化为 ComputeStaleness 的 2x 规则
	got := ComputeStalenessWithExpire(now.Add(-7*time.Minute), 5*time.Minute, 1*time.Minute, now)
	want := ComputeStaleness(now.Add(-7*time.Minute), 5*time.Minute, now)
	if got != want {
		t.Errorf("got %v, want %v (fallback to ComputeStaleness)", got, want)
	}
}

func TestComputeConfidence(t *testing.T) {
	cases := []struct {
		name      string
		coverage  float64
		anyStale  bool
		fallback  bool
		want      Confidence
	}{
		{"high_clean", 0.9, false, false, ConfidenceHigh},
		{"high_with_stale_downgrades_mid", 0.9, true, false, ConfidenceMid},
		{"high_with_fallback_downgrades_mid", 0.95, false, true, ConfidenceMid},
		{"mid_band", 0.7, false, false, ConfidenceMid},
		{"low_band", 0.3, false, false, ConfidenceLow},
		{"low_band_overrides_clean_flags", 0.1, false, false, ConfidenceLow},
		{"coverage_clamped_below_zero", -0.5, false, false, ConfidenceLow},
		{"coverage_clamped_above_one", 1.5, false, false, ConfidenceHigh},
		{"exact_0_5_is_mid", 0.5, false, false, ConfidenceMid},
		{"exact_0_8_is_high_clean", 0.8, false, false, ConfidenceHigh},
	}
	for _, c := range cases {
		got := ComputeConfidence(c.coverage, c.anyStale, c.fallback)
		if got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestSourcedValue_GenericAndHelpers(t *testing.T) {
	now := time.Date(2026, 5, 24, 10, 0, 0, 0, time.UTC)
	v := SourcedValue[float64]{
		Value:         1.2345,
		Source:        "tencent",
		FetchedAt:     now.Add(-3 * time.Minute),
		TTL:           5 * time.Minute,
		Staleness:     Fresh,
		FallbackUsed:  true,
		FallbackChain: []string{"eastmoney", "tencent"},
		Reason:        "eastmoney 5xx, retried 2 then fallback",
	}
	if v.Value != 1.2345 {
		t.Errorf("Value lost: %v", v.Value)
	}
	if !v.IsUsable() {
		t.Errorf("fresh should be usable")
	}
	if got := v.Age(now); got != 3*time.Minute {
		t.Errorf("Age = %v, want 3m", got)
	}
}

func TestSourcedValue_ZeroFetchedAtAgeZero(t *testing.T) {
	v := SourcedValue[int]{Value: 1}
	if got := v.Age(time.Now()); got != 0 {
		t.Errorf("zero FetchedAt should yield Age=0, got %v", got)
	}
}
