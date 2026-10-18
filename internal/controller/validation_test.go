package controller

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"

	offpeakv1alpha1 "github.com/Thinnesen/offpeak/api/v1alpha1"
)

const areaDK1 = "DK1"

// validDeferrableJob returns a DeferrableJob that the API server accepts.
func validDeferrableJob(name string) *offpeakv1alpha1.DeferrableJob {
	return &offpeakv1alpha1.DeferrableJob{
		Name: name, Namespace: "default",
		Spec: offpeakv1alpha1.DeferrableJobSpec{
			Deadline:          metav1.NewTime(time.Now().Add(24 * time.Hour)),
			EstimatedDuration: metav1.Duration{Duration: time.Hour},
			Signals:           []offpeakv1alpha1.SignalRef{{SourceRef: "dk1-carbon", Weight: 100}},
			JobTemplate: batchv1.JobTemplateSpec{
				Spec: batchv1.JobSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							RestartPolicy: corev1.RestartPolicyNever,
							Containers:    []corev1.Container{{Name: "main", Image: "busybox"}},
						},
					},
				},
			},
		},
	}
}

func validSignalSource(name string) *offpeakv1alpha1.SignalSource {
	return &offpeakv1alpha1.SignalSource{
		Name: name,
		Spec: offpeakv1alpha1.SignalSourceSpec{
			EnergiDataService: &offpeakv1alpha1.EnergiDataServiceSource{
				Area:   areaDK1,
				Series: offpeakv1alpha1.SeriesCO2Prognosis,
			},
		},
	}
}

func expectInvalid(err error) {
	GinkgoHelper()
	Expect(err).To(HaveOccurred())
	Expect(apierrors.IsInvalid(err)).To(BeTrue(), "error is not Invalid: %v", err)
}

