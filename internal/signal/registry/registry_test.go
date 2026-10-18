package registry

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	clocktesting "k8s.io/utils/clock/testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	offpeakv1alpha1 "github.com/Thinnesen/offpeak/api/v1alpha1"
	"github.com/Thinnesen/offpeak/internal/signal"
	"github.com/Thinnesen/offpeak/internal/signal/awsspot"
	"github.com/Thinnesen/offpeak/internal/signal/electricitymaps"
	"github.com/Thinnesen/offpeak/internal/signal/energidataservice"
	"github.com/Thinnesen/offpeak/internal/signal/energycharts"
	"github.com/Thinnesen/offpeak/internal/signal/entsoe"
	"github.com/Thinnesen/offpeak/internal/signal/ukcarbon"
	"github.com/Thinnesen/offpeak/internal/signal/weatherproxy"
)

const ns = "offpeak-system"

func newRegistry(t *testing.T, objs ...runtime.Object) *Registry {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(objs...).Build()
	return New(Options{
		Reader: c, Namespace: ns, Getter: signal.NewHTTPGetter(), Clock: clocktesting.NewFakeClock(time.Now()),
		AWSCredentials: func(_ context.Context, region string) (aws.CredentialsProvider, error) {
			if region == "xx-fail-1" {
				return nil, errors.New("no AWS configuration")
			}
			return credentials.NewStaticCredentialsProvider("AKIDEXAMPLE", "secret", ""), nil
		},
	})
}

func edsSource(rv string) *offpeakv1alpha1.SignalSource {
	return &offpeakv1alpha1.SignalSource{
		Name: "dk1-carbon", ResourceVersion: rv,
		Spec: offpeakv1alpha1.SignalSourceSpec{
			EnergiDataService: &offpeakv1alpha1.EnergiDataServiceSource{
				Area: "DK1", Series: offpeakv1alpha1.SeriesCO2Prognosis,
			},
		},
	}
}

