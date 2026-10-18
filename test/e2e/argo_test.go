//go:build e2e

package e2e

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/Thinnesen/offpeak/test/utils"
)

// Argo Workflows for the e2e test, pinned by version, manifest SHA-256 and image digests.
const (
	argoVersion        = "v4.1.5"
	argoManifestSHA256 = "9093296c8e1cb43938733656dac1cdea145a1d2bfb2faccebaa921e86f4e55b7"
	argoControllerImg  = "quay.io/argoproj/workflow-controller@sha256:" +
		"328307c9436f40883b2e9ada14ca4082f062159818a1ca9a4e88577d5dbba257"
	argoExecImg = "quay.io/argoproj/argoexec@sha256:" +
		"8275c3b970f399e9264ebd5f57e17f554e7494046d0f36f5f05ae2570a601de2"
)

// argoManifest downloads and checks the Argo manifest, removes argo-server and pins the image.
func argoManifest() string {
	GinkgoHelper()
	url := "https://github.com/argoproj/argo-workflows/releases/download/" + argoVersion + "/install.yaml"
	resp, err := http.Get(url) //nolint:gosec,noctx // pinned URL, checked by SHA-256 below
	Expect(err).NotTo(HaveOccurred())
	defer func() { _ = resp.Body.Close() }()
	Expect(resp.StatusCode).To(Equal(http.StatusOK))
	body, err := io.ReadAll(resp.Body)
	Expect(err).NotTo(HaveOccurred())
	sum := sha256.Sum256(body)
	Expect(hex.EncodeToString(sum[:])).To(Equal(argoManifestSHA256), "the Argo manifest changed")

	var docs []string
	for doc := range strings.SplitSeq(string(body), "\n---\n") {
		if strings.Contains(doc, "kind: Deployment") && strings.Contains(doc, "name: argo-server\n") {
			continue
		}
		docs = append(docs, doc)
	}
	manifest := strings.Join(docs, "\n---\n")
	return strings.ReplaceAll(manifest, "quay.io/argoproj/workflow-controller:"+argoVersion, argoControllerImg)
}

// argoWorkflow returns a Workflow that creates a DeferrableJob with a short window and waits for it.
func argoWorkflow(deadline time.Time) string {
	dj := deferrableJob("argo-", time.Time{}, deadline, "2m")
	dj = strings.Replace(dj, "  name: argo-\n", "  generateName: argo-\n", 1)
	var indented strings.Builder
	for line := range strings.SplitSeq(strings.TrimRight(dj, "\n"), "\n") {
		indented.WriteString("          " + line + "\n")
	}
	return fmt.Sprintf(`apiVersion: argoproj.io/v1alpha1
kind: Workflow
metadata:
  name: offpeak-demo
  namespace: %s
spec:
  serviceAccountName: offpeak-workflow
  entrypoint: deferred
  templates:
    - name: deferred
      resource:
        action: create
        setOwnerReference: true
        successCondition: status.phase == Succeeded
        failureCondition: status.phase == Failed
        manifest: |
%s`, jobNamespace, indented.String())
}

// argoRBAC lets the workflow create DeferrableJobs and write Argo task results.
const argoRBAC = `apiVersion: v1
kind: ServiceAccount
metadata:
  name: offpeak-workflow
  namespace: offpeak-e2e
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: offpeak-workflow
  namespace: offpeak-e2e
rules:
  - apiGroups: [offpeak.thinnesen.com]
    resources: [deferrablejobs]
    verbs: [create, get, list, watch, patch]
  - apiGroups: [argoproj.io]
    resources: [workflowtaskresults]
    verbs: [create, patch]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: offpeak-workflow
  namespace: offpeak-e2e
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: offpeak-workflow
subjects:
  - kind: ServiceAccount
    name: offpeak-workflow
    namespace: offpeak-e2e
`

// argoSpecs registers the Argo spec. It uses the namespace and SignalSource of offpeakSpecs.
func argoSpecs() {
	It("runs a DeferrableJob from an Argo Workflow", func() {
		By("installing Argo Workflows " + argoVersion)
		_, _ = utils.Run(exec.Command("kubectl", "create", "ns", "argo"))
		cmd := exec.Command("kubectl", "apply", "--server-side", "-n", "argo", "-f", "-")
		cmd.Stdin = strings.NewReader(argoManifest())
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred())
		_, err = utils.Run(exec.Command("kubectl", "patch", "configmap", "workflow-controller-configmap", "-n", "argo",
			"--type", "merge", "-p", fmt.Sprintf(`{"data":{"executor":"image: %s\n"}}`, argoExecImg)))
		Expect(err).NotTo(HaveOccurred())
		_, err = utils.Run(exec.Command("kubectl", "rollout", "status", "deployment/workflow-controller",
			"-n", "argo", "--timeout=5m"))
		Expect(err).NotTo(HaveOccurred())

		By("submitting a Workflow that creates a DeferrableJob")
		kubectlApply(argoRBAC)
		kubectlApply(argoWorkflow(time.Now().Add(3 * time.Minute)))

		By("waiting until the Workflow succeeded")
		Eventually(func(g Gomega) {
			g.Expect(kubectlGet(g, "workflow", "offpeak-demo", "{.status.phase}")).To(Equal("Succeeded"))
		}, 8*time.Minute, 5*time.Second).Should(Succeed())

		By("checking that the DeferrableJob succeeded and belongs to the Workflow")
		out, err := utils.Run(exec.Command("kubectl", "get", "deferrablejob", "-n", jobNamespace,
			"-o", `jsonpath={range .items[?(@.metadata.ownerReferences[0].kind=="Workflow")]}{.status.phase}{"\n"}{end}`))
		Expect(err).NotTo(HaveOccurred())
		Expect(utils.GetNonEmptyLines(out)).To(Equal([]string{"Succeeded"}))
	})

	It("removes Argo Workflows", func() {
		_, _ = utils.Run(exec.Command("kubectl", "delete", "workflow", "offpeak-demo", "-n", jobNamespace))
		_, _ = utils.Run(exec.Command("kubectl", "delete", "ns", "argo", "--wait=false"))
	})
}
