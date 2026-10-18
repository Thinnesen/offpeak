// Package region maps cloud regions to their grid and to a coordinate of the named area.
package region

import (
	"fmt"
	"maps"
	"slices"
	"strings"
)

// Info describes the grid of a cloud region.
type Info struct {
	// Area is the place that the cloud provider names for the region.
	Area string
	// Latitude and Longitude are a point in that area.
	Latitude, Longitude float64
	// Grid is a readable name of the power grid or the bidding zone.
	Grid string
	// EntsoeZone is the ENTSO-E EIC code of the bidding zone, for European regions.
	EntsoeZone string
	// ElectricityMapsZone is the Electricity Maps zone, when it is known.
	ElectricityMapsZone string
	// UKCarbonIntensity is true for regions in Great Britain (National Grid ESO data).
	UKCarbonIntensity bool
}

// EIC codes of the ENTSO-E bidding zones in the table.
const (
	eicDE   = "10Y1001A1001A82H" // DE-LU
	eicFR   = "10YFR-RTE------C"
	eicNL   = "10YNL----------L"
	eicBE   = "10YBE----------2"
	eicIE   = "10Y1001A1001A59C" // SEM (Ireland and Northern Ireland)
	eicSE3  = "10Y1001A1001A46L"
	eicFI   = "10YFI-1--------U"
	eicITNO = "10Y1001A1001A73I" // IT-North
)

// Shared grid descriptions for areas that several clouds use.
var (
	ashburn   = Info{Area: "Northern Virginia, US", Latitude: 39.04, Longitude: -77.49, Grid: "PJM", ElectricityMapsZone: "US-MIDA-PJM"}
	dublin    = Info{Area: "Dublin, Ireland", Latitude: 53.35, Longitude: -6.26, Grid: "SEM (Ireland)", EntsoeZone: eicIE, ElectricityMapsZone: "IE"}
	london    = Info{Area: "London, UK", Latitude: 51.51, Longitude: -0.13, Grid: "Great Britain", ElectricityMapsZone: "GB", UKCarbonIntensity: true}
	paris     = Info{Area: "Paris, France", Latitude: 48.86, Longitude: 2.35, Grid: "France", EntsoeZone: eicFR, ElectricityMapsZone: "FR"}
	frankfurt = Info{Area: "Frankfurt, Germany", Latitude: 50.11, Longitude: 8.68, Grid: "DE-LU", EntsoeZone: eicDE, ElectricityMapsZone: "DE"}
	tokyo     = Info{Area: "Tokyo, Japan", Latitude: 35.68, Longitude: 139.69, Grid: "TEPCO (Tokyo)", ElectricityMapsZone: "JP-TK"}
	singapore = Info{Area: "Singapore", Latitude: 1.35, Longitude: 103.82, Grid: "Singapore", ElectricityMapsZone: "SG"}
	sydney    = Info{Area: "Sydney, Australia", Latitude: -33.87, Longitude: 151.21, Grid: "NEM New South Wales", ElectricityMapsZone: "AU-NSW"}
	saoPaulo  = Info{Area: "Sao Paulo, Brazil", Latitude: -23.55, Longitude: -46.63, Grid: "Brazil South-East/Centre-West", ElectricityMapsZone: "BR-CS"}
	montreal  = Info{Area: "Montreal, Canada", Latitude: 45.50, Longitude: -73.57, Grid: "Hydro-Quebec", ElectricityMapsZone: "CA-QC"}
	mumbai    = Info{Area: "Mumbai, India", Latitude: 19.08, Longitude: 72.88, Grid: "India Western grid", ElectricityMapsZone: "IN-WE"}
)

