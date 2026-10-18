package region

import (
	"regexp"
	"strings"
	"testing"
)

func TestLookup(t *testing.T) {
	info, err := Lookup("AWS:eu-north-1")
	if err != nil {
		t.Fatal(err)
	}
	if info.EntsoeZone != eicSE3 || info.ElectricityMapsZone != "SE-SE3" {
		t.Errorf("Lookup(aws:eu-north-1) = %+v", info)
	}
	if info, _ := Lookup("gcp:europe-west2"); !info.UKCarbonIntensity {
		t.Errorf("gcp:europe-west2 is in Great Britain: %+v", info)
	}
	if _, err := Lookup("aws:mars-1"); err == nil || !strings.Contains(err.Error(), "latitude") {
		t.Errorf("Lookup(aws:mars-1) error = %v, want a hint", err)
	}
}

func TestTable(t *testing.T) {
	key := regexp.MustCompile(`^(aws|azure|gcp):[a-z0-9-]+$`)
	eic := regexp.MustCompile(`^[0-9]{2}[A-Z][0-9A-Z-]{12}[0-9A-Z]$`)
	names := Names()
	if len(names) < 40 {
		t.Errorf("table has %d regions, want the main regions of three clouds", len(names))
	}
	for _, name := range names {
		info := regions[name]
		switch {
		case !key.MatchString(name):
			t.Errorf("%s: bad key", name)
		case info.Area == "" || info.Grid == "":
			t.Errorf("%s: area and grid are required", name)
		case info.Latitude < -90 || info.Latitude > 90 || info.Longitude < -180 || info.Longitude > 180:
			t.Errorf("%s: bad coordinate", name)
		case info.EntsoeZone != "" && !eic.MatchString(info.EntsoeZone):
			t.Errorf("%s: bad EIC %q", name, info.EntsoeZone)
		}
	}
}
