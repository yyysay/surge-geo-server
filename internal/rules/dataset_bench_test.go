package rules

import (
	"os"
	"path/filepath"
	"testing"
)

// Set SURGE_GEO_BENCH_DATA to a directory containing geosite.dat and geoip.dat.
// Keeping real data out of the repository makes the fixture small and reusable.
func benchmarkData(b *testing.B) ([]byte, []byte) {
	b.Helper()
	dir := os.Getenv("SURGE_GEO_BENCH_DATA")
	if dir == "" {
		b.Skip("set SURGE_GEO_BENCH_DATA to benchmark a local DAT snapshot")
	}
	site, err := os.ReadFile(filepath.Join(dir, "geosite.dat"))
	if err != nil {
		b.Fatal(err)
	}
	ip, err := os.ReadFile(filepath.Join(dir, "geoip.dat"))
	if err != nil {
		b.Fatal(err)
	}
	return site, ip
}

func BenchmarkDatasetLoad(b *testing.B) {
	site, ip := benchmarkData(b)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, err := New(site, ip, RegexStrict); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDatasetLoadWithLookups(b *testing.B) {
	site, ip := benchmarkData(b)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		d, err := New(site, ip, RegexStrict)
		if err != nil {
			b.Fatal(err)
		}
		if _, err := d.LookupDomain("example.com"); err != nil {
			b.Fatal(err)
		}
		if _, err := d.LookupIP("8.8.8.8"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkGeoSiteCached(b *testing.B) {
	site, ip := benchmarkData(b)
	d, err := New(site, ip, RegexStrict)
	if err != nil {
		b.Fatal(err)
	}
	if !d.HasGeoSite("cn") {
		b.Fatal("benchmark requires the cn set")
	}
	d.RenderGeoSite("cn")
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		d.RenderGeoSite("cn")
	}
}
