package config

import (
	"slices"
	"testing"
)

// SeedSplitDomains backfills dns.domains on startup so configurations written
// before the field existed still serve the CN split list (spec §9.2).
func TestStartupSeedsDefaultDomains(t *testing.T) {
	seeded := Default()
	seeded.DNS.Domains = nil
	if !SeedSplitDomains(&seeded) {
		t.Fatal("SeedSplitDomains on an empty list = false, want true")
	}
	if !slices.Equal(seeded.DNS.Domains, DefaultSplitDomains()) {
		t.Fatalf("seeded domains = %v, want the defaults", seeded.DNS.Domains)
	}

	custom := Default()
	custom.DNS.Domains = []string{"example.test"}
	if SeedSplitDomains(&custom) {
		t.Fatal("SeedSplitDomains on a configured list = true, want false")
	}
	if !slices.Equal(custom.DNS.Domains, []string{"example.test"}) {
		t.Fatalf("configured domains were modified: %v", custom.DNS.Domains)
	}
}
