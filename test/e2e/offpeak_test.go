//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/Thinnesen/offpeak/test/utils"
)

const (
	// jobImage is the image of the test Jobs, pinned by digest.
	jobImage = "busybox:1.38.0@sha256:fd7dc98638c8e305f4dc34e979f1c0fdfdcaeb0fbf8fcff77ae834b6da3d7e6e"
	// jobNamespace is the namespace of the test DeferrableJobs.
	jobNamespace = "offpeak-e2e"
)

// kubectlApply applies a manifest from stdin.
func kubectlApply(manifest string) {
	GinkgoHelper()
	cmd := exec.Command("kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(manifest)
	_, err := utils.Run(cmd)
	Expect(err).NotTo(HaveOccurred())
}

// kubectlGet returns a JSONPath value of an object in the test namespace.
func kubectlGet(g Gomega, kind, name, jsonPath string) string {
	out, err := utils.Run(exec.Command("kubectl", "get", kind, name, "-n", jobNamespace, "-o", "jsonpath="+jsonPath))
	g.Expect(err).NotTo(HaveOccurred())
	return out
}

// deferrableJob returns a DeferrableJob manifest that uses the live DK1 carbon signal.
func deferrableJob(name string, notBefore, deadline time.Time, duration string) string {
	nb := ""
	if !notBefore.IsZero() {
		nb = fmt.Sprintf("  notBefore: %q\n", notBefore.UTC().Format(time.RFC3339))
	}
	return fmt.Sprintf(`apiVersion: offpeak.thinnesen.com/v1alpha1
kind: DeferrableJob
metadata:
  name: %s
  namespace: %s
spec:
  deadline: %q
%s  estimatedDuration: %s
  signals:
    - sourceRef: dk1-carbon
      weight: 100
  jobTemplate:
    spec:
      backoffLimit: 0
      template:
        spec:
          restartPolicy: Never
          securityContext:
            runAsNonRoot: true
            runAsUser: 65534
            seccompProfile:
              type: RuntimeDefault
          containers:
            - name: main
              image: %s
              command: ["sh", "-c", "echo offpeak e2e"]
              securityContext:
                allowPrivilegeEscalation: false
                readOnlyRootFilesystem: true
                capabilities:
                  drop: ["ALL"]
`, name, jobNamespace, deadline.UTC().Format(time.RFC3339), nb, duration, jobImage)
}

// offpeakSpecs registers the offpeak e2e specs. They run after the controller is up.
func offpeakSpecs() {
	It("plans with a live Energi Data Service signal", func() {
		By("creating the test namespace and the DK1 carbon SignalSource")
		_, _ = utils.Run(exec.Command("kubectl", "create", "ns", jobNamespace))
		kubectlApply(`apiVersion: offpeak.thinnesen.com/v1alpha1
kind: SignalSource
metadata:
  name: dk1-carbon
spec:
  energiDataService:
    area: DK1
    series: CO2Prognosis
`)

		By("creating a DeferrableJob with a short window")
		now := time.Now()
		kubectlApply(deferrableJob("short-window", time.Time{}, now.Add(3*time.Minute), "2m"))

		By("waiting until the Job ran and succeeded")
		Eventually(func(g Gomega) {
			g.Expect(kubectlGet(g, "deferrablejob", "short-window", "{.status.phase}")).
				To(Equal("Succeeded"))
		}, 5*time.Minute, 2*time.Second).Should(Succeed())
		Eventually(func(g Gomega) {
			g.Expect(kubectlGet(g, "job", "short-window", "{.status.succeeded}")).To(Equal("1"))
		}).Should(Succeed())

		By("checking that the signal was available")
		Eventually(func(g Gomega) {
			cond := kubectlGet(g, "deferrablejob", "short-window",
				`{.status.conditions[?(@.type=="SignalsAvailable")].status}`)
			g.Expect(cond).To(Equal("True"))
		}).Should(Succeed())
	})

	It("shows a future planned start and estimates for a long window", func() {
		now := time.Now()
		notBefore := now.Add(2 * time.Hour)
		kubectlApply(deferrableJob("long-window", notBefore, now.Add(30*time.Hour), "1h"))

		Eventually(func(g Gomega) {
			out := kubectlGet(g, "deferrablejob", "long-window", "{.status}")
			var st struct {
				Phase        string    `json:"phase"`
				PlannedStart time.Time `json:"plannedStart"`
				JobName      string    `json:"jobName"`
				Estimates    []struct {
					Source, Unit, Planned, IfStartedNow string
				} `json:"estimates"`
			}
			g.Expect(json.Unmarshal([]byte(out), &st)).To(Succeed())
			g.Expect(st.Phase).To(Equal("Waiting"))
			g.Expect(st.JobName).To(BeEmpty())
			g.Expect(st.PlannedStart).To(BeTemporally(">=", notBefore.Truncate(time.Second)))
			g.Expect(st.Estimates).To(HaveLen(1))
			g.Expect(st.Estimates[0].Source).To(Equal("dk1-carbon"))
			g.Expect(st.Estimates[0].Unit).To(Equal("gCO2eq/kWh"))
			g.Expect(st.Estimates[0].Planned).NotTo(BeEmpty())
		}).Should(Succeed())

		By("checking that no Job exists yet")
		_, err := utils.Run(exec.Command("kubectl", "get", "job", "long-window", "-n", jobNamespace))
		Expect(err).To(HaveOccurred())
	})
}

// cleanupSpecs removes the objects of offpeakSpecs and argoSpecs.
func cleanupSpecs() {
	It("cleans up the test objects", func() {
		_, _ = utils.Run(exec.Command("kubectl", "delete", "ns", jobNamespace, "--wait=false"))
		_, _ = utils.Run(exec.Command("kubectl", "delete", "signalsource", "dk1-carbon"))
	})
}
