// Package registry builds signal sources from SignalSource objects.
package registry

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/clock"
	"sigs.k8s.io/controller-runtime/pkg/client"

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

// Options configure a Registry.
type Options struct {
	// Reader reads Secrets. Use the uncached API reader, so that "get" in one namespace is enough.
	Reader client.Reader
	// Namespace is the namespace of the token Secrets.
	Namespace string
	// Getter is the shared HTTP getter of the providers.
	Getter signal.Getter
	// Clock is the clock of the providers with their own cache.
	Clock clock.PassiveClock
	// OpenMeteoURL is the Open-Meteo base URL of the weatherProxy provider. The default is the public API.
	OpenMeteoURL string
	// AWSCredentials returns the AWS credentials for a region. The default is the default AWS credential chain.
	AWSCredentials func(ctx context.Context, region string) (aws.CredentialsProvider, error)
}

// Registry builds and keeps a Source for each SignalSource version. It is safe for concurrent use.
type Registry struct {
	opts Options
	// awsGetter does not cache: signed AWS requests differ at each call, and awsspot has its own cache.
	awsGetter signal.Getter

	mu      sync.Mutex
	sources map[string]cachedSource
}

type cachedSource struct {
	resourceVersion string
	source          signal.Source
}

// New returns a Registry.
func New(opts Options) *Registry {
	if opts.AWSCredentials == nil {
		opts.AWSCredentials = awsspot.DefaultCredentials
	}
	return &Registry{opts: opts, awsGetter: signal.NewHTTPGetter(), sources: map[string]cachedSource{}}
}

// Source returns the source for ss.
func (r *Registry) Source(ctx context.Context, ss *offpeakv1alpha1.SignalSource) (signal.Source, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c, ok := r.sources[ss.Name]; ok && c.resourceVersion == ss.ResourceVersion {
		return c.source, nil
	}
	src, err := r.build(ctx, ss)
	if err != nil {
		return nil, fmt.Errorf("SignalSource %s: %w", ss.Name, err)
	}
	r.sources[ss.Name] = cachedSource{resourceVersion: ss.ResourceVersion, source: src}
	return src, nil
}

func (r *Registry) build(ctx context.Context, ss *offpeakv1alpha1.SignalSource) (signal.Source, error) {
	spec := ss.Spec
	switch {
	case spec.EnergiDataService != nil:
		return energidataservice.New(energidataservice.Config{
			Area:   spec.EnergiDataService.Area,
			Series: string(spec.EnergiDataService.Series),
		}, r.opts.Getter)
	case spec.Entsoe != nil:
		return entsoe.New(entsoe.Config{BiddingZone: spec.Entsoe.BiddingZone},
			r.token(spec.Entsoe.TokenSecretRef), r.opts.Getter)
	case spec.ElectricityMaps != nil:
		return electricitymaps.New(electricitymaps.Config{Zone: spec.ElectricityMaps.Zone},
			r.token(spec.ElectricityMaps.TokenSecretRef), r.opts.Getter)
	case spec.WeatherProxy != nil:
		wp := spec.WeatherProxy
		return weatherproxy.New(weatherproxy.Config{
			CloudRegion: wp.CloudRegion, Latitude: wp.Latitude, Longitude: wp.Longitude, BaseURL: r.opts.OpenMeteoURL,
		}, r.opts.Getter)
	case spec.UKCarbonIntensity != nil:
		return ukcarbon.New(ukcarbon.Config{RegionID: spec.UKCarbonIntensity.RegionID}, r.opts.Getter)
	case spec.EnergyCharts != nil:
		return energycharts.New(energycharts.Config{Country: spec.EnergyCharts.Country}, r.opts.Getter)
	case spec.AWSSpot != nil:
		creds, err := r.opts.AWSCredentials(ctx, spec.AWSSpot.Region)
		if err != nil {
			return nil, err
		}
		return awsspot.New(awsspot.Config{
			Region:             spec.AWSSpot.Region,
			ProductDescription: spec.AWSSpot.ProductDescription,
		}, creds, r.awsGetter, r.opts.Clock)
	default:
		return nil, errors.New("no provider is set")
	}
}

// The controller reads token Secrets only in its own namespace. kustomize replaces "system".
// +kubebuilder:rbac:groups="",namespace=system,resources=secrets,verbs=get

// token reads the Secret at each call, so a rotated token works without a restart.
func (r *Registry) token(ref offpeakv1alpha1.SecretKeyRef) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		var s corev1.Secret
		if err := r.opts.Reader.Get(ctx, client.ObjectKey{Namespace: r.opts.Namespace, Name: ref.Name}, &s); err != nil {
			return "", fmt.Errorf("get Secret %s/%s: %w", r.opts.Namespace, ref.Name, err)
		}
		tok := strings.TrimSpace(string(s.Data[ref.Key]))
		if tok == "" {
			return "", fmt.Errorf("key %q of Secret %s/%s is empty", ref.Key, r.opts.Namespace, ref.Name)
		}
		return tok, nil
	}
}