var _ = Describe("API validation", func() {
	Context("DeferrableJob", func() {
		It("accepts a valid object", func() {
			dj := validDeferrableJob("valid-djob")
			Expect(k8sClient.Create(ctx, dj)).To(Succeed())
			Expect(k8sClient.Delete(ctx, dj)).To(Succeed())
		})

		DescribeTable("rejects an invalid object",
			func(change func(*offpeakv1alpha1.DeferrableJob)) {
				dj := validDeferrableJob("invalid-djob")
				change(dj)
				expectInvalid(k8sClient.Create(ctx, dj))
			},
			Entry("zero duration", func(dj *offpeakv1alpha1.DeferrableJob) {
				dj.Spec.EstimatedDuration = metav1.Duration{}
			}),
			Entry("negative duration", func(dj *offpeakv1alpha1.DeferrableJob) {
				dj.Spec.EstimatedDuration = metav1.Duration{Duration: -time.Minute}
			}),
			Entry("no signals", func(dj *offpeakv1alpha1.DeferrableJob) {
				dj.Spec.Signals = nil
			}),
			Entry("nine signals", func(dj *offpeakv1alpha1.DeferrableJob) {
				dj.Spec.Signals = nil
				for _, n := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"} {
					dj.Spec.Signals = append(dj.Spec.Signals, offpeakv1alpha1.SignalRef{SourceRef: n, Weight: 1})
				}
			}),
			Entry("duplicate signal", func(dj *offpeakv1alpha1.DeferrableJob) {
				dj.Spec.Signals = append(dj.Spec.Signals, dj.Spec.Signals[0])
			}),
			Entry("weight 0", func(dj *offpeakv1alpha1.DeferrableJob) {
				dj.Spec.Signals[0].Weight = 0
			}),
			Entry("weight 101", func(dj *offpeakv1alpha1.DeferrableJob) {
				dj.Spec.Signals[0].Weight = 101
			}),
			Entry("notBefore equal to deadline", func(dj *offpeakv1alpha1.DeferrableJob) {
				dj.Spec.NotBefore = new(dj.Spec.Deadline)
			}),
			Entry("notBefore after deadline", func(dj *offpeakv1alpha1.DeferrableJob) {
				dj.Spec.NotBefore = new(metav1.NewTime(dj.Spec.Deadline.Add(time.Hour)))
			}),
			Entry("empty Karpenter node pool", func(dj *offpeakv1alpha1.DeferrableJob) {
				dj.Spec.Karpenter = &offpeakv1alpha1.KarpenterSpec{}
			}),
			Entry("unknown capacity type", func(dj *offpeakv1alpha1.DeferrableJob) {
				dj.Spec.Karpenter = &offpeakv1alpha1.KarpenterSpec{NodePool: "gpu", CapacityType: "Cheap"}
			}),
			Entry("negative fallback margin", func(dj *offpeakv1alpha1.DeferrableJob) {
				dj.Spec.Karpenter = &offpeakv1alpha1.KarpenterSpec{
					NodePool: "gpu", FallbackMargin: &metav1.Duration{Duration: -time.Minute},
				}
			}),
			Entry("long name with SpotWithFallback", func(dj *offpeakv1alpha1.DeferrableJob) {
				dj.Name = strings.Repeat("a", 61)
				dj.Spec.Karpenter = &offpeakv1alpha1.KarpenterSpec{
					NodePool: "gpu", CapacityType: offpeakv1alpha1.CapacitySpotWithFallback,
				}
			}),
		)

		It("accepts a 60-character name with SpotWithFallback and defaults the capacity type", func() {
			dj := validDeferrableJob(strings.Repeat("b", 60))
			dj.Spec.Karpenter = &offpeakv1alpha1.KarpenterSpec{
				NodePool: "gpu", CapacityType: offpeakv1alpha1.CapacitySpotWithFallback,
			}
			Expect(k8sClient.Create(ctx, dj)).To(Succeed())
			Expect(k8sClient.Delete(ctx, dj)).To(Succeed())

			dj = validDeferrableJob(strings.Repeat("c", 61))
			dj.Spec.Karpenter = &offpeakv1alpha1.KarpenterSpec{NodePool: "gpu"}
			Expect(k8sClient.Create(ctx, dj)).To(Succeed())
			Expect(dj.Spec.Karpenter.CapacityType).To(Equal(offpeakv1alpha1.CapacityAny))
			Expect(k8sClient.Delete(ctx, dj)).To(Succeed())
		})

		It("accepts notBefore before deadline", func() {
			dj := validDeferrableJob("not-before-djob")
			dj.Spec.NotBefore = new(metav1.NewTime(dj.Spec.Deadline.Add(-time.Hour)))
			Expect(k8sClient.Create(ctx, dj)).To(Succeed())
			Expect(k8sClient.Delete(ctx, dj)).To(Succeed())
		})
	})

	Context("SignalSource", func() {
		It("accepts a valid object", func() {
			ss := validSignalSource("valid-source")
			Expect(k8sClient.Create(ctx, ss)).To(Succeed())
			Expect(k8sClient.Delete(ctx, ss)).To(Succeed())
		})

		secret := offpeakv1alpha1.SecretKeyRef{Name: "token", Key: "token"}
		DescribeTable("rejects an invalid object",
			func(spec offpeakv1alpha1.SignalSourceSpec) {
				ss := validSignalSource("invalid-source")
				ss.Spec = spec
				expectInvalid(k8sClient.Create(ctx, ss))
			},
			Entry("no provider", offpeakv1alpha1.SignalSourceSpec{}),
			Entry("two providers", offpeakv1alpha1.SignalSourceSpec{
				EnergiDataService: &offpeakv1alpha1.EnergiDataServiceSource{Area: areaDK1, Series: "DayAheadPrice"},
				AWSSpot:           &offpeakv1alpha1.AWSSpotSource{Region: "eu-north-1"},
			}),
			Entry("unknown area", offpeakv1alpha1.SignalSourceSpec{
				EnergiDataService: &offpeakv1alpha1.EnergiDataServiceSource{Area: "DK3", Series: "DayAheadPrice"},
			}),
			Entry("unknown series", offpeakv1alpha1.SignalSourceSpec{
				EnergiDataService: &offpeakv1alpha1.EnergiDataServiceSource{Area: areaDK1, Series: "Wind"},
			}),
			Entry("bad EIC code", offpeakv1alpha1.SignalSourceSpec{
				Entsoe: &offpeakv1alpha1.EntsoeSource{BiddingZone: "10Y&x=1", TokenSecretRef: secret},
			}),
			Entry("bad AWS region", offpeakv1alpha1.SignalSourceSpec{
				AWSSpot: &offpeakv1alpha1.AWSSpotSource{Region: "eu-north-1/../x"},
			}),
			Entry("bad zone", offpeakv1alpha1.SignalSourceSpec{
				ElectricityMaps: &offpeakv1alpha1.ElectricityMapsSource{Zone: "se se3", TokenSecretRef: secret},
			}),
			Entry("weather proxy without a place", offpeakv1alpha1.SignalSourceSpec{
				WeatherProxy: &offpeakv1alpha1.WeatherProxySource{},
			}),
			Entry("weather proxy with a region and a coordinate", offpeakv1alpha1.SignalSourceSpec{
				WeatherProxy: &offpeakv1alpha1.WeatherProxySource{CloudRegion: "aws:eu-north-1", Latitude: "1", Longitude: "2"},
			}),
			Entry("weather proxy with only a latitude", offpeakv1alpha1.SignalSourceSpec{
				WeatherProxy: &offpeakv1alpha1.WeatherProxySource{Latitude: "1"},
			}),
			Entry("weather proxy with a bad cloud", offpeakv1alpha1.SignalSourceSpec{
				WeatherProxy: &offpeakv1alpha1.WeatherProxySource{CloudRegion: "oracle:x"},
			}),
			Entry("UK region id out of range", offpeakv1alpha1.SignalSourceSpec{
				UKCarbonIntensity: &offpeakv1alpha1.UKCarbonIntensitySource{RegionID: 18},
			}),
			Entry("Energy-Charts country without data", offpeakv1alpha1.SignalSourceSpec{
				EnergyCharts: &offpeakv1alpha1.EnergyChartsSource{Country: "ie"},
			}),
			Entry("empty secret key", offpeakv1alpha1.SignalSourceSpec{
				Entsoe: &offpeakv1alpha1.EntsoeSource{
					BiddingZone: "10Y1001A1001A82H", TokenSecretRef: offpeakv1alpha1.SecretKeyRef{Name: "entsoe"},
				},
			}),
		)

		It("defaults the AWS product description", func() {
			ss := validSignalSource("aws-default")
			ss.Spec = offpeakv1alpha1.SignalSourceSpec{AWSSpot: &offpeakv1alpha1.AWSSpotSource{Region: "eu-north-1"}}
			Expect(k8sClient.Create(ctx, ss)).To(Succeed())
			Expect(ss.Spec.AWSSpot.ProductDescription).To(Equal("Linux/UNIX"))
			Expect(k8sClient.Delete(ctx, ss)).To(Succeed())
		})
	})

	It("accepts every sample in config/samples", func() {
		files, err := filepath.Glob(filepath.Join("..", "..", "config", "samples", "offpeak_*.yaml"))
		Expect(err).NotTo(HaveOccurred())
		Expect(files).NotTo(BeEmpty())
		for _, f := range files {
			data, err := os.ReadFile(f)
			Expect(err).NotTo(HaveOccurred())
			dec := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)
			for {
				obj := &unstructured.Unstructured{}
				if err := dec.Decode(&obj.Object); err == io.EOF {
					break
				} else {
					Expect(err).NotTo(HaveOccurred(), f)
				}
				if len(obj.Object) == 0 {
					continue
				}
				if obj.GetKind() == "DeferrableJob" {
					obj.SetNamespace("default")
				}
				Expect(k8sClient.Create(ctx, obj)).To(Succeed(), "%s: %s", f, obj.GetName())
				Expect(k8sClient.Delete(ctx, obj)).To(Succeed())
			}
		}
	})
})