// regions is the table. The key is "<cloud>:<region>".
var regions = map[string]Info{
	// AWS
	"aws:us-east-1":      ashburn,
	"aws:us-east-2":      {Area: "Ohio, US", Latitude: 39.96, Longitude: -83.00, Grid: "PJM", ElectricityMapsZone: "US-MIDA-PJM"},
	"aws:us-west-1":      {Area: "Northern California, US", Latitude: 37.34, Longitude: -121.89, Grid: "CAISO", ElectricityMapsZone: "US-CAL-CISO"},
	"aws:us-west-2":      {Area: "Oregon, US", Latitude: 45.84, Longitude: -119.70, Grid: "Bonneville Power (BPA)", ElectricityMapsZone: "US-NW-BPAT"},
	"aws:ca-central-1":   montreal,
	"aws:eu-west-1":      dublin,
	"aws:eu-west-2":      london,
	"aws:eu-west-3":      paris,
	"aws:eu-central-1":   frankfurt,
	"aws:eu-north-1":     {Area: "Stockholm, Sweden", Latitude: 59.33, Longitude: 18.07, Grid: "Sweden SE3", EntsoeZone: eicSE3, ElectricityMapsZone: "SE-SE3"},
	"aws:eu-south-1":     {Area: "Milan, Italy", Latitude: 45.46, Longitude: 9.19, Grid: "Italy North", EntsoeZone: eicITNO, ElectricityMapsZone: "IT-NO"},
	"aws:ap-northeast-1": tokyo,
	"aws:ap-southeast-1": singapore,
	"aws:ap-southeast-2": sydney,
	"aws:ap-south-1":     mumbai,
	"aws:sa-east-1":      saoPaulo,

	// Azure
	"azure:eastus":             ashburn,
	"azure:westus2":            {Area: "Washington, US", Latitude: 47.23, Longitude: -119.85, Grid: "Pacific Northwest"},
	"azure:canadacentral":      {Area: "Toronto, Canada", Latitude: 43.65, Longitude: -79.38, Grid: "Ontario (IESO)", ElectricityMapsZone: "CA-ON"},
	"azure:northeurope":        dublin,
	"azure:westeurope":         {Area: "Amsterdam, Netherlands", Latitude: 52.37, Longitude: 4.90, Grid: "Netherlands", EntsoeZone: eicNL, ElectricityMapsZone: "NL"},
	"azure:uksouth":            london,
	"azure:francecentral":      paris,
	"azure:germanywestcentral": frankfurt,
	"azure:swedencentral":      {Area: "Gavle, Sweden", Latitude: 60.67, Longitude: 17.14, Grid: "Sweden SE3", EntsoeZone: eicSE3, ElectricityMapsZone: "SE-SE3"},
	"azure:japaneast":          tokyo,
	"azure:southeastasia":      singapore,
	"azure:australiaeast":      sydney,
	"azure:centralindia":       {Area: "Pune, India", Latitude: 18.52, Longitude: 73.86, Grid: "India Western grid", ElectricityMapsZone: "IN-WE"},
	"azure:brazilsouth":        saoPaulo,

	// Google Cloud
	"gcp:us-central1":             {Area: "Iowa, US", Latitude: 41.26, Longitude: -95.86, Grid: "MISO", ElectricityMapsZone: "US-MIDW-MISO"},
	"gcp:us-east1":                {Area: "South Carolina, US", Latitude: 33.20, Longitude: -80.01, Grid: "South Carolina"},
	"gcp:us-east4":                ashburn,
	"gcp:us-west1":                {Area: "Oregon, US", Latitude: 45.59, Longitude: -121.18, Grid: "Bonneville Power (BPA)", ElectricityMapsZone: "US-NW-BPAT"},
	"gcp:northamerica-northeast1": montreal,
	"gcp:europe-west1":            {Area: "St. Ghislain, Belgium", Latitude: 50.45, Longitude: 3.82, Grid: "Belgium", EntsoeZone: eicBE, ElectricityMapsZone: "BE"},
	"gcp:europe-west2":            london,
	"gcp:europe-west3":            frankfurt,
	"gcp:europe-west4":            {Area: "Eemshaven, Netherlands", Latitude: 53.44, Longitude: 6.83, Grid: "Netherlands", EntsoeZone: eicNL, ElectricityMapsZone: "NL"},
	"gcp:europe-west9":            paris,
	"gcp:europe-north1":           {Area: "Hamina, Finland", Latitude: 60.57, Longitude: 27.20, Grid: "Finland", EntsoeZone: eicFI, ElectricityMapsZone: "FI"},
	"gcp:asia-northeast1":         tokyo,
	"gcp:asia-southeast1":         singapore,
	"gcp:australia-southeast1":    sydney,
	"gcp:asia-south1":             mumbai,
	"gcp:southamerica-east1":      saoPaulo,
}

// Lookup returns the grid of a cloud region, for example "aws:eu-north-1".
func Lookup(cloudRegion string) (Info, error) {
	info, ok := regions[strings.ToLower(cloudRegion)]
	if !ok {
		return Info{}, fmt.Errorf("unknown cloud region %q: use <aws|azure|gcp>:<region>, or set latitude and longitude", cloudRegion)
	}
	return info, nil
}

// Names returns all known cloud regions, sorted.
func Names() []string {
	return slices.Sorted(maps.Keys(regions))
}
