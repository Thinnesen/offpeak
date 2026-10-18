package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// EnergiDataServiceSeries is a data set of Energi Data Service.
// +kubebuilder:validation:Enum=DayAheadPrice;CO2Prognosis
type EnergiDataServiceSeries string

const (
	// SeriesDayAheadPrice is the day-ahead electricity price in EUR/MWh (data set DayAheadPrices).
	SeriesDayAheadPrice EnergiDataServiceSeries = "DayAheadPrice"
	// SeriesCO2Prognosis is the CO2 emission prognosis in gCO2eq/kWh (data set CO2EmisProg).
	SeriesCO2Prognosis EnergiDataServiceSeries = "CO2Prognosis"
)

// SignalSourceSpec defines a signal provider. Exactly one provider must be set.
// +kubebuilder:validation:XValidation:rule="[has(self.energiDataService), has(self.entsoe), has(self.awsSpot), has(self.electricityMaps), has(self.weatherProxy), has(self.ukCarbonIntensity), has(self.energyCharts)].filter(x, x).size() == 1",message="exactly one provider must be set"
type SignalSourceSpec struct {
	// energiDataService reads Danish day-ahead prices or CO2 prognoses. It needs no key.
	// +optional
	EnergiDataService *EnergiDataServiceSource `json:"energiDataService,omitempty"`

	// entsoe reads day-ahead prices from the ENTSO-E Transparency Platform.
	// +optional
	Entsoe *EntsoeSource `json:"entsoe,omitempty"`

	// awsSpot reads the EC2 spot price history.
	// +optional
	AWSSpot *AWSSpotSource `json:"awsSpot,omitempty"`

	// electricityMaps reads the carbon intensity forecast of Electricity Maps.
	// +optional
	ElectricityMaps *ElectricityMapsSource `json:"electricityMaps,omitempty"`

	// weatherProxy estimates clean power from the Open-Meteo sun and wind forecast. It is a proxy. It needs no key.
	// +optional
	WeatherProxy *WeatherProxySource `json:"weatherProxy,omitempty"`

	// ukCarbonIntensity reads the 48-hour carbon forecast for Great Britain. It needs no key.
	// +optional
	UKCarbonIntensity *UKCarbonIntensitySource `json:"ukCarbonIntensity,omitempty"`

	// energyCharts reads the forecast of the non-renewable share for a European country. It needs no key.
	// +optional
	EnergyCharts *EnergyChartsSource `json:"energyCharts,omitempty"`
}

// EnergyChartsSource selects the country.
type EnergyChartsSource struct {
	// country is a lower-case country code with Energy-Charts data.
	// +kubebuilder:validation:Enum=at;be;ch;cz;de;dk;es;fi;fr;it;lu;nl;no;pl;pt;se
	// +required
	Country string `json:"country"`
}

// UKCarbonIntensitySource selects the area of Great Britain.
type UKCarbonIntensitySource struct {
	// regionID is 0 (the default) for Great Britain, or a regional id from 1 to 17, for example 13 for London.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=17
	// +optional
	RegionID int32 `json:"regionID,omitempty"`
}

// WeatherProxySource selects the place of the weather forecast. Set cloudRegion, or latitude and longitude.
// +kubebuilder:validation:XValidation:rule="has(self.cloudRegion) != (has(self.latitude) && has(self.longitude))",message="set cloudRegion, or latitude and longitude"
// +kubebuilder:validation:XValidation:rule="has(self.latitude) == has(self.longitude)",message="set latitude and longitude together"
type WeatherProxySource struct {
	// cloudRegion is a cloud region, for example aws:eu-north-1, azure:westeurope or gcp:us-central1.
	// +kubebuilder:validation:Pattern=`^(aws|azure|gcp):[a-z0-9-]+$`
	// +kubebuilder:validation:MaxLength=64
	// +optional
	CloudRegion string `json:"cloudRegion,omitempty"`

	// latitude in decimal degrees, for example "59.33".
	// +kubebuilder:validation:Pattern=`^-?[0-9]{1,2}(\.[0-9]{1,6})?$`
	// +optional
	Latitude string `json:"latitude,omitempty"`

	// longitude in decimal degrees, for example "18.07".
	// +kubebuilder:validation:Pattern=`^-?[0-9]{1,3}(\.[0-9]{1,6})?$`
	// +optional
	Longitude string `json:"longitude,omitempty"`
}

