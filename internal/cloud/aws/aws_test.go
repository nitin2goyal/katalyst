package aws

import (
	"math"
	"testing"

	"github.com/koptimizer/koptimizer/pkg/cloudprovider"
)

// ---------------------------------------------------------------------------
// extractAWSFamily
// ---------------------------------------------------------------------------

func TestExtractAWSFamily(t *testing.T) {
	tests := []struct {
		name         string
		instanceType string
		want         string
	}{
		{"general purpose m5", "m5.xlarge", "m5"},
		{"compute optimized c5d", "c5d.4xlarge", "c5d"},
		{"memory optimized r6g", "r6g.medium", "r6g"},
		{"gpu instance p3", "p3.2xlarge", "p3"},
		{"no dot separator", "unknown", "unknown"},
		{"empty string", "", ""},
		{"multiple dots", "m5.xlarge.extra", "m5"},
		{"dot at start", ".xlarge", ""},
		{"dot at end", "m5.", "m5"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractAWSFamily(tt.instanceType)
			if got != tt.want {
				t.Errorf("extractAWSFamily(%q) = %q, want %q", tt.instanceType, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// computeAWSPrice
// ---------------------------------------------------------------------------

func TestComputeAWSPrice(t *testing.T) {
	tests := []struct {
		name     string
		family   string
		cpuCores int
		memMiB   int
		gpus     int
		gpuType  string
		want     float64
	}{
		{
			name:     "m5.xlarge equivalent (4 CPU, 16 GiB, no GPU)",
			family:   "m5",
			cpuCores: 4,
			memMiB:   16384,
			gpus:     0,
			gpuType:  "",
			// 4*0.048 + (16384/1024)*0.00643 = 0.192 + 0.10288 = 0.29488 -> rounds to 0.2949
			want: 0.2949,
		},
		{
			name:     "c5.2xlarge equivalent (8 CPU, 16 GiB, no GPU)",
			family:   "c5",
			cpuCores: 8,
			memMiB:   16384,
			gpus:     0,
			gpuType:  "",
			// 8*0.0425 + 16*0.00569 = 0.34 + 0.09104 = 0.43104 -> rounds to 0.431
			want: 0.431,
		},
		{
			name:     "GPU instance with V100 (unknown family falls back to m5)",
			family:   "p3",
			cpuCores: 8,
			memMiB:   61440,
			gpus:     1,
			gpuType:  "V100",
			// 8*0.048 + (61440/1024)*0.00643 + 1*2.448
			// = 0.384 + 60*0.00643 + 2.448
			// = 0.384 + 0.3858 + 2.448 = 3.2178
			want: 3.2178,
		},
		{
			name:     "GPU instance with A100",
			family:   "p4d",
			cpuCores: 96,
			memMiB:   1179648, // 1152 GiB
			gpus:     8,
			gpuType:  "A100",
			// 96*0.048 + (1179648/1024)*0.00643 + 8*3.40
			// = 4.608 + 1152*0.00643 + 27.2
			// = 4.608 + 7.40736 + 27.2 = 39.21536 -> rounds to 39.2154
			want: 39.2154,
		},
		{
			name:     "unknown family falls back to m5 rates",
			family:   "unknown",
			cpuCores: 2,
			memMiB:   8192,
			gpus:     0,
			gpuType:  "",
			// 2*0.048 + 8*0.00643 = 0.096 + 0.05144 = 0.14744 -> rounds to 0.1474
			want: 0.1474,
		},
		{
			name:     "unknown GPU type uses default rate 1.0",
			family:   "m5",
			cpuCores: 4,
			memMiB:   16384,
			gpus:     2,
			gpuType:  "UnknownGPU",
			// 4*0.048 + 16*0.00643 + 2*1.0 = 0.192 + 0.10288 + 2.0 = 2.29488 -> 2.2949
			want: 2.2949,
		},
		{
			name:     "zero CPU and memory",
			family:   "m5",
			cpuCores: 0,
			memMiB:   0,
			gpus:     0,
			gpuType:  "",
			want:     0.0,
		},
		{
			name:     "graviton m6g instance",
			family:   "m6g",
			cpuCores: 4,
			memMiB:   16384,
			gpus:     0,
			gpuType:  "",
			// 4*0.0385 + 16*0.00514 = 0.154 + 0.08224 = 0.23624 -> 0.2362
			want: 0.2362,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := computeAWSPrice(tt.family, tt.cpuCores, tt.memMiB, tt.gpus, tt.gpuType)
			if math.Abs(got-tt.want) > 0.0001 {
				t.Errorf("computeAWSPrice(%q, %d, %d, %d, %q) = %f, want %f",
					tt.family, tt.cpuCores, tt.memMiB, tt.gpus, tt.gpuType, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// parseAWSPriceListItem
// ---------------------------------------------------------------------------

func TestParseAWSPriceListItem(t *testing.T) {
	tests := []struct {
		name             string
		priceJSON        string
		wantInstanceType string
		wantPrice        float64
		wantOK           bool
	}{
		{
			name: "valid price list item",
			priceJSON: `{
				"product": {
					"attributes": {
						"instanceType": "m5.xlarge"
					}
				},
				"terms": {
					"OnDemand": {
						"offer1": {
							"priceDimensions": {
								"dim1": {
									"unit": "Hrs",
									"pricePerUnit": {"USD": "0.192"}
								}
							}
						}
					}
				}
			}`,
			wantInstanceType: "m5.xlarge",
			wantPrice:        0.192,
			wantOK:           true,
		},
		{
			name: "multiple offers takes first valid",
			priceJSON: `{
				"product": {
					"attributes": {
						"instanceType": "c5.2xlarge"
					}
				},
				"terms": {
					"OnDemand": {
						"offer1": {
							"priceDimensions": {
								"dim1": {
									"unit": "Hrs",
									"pricePerUnit": {"USD": "0.340"}
								}
							}
						}
					}
				}
			}`,
			wantInstanceType: "c5.2xlarge",
			wantPrice:        0.340,
			wantOK:           true,
		},
		{
			name:             "invalid JSON",
			priceJSON:        `{not valid json`,
			wantInstanceType: "",
			wantPrice:        0,
			wantOK:           false,
		},
		{
			name: "missing instanceType",
			priceJSON: `{
				"product": {
					"attributes": {}
				},
				"terms": {
					"OnDemand": {
						"offer1": {
							"priceDimensions": {
								"dim1": {
									"unit": "Hrs",
									"pricePerUnit": {"USD": "0.192"}
								}
							}
						}
					}
				}
			}`,
			wantInstanceType: "",
			wantPrice:        0,
			wantOK:           false,
		},
		{
			name: "empty instanceType",
			priceJSON: `{
				"product": {
					"attributes": {
						"instanceType": ""
					}
				},
				"terms": {
					"OnDemand": {
						"offer1": {
							"priceDimensions": {
								"dim1": {
									"unit": "Hrs",
									"pricePerUnit": {"USD": "0.192"}
								}
							}
						}
					}
				}
			}`,
			wantInstanceType: "",
			wantPrice:        0,
			wantOK:           false,
		},
		{
			name: "no OnDemand terms",
			priceJSON: `{
				"product": {
					"attributes": {
						"instanceType": "m5.xlarge"
					}
				},
				"terms": {
					"OnDemand": {}
				}
			}`,
			wantInstanceType: "",
			wantPrice:        0,
			wantOK:           false,
		},
		{
			name: "unit is not Hrs",
			priceJSON: `{
				"product": {
					"attributes": {
						"instanceType": "m5.xlarge"
					}
				},
				"terms": {
					"OnDemand": {
						"offer1": {
							"priceDimensions": {
								"dim1": {
									"unit": "Quantity",
									"pricePerUnit": {"USD": "100.00"}
								}
							}
						}
					}
				}
			}`,
			wantInstanceType: "",
			wantPrice:        0,
			wantOK:           false,
		},
		{
			name: "zero price",
			priceJSON: `{
				"product": {
					"attributes": {
						"instanceType": "m5.xlarge"
					}
				},
				"terms": {
					"OnDemand": {
						"offer1": {
							"priceDimensions": {
								"dim1": {
									"unit": "Hrs",
									"pricePerUnit": {"USD": "0.000"}
								}
							}
						}
					}
				}
			}`,
			wantInstanceType: "",
			wantPrice:        0,
			wantOK:           false,
		},
		{
			name: "missing USD price",
			priceJSON: `{
				"product": {
					"attributes": {
						"instanceType": "m5.xlarge"
					}
				},
				"terms": {
					"OnDemand": {
						"offer1": {
							"priceDimensions": {
								"dim1": {
									"unit": "Hrs",
									"pricePerUnit": {"CNY": "1.234"}
								}
							}
						}
					}
				}
			}`,
			wantInstanceType: "",
			wantPrice:        0,
			wantOK:           false,
		},
		{
			name: "invalid price string",
			priceJSON: `{
				"product": {
					"attributes": {
						"instanceType": "m5.xlarge"
					}
				},
				"terms": {
					"OnDemand": {
						"offer1": {
							"priceDimensions": {
								"dim1": {
									"unit": "Hrs",
									"pricePerUnit": {"USD": "not-a-number"}
								}
							}
						}
					}
				}
			}`,
			wantInstanceType: "",
			wantPrice:        0,
			wantOK:           false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			instanceType, price, ok := parseAWSPriceListItem(tt.priceJSON)
			if ok != tt.wantOK {
				t.Errorf("parseAWSPriceListItem() ok = %v, want %v", ok, tt.wantOK)
			}
			if instanceType != tt.wantInstanceType {
				t.Errorf("parseAWSPriceListItem() instanceType = %q, want %q", instanceType, tt.wantInstanceType)
			}
			if tt.wantOK && math.Abs(price-tt.wantPrice) > 0.0001 {
				t.Errorf("parseAWSPriceListItem() price = %f, want %f", price, tt.wantPrice)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// estimateInterruptionRate
// ---------------------------------------------------------------------------

func TestEstimateInterruptionRate(t *testing.T) {
	tests := []struct {
		name   string
		family string
		want   float64
	}{
		// High interruption families
		{"m5 high interrupt", "m5", 15.0},
		{"m5a high interrupt", "m5a", 15.0},
		{"c5 high interrupt", "c5", 15.0},
		{"c5a high interrupt", "c5a", 15.0},
		{"r5 high interrupt", "r5", 15.0},
		{"r5a high interrupt", "r5a", 15.0},
		{"t3 high interrupt", "t3", 15.0},
		{"t3a high interrupt", "t3a", 15.0},

		// Medium interruption families
		{"m6i medium interrupt", "m6i", 8.0},
		{"m6a medium interrupt", "m6a", 8.0},
		{"c6i medium interrupt", "c6i", 8.0},
		{"c6a medium interrupt", "c6a", 8.0},
		{"r6i medium interrupt", "r6i", 8.0},
		{"r6a medium interrupt", "r6a", 8.0},
		{"m5zn medium interrupt", "m5zn", 8.0},

		// Low interruption families
		{"m7i low interrupt", "m7i", 3.0},
		{"m7a low interrupt", "m7a", 3.0},
		{"m7g low interrupt", "m7g", 3.0},
		{"c7i low interrupt", "c7i", 3.0},
		{"c7g low interrupt", "c7g", 3.0},
		{"m6g low interrupt", "m6g", 3.0},
		{"c6g low interrupt", "c6g", 3.0},
		{"r6g low interrupt", "r6g", 3.0},
		{"r7i low interrupt", "r7i", 3.0},

		// Default rate for unknown families
		{"p3 default", "p3", 10.0},
		{"unknown family default", "xyz", 10.0},
		{"empty family default", "", 10.0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := estimateInterruptionRate(tt.family)
			if got != tt.want {
				t.Errorf("estimateInterruptionRate(%q) = %f, want %f", tt.family, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// matchRIToNodeGroup
// ---------------------------------------------------------------------------

func TestMatchRIToNodeGroup(t *testing.T) {
	tests := []struct {
		name            string
		ri              *cloudprovider.Commitment
		nodeGroupFamily string
		want            bool
	}{
		{
			name: "matching family",
			ri: &cloudprovider.Commitment{
				InstanceFamily: "m5",
			},
			nodeGroupFamily: "m5",
			want:            true,
		},
		{
			name: "case insensitive match",
			ri: &cloudprovider.Commitment{
				InstanceFamily: "M5",
			},
			nodeGroupFamily: "m5",
			want:            true,
		},
		{
			name: "non-matching family",
			ri: &cloudprovider.Commitment{
				InstanceFamily: "m5",
			},
			nodeGroupFamily: "c5",
			want:            false,
		},
		{
			name: "empty RI family",
			ri: &cloudprovider.Commitment{
				InstanceFamily: "",
			},
			nodeGroupFamily: "m5",
			want:            false,
		},
		{
			name: "empty node group family",
			ri: &cloudprovider.Commitment{
				InstanceFamily: "m5",
			},
			nodeGroupFamily: "",
			want:            false,
		},
		{
			name: "both empty",
			ri: &cloudprovider.Commitment{
				InstanceFamily: "",
			},
			nodeGroupFamily: "",
			want:            false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchRIToNodeGroup(tt.ri, tt.nodeGroupFamily)
			if got != tt.want {
				t.Errorf("matchRIToNodeGroup(%+v, %q) = %v, want %v",
					tt.ri, tt.nodeGroupFamily, got, tt.want)
			}
		})
	}
}
