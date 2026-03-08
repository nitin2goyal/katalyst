package metrics

import (
	"math"
	"testing"
)

// ---------------------------------------------------------------------------
// Percentile Tests
// ---------------------------------------------------------------------------

func TestPercentile_EmptySlice(t *testing.T) {
	a := NewAggregator()
	if got := a.Percentile(nil, 50); got != 0 {
		t.Errorf("Percentile(nil, 50) = %f, want 0", got)
	}
}

func TestPercentile_SingleValue(t *testing.T) {
	a := NewAggregator()
	if got := a.Percentile([]float64{42.0}, 50); got != 42.0 {
		t.Errorf("Percentile([42], 50) = %f, want 42", got)
	}
	if got := a.Percentile([]float64{42.0}, 99); got != 42.0 {
		t.Errorf("Percentile([42], 99) = %f, want 42", got)
	}
}

func TestPercentile_KnownValues(t *testing.T) {
	a := NewAggregator()
	values := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}

	tests := []struct {
		name string
		p    float64
		want float64
	}{
		{"P0", 0, 1.0},
		{"P50 (median)", 50, 5.5},
		{"P100", 100, 10.0},
		{"P25", 25, 3.25},
		{"P75", 75, 7.75},
		{"P95", 95, 9.55},
		{"P99", 99, 9.91},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := a.Percentile(values, tt.p)
			if math.Abs(got-tt.want) > 0.01 {
				t.Errorf("Percentile(1..10, %.0f) = %.2f, want %.2f", tt.p, got, tt.want)
			}
		})
	}
}

func TestPercentile_DoesNotMutateInput(t *testing.T) {
	a := NewAggregator()
	values := []float64{5, 3, 1, 4, 2}
	original := make([]float64, len(values))
	copy(original, values)

	a.Percentile(values, 50)

	for i := range values {
		if values[i] != original[i] {
			t.Errorf("input mutated at index %d: got %.0f, want %.0f", i, values[i], original[i])
		}
	}
}

func TestPercentile_TwoValues(t *testing.T) {
	a := NewAggregator()
	// P50 of [10, 20] = 15 (linear interpolation)
	got := a.Percentile([]float64{10, 20}, 50)
	if math.Abs(got-15.0) > 0.01 {
		t.Errorf("P50([10,20]) = %.2f, want 15.0", got)
	}
}

func TestPercentile_AllSameValues(t *testing.T) {
	a := NewAggregator()
	values := []float64{7, 7, 7, 7, 7}
	got := a.Percentile(values, 95)
	if got != 7.0 {
		t.Errorf("P95([7,7,7,7,7]) = %.2f, want 7", got)
	}
}

// ---------------------------------------------------------------------------
// Mean Tests
// ---------------------------------------------------------------------------

func TestMean_EmptySlice(t *testing.T) {
	a := NewAggregator()
	if got := a.Mean(nil); got != 0 {
		t.Errorf("Mean(nil) = %f, want 0", got)
	}
}

func TestMean_SingleValue(t *testing.T) {
	a := NewAggregator()
	if got := a.Mean([]float64{5.0}); got != 5.0 {
		t.Errorf("Mean([5]) = %f, want 5", got)
	}
}

func TestMean_MultipleValues(t *testing.T) {
	a := NewAggregator()
	got := a.Mean([]float64{10, 20, 30})
	if math.Abs(got-20.0) > 0.001 {
		t.Errorf("Mean([10,20,30]) = %f, want 20", got)
	}
}

// ---------------------------------------------------------------------------
// StdDev Tests
// ---------------------------------------------------------------------------

func TestStdDev_TooFewValues(t *testing.T) {
	a := NewAggregator()
	if got := a.StdDev(nil); got != 0 {
		t.Errorf("StdDev(nil) = %f, want 0", got)
	}
	if got := a.StdDev([]float64{5.0}); got != 0 {
		t.Errorf("StdDev([5]) = %f, want 0", got)
	}
}

func TestStdDev_IdenticalValues(t *testing.T) {
	a := NewAggregator()
	got := a.StdDev([]float64{5, 5, 5, 5})
	if got != 0 {
		t.Errorf("StdDev([5,5,5,5]) = %f, want 0", got)
	}
}

func TestStdDev_KnownValues(t *testing.T) {
	a := NewAggregator()
	// Sample std dev of [2, 4, 4, 4, 5, 5, 7, 9]
	// Mean = 5, Variance = 32/7 ≈ 4.571, StdDev ≈ 2.138
	values := []float64{2, 4, 4, 4, 5, 5, 7, 9}
	got := a.StdDev(values)
	expected := 2.138
	if math.Abs(got-expected) > 0.01 {
		t.Errorf("StdDev = %.3f, want ~%.3f", got, expected)
	}
}

// ---------------------------------------------------------------------------
// Max / Min Tests
// ---------------------------------------------------------------------------

func TestMax_EmptySlice(t *testing.T) {
	a := NewAggregator()
	if got := a.Max(nil); got != 0 {
		t.Errorf("Max(nil) = %f, want 0", got)
	}
}

func TestMax_Basic(t *testing.T) {
	a := NewAggregator()
	got := a.Max([]float64{3, 1, 4, 1, 5, 9, 2, 6})
	if got != 9.0 {
		t.Errorf("Max = %f, want 9", got)
	}
}

func TestMax_NegativeValues(t *testing.T) {
	a := NewAggregator()
	got := a.Max([]float64{-5, -3, -1, -8})
	if got != -1.0 {
		t.Errorf("Max(negatives) = %f, want -1", got)
	}
}

func TestMin_EmptySlice(t *testing.T) {
	a := NewAggregator()
	if got := a.Min(nil); got != 0 {
		t.Errorf("Min(nil) = %f, want 0", got)
	}
}

func TestMin_Basic(t *testing.T) {
	a := NewAggregator()
	got := a.Min([]float64{3, 1, 4, 1, 5, 9, 2, 6})
	if got != 1.0 {
		t.Errorf("Min = %f, want 1", got)
	}
}

func TestMin_NegativeValues(t *testing.T) {
	a := NewAggregator()
	got := a.Min([]float64{-5, -3, -1, -8})
	if got != -8.0 {
		t.Errorf("Min(negatives) = %f, want -8", got)
	}
}