// EnergiDataServiceSource configures the Energi Data Service provider.
type EnergiDataServiceSource struct {
	// area is the Danish price area.
	// +kubebuilder:validation:Enum=DK1;DK2
	// +required
	Area string `json:"area"`

	// series is the data set to read.
	// +required
	Series EnergiDataServiceSeries `json:"series"`
}

// EntsoeSource configures the ENTSO-E provider.
type EntsoeSource struct {
	// biddingZone is the EIC code of the bidding zone, for example 10Y1001A1001A82H.
	// +kubebuilder:validation:Pattern=`^[0-9]{2}[A-Z][0-9A-Z-]{12}[0-9A-Z]$`
	// +required
	BiddingZone string `json:"biddingZone"`

	// tokenSecretRef selects the API token. The Secret must be in the controller namespace.
	// +required
	TokenSecretRef SecretKeyRef `json:"tokenSecretRef"`
}

// AWSSpotSource configures the AWS spot price provider.
type AWSSpotSource struct {
	// region is the AWS region, for example eu-north-1.
	// +kubebuilder:validation:Pattern=`^[a-z]{2}(-[a-z]+)+-[0-9]+$`
	// +kubebuilder:validation:MaxLength=32
	// +required
	Region string `json:"region"`

	// productDescription filters the spot price history.
	// +kubebuilder:validation:Enum="Linux/UNIX";"Linux/UNIX (Amazon VPC)";"Windows";"Windows (Amazon VPC)";"Red Hat Enterprise Linux";"SUSE Linux"
	// +kubebuilder:default="Linux/UNIX"
	// +optional
	ProductDescription string `json:"productDescription,omitempty"`
}

// ElectricityMapsSource configures the Electricity Maps provider.
type ElectricityMapsSource struct {
	// zone is the Electricity Maps zone, for example SE-SE3.
	// +kubebuilder:validation:Pattern=`^[A-Z]{2}(-[A-Z0-9]+)*$`
	// +kubebuilder:validation:MaxLength=32
	// +required
	Zone string `json:"zone"`

	// tokenSecretRef selects the API token. The Secret must be in the controller namespace.
	// +required
	TokenSecretRef SecretKeyRef `json:"tokenSecretRef"`
}

// SecretKeyRef selects a key of a Secret in the controller namespace.
type SecretKeyRef struct {
	// name is the name of the Secret.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`
	// +required
	Name string `json:"name"`

	// key is the key in the Secret data.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=`^[-._a-zA-Z0-9]+$`
	// +required
	Key string `json:"key"`
}

// SignalSourceStatus defines the observed state of SignalSource.
type SignalSourceStatus struct {
	// conditions hold the Ready condition.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// lastFetchTime is the time of the last successful fetch.
	// +optional
	LastFetchTime *metav1.Time `json:"lastFetchTime,omitempty"`

	// unit is the unit of the signal values, for example EUR/MWh.
	// +optional
	Unit string `json:"unit,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="Unit",type=string,JSONPath=".status.unit"
// +kubebuilder:printcolumn:name="Last fetch",type=date,JSONPath=".status.lastFetchTime"

// SignalSource is a cluster-wide source of a signal time series, for example a price or a carbon intensity.
type SignalSource struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the provider of the signal.
	// +required
	Spec SignalSourceSpec `json:"spec"`

	// status defines the observed state of SignalSource
	// +optional
	Status SignalSourceStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// SignalSourceList contains a list of SignalSource
type SignalSourceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []SignalSource `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &SignalSource{}, &SignalSourceList{})
		return nil
	})
}