func TestSourceEnergiDataService(t *testing.T) {
	r := newRegistry(t)
	ctx := context.Background()

	src, err := r.Source(ctx, edsSource("1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := src.(*energidataservice.Source); !ok {
		t.Errorf("Source() = %T, want *energidataservice.Source", src)
	}
	again, err := r.Source(ctx, edsSource("1"))
	if err != nil || again != src {
		t.Errorf("Source() with the same resourceVersion built a new source")
	}
	changed, err := r.Source(ctx, edsSource("2"))
	if err != nil || changed == src {
		t.Errorf("Source() with a new resourceVersion returned the old source")
	}
}

func TestSourceAWSSpot(t *testing.T) {
	r := newRegistry(t)
	ss := &offpeakv1alpha1.SignalSource{
		Name: "spot", ResourceVersion: "1",
		Spec: offpeakv1alpha1.SignalSourceSpec{AWSSpot: &offpeakv1alpha1.AWSSpotSource{Region: "eu-north-1"}},
	}
	src, err := r.Source(context.Background(), ss)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := src.(*awsspot.Source); !ok {
		t.Errorf("Source() = %T, want *awsspot.Source", src)
	}

	ss.Spec.AWSSpot.Region = "xx-fail-1"
	ss.ResourceVersion = "2"
	if _, err := r.Source(context.Background(), ss); err == nil {
		t.Error("Source() error = nil when the AWS configuration fails")
	}
}

func TestSourceEntsoe(t *testing.T) {
	r := newRegistry(t)
	src, err := r.Source(context.Background(), &offpeakv1alpha1.SignalSource{
		Name: "de-price",
		Spec: offpeakv1alpha1.SignalSourceSpec{Entsoe: &offpeakv1alpha1.EntsoeSource{
			BiddingZone: "10Y1001A1001A82H", TokenSecretRef: offpeakv1alpha1.SecretKeyRef{Name: "entsoe", Key: "token"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := src.(*entsoe.Source); !ok {
		t.Errorf("Source() = %T, want *entsoe.Source", src)
	}
}

func TestSourceElectricityMaps(t *testing.T) {
	r := newRegistry(t)
	src, err := r.Source(context.Background(), &offpeakv1alpha1.SignalSource{
		Name: "se3-carbon",
		Spec: offpeakv1alpha1.SignalSourceSpec{ElectricityMaps: &offpeakv1alpha1.ElectricityMapsSource{
			Zone: "SE-SE3", TokenSecretRef: offpeakv1alpha1.SecretKeyRef{Name: "electricitymaps", Key: "token"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := src.(*electricitymaps.Source); !ok {
		t.Errorf("Source() = %T, want *electricitymaps.Source", src)
	}
}

func TestSourceWeatherProxy(t *testing.T) {
	r := newRegistry(t)
	src, err := r.Source(context.Background(), &offpeakv1alpha1.SignalSource{
		Name: "nordic-weather",
		Spec: offpeakv1alpha1.SignalSourceSpec{WeatherProxy: &offpeakv1alpha1.WeatherProxySource{CloudRegion: "aws:eu-north-1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := src.(*weatherproxy.Source); !ok {
		t.Errorf("Source() = %T, want *weatherproxy.Source", src)
	}
}

func TestSourceUKCarbonIntensity(t *testing.T) {
	r := newRegistry(t)
	src, err := r.Source(context.Background(), &offpeakv1alpha1.SignalSource{
		Name: "london-carbon",
		Spec: offpeakv1alpha1.SignalSourceSpec{UKCarbonIntensity: &offpeakv1alpha1.UKCarbonIntensitySource{RegionID: 13}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := src.(*ukcarbon.Source); !ok {
		t.Errorf("Source() = %T, want *ukcarbon.Source", src)
	}
}

func TestSourceEnergyCharts(t *testing.T) {
	r := newRegistry(t)
	src, err := r.Source(context.Background(), &offpeakv1alpha1.SignalSource{
		Name: "de-renewables",
		Spec: offpeakv1alpha1.SignalSourceSpec{EnergyCharts: &offpeakv1alpha1.EnergyChartsSource{Country: "de"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := src.(*energycharts.Source); !ok {
		t.Errorf("Source() = %T, want *energycharts.Source", src)
	}
}

func TestSourceInvalidSpec(t *testing.T) {
	r := newRegistry(t)
	ss := edsSource("1")
	ss.Spec.EnergiDataService.Area = "DK9"
	if _, err := r.Source(context.Background(), ss); err == nil {
		t.Error("Source() error = nil for a bad area")
	}
	if _, err := r.Source(context.Background(), &offpeakv1alpha1.SignalSource{}); err == nil {
		t.Error("Source() error = nil without a provider")
	}
}

func TestToken(t *testing.T) {
	secret := &corev1.Secret{
		Name: "entsoe", Namespace: ns,
		Data: map[string][]byte{"token": []byte("s3cr3t\n")},
	}
	other := &corev1.Secret{
		Name: "elsewhere", Namespace: "default",
		Data: map[string][]byte{"token": []byte("x")},
	}
	empty := &corev1.Secret{
		Name: "empty", Namespace: ns,
		Data: map[string][]byte{"token": []byte("  ")},
	}
	r := newRegistry(t, secret, other, empty)
	ctx := context.Background()

	tok, err := r.token(offpeakv1alpha1.SecretKeyRef{Name: "entsoe", Key: "token"})(ctx)
	if err != nil || tok != "s3cr3t" {
		t.Errorf("token() = %q, %v, want the trimmed token", tok, err)
	}

	tests := []struct {
		name string
		ref  offpeakv1alpha1.SecretKeyRef
	}{
		{"missing Secret", offpeakv1alpha1.SecretKeyRef{Name: "nope", Key: "token"}},
		{"Secret in another namespace", offpeakv1alpha1.SecretKeyRef{Name: "elsewhere", Key: "token"}},
		{"missing key", offpeakv1alpha1.SecretKeyRef{Name: "entsoe", Key: "other"}},
		{"empty value", offpeakv1alpha1.SecretKeyRef{Name: "empty", Key: "token"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := r.token(tt.ref)(ctx)
			if err == nil {
				t.Fatal("token() error = nil")
			}
			if strings.Contains(err.Error(), "s3cr3t") {
				t.Errorf("error %q leaks the token", err)
			}
		})
	}
}
