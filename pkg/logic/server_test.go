package logic_test

import (
	"encoding/json"
	"fmt"
	jsonpatch "github.com/evanphx/json-patch/v5"
	"github.com/gardener/vpa-minallowed-remover/pkg/logic"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"io"
	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"strings"

	autoscalingv1 "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/apis/autoscaling.k8s.io/v1"
	"net/http"
	"net/http/httptest"
)

var (
	codecs            = serializer.NewCodecFactory(runtime.NewScheme())
	deserializer      = codecs.UniversalDeserializer()
	ar                *admissionv1.AdmissionReview
	vpa               *autoscalingv1.VerticalPodAutoscaler
	containerPolicies []autoscalingv1.ContainerResourcePolicy
	cpuQuantity       = resource.MustParse("300m")
	memQuantity       = resource.MustParse("1024G")
	rawVpa            []byte
	requestReader     *strings.Reader
)

var _ = Describe("Server", func() {
	BeforeEach(func() {
		ar = &admissionv1.AdmissionReview{Request: &admissionv1.AdmissionRequest{UID: "12345"}}

		vpa = &autoscalingv1.VerticalPodAutoscaler{
			TypeMeta: metav1.TypeMeta{
				Kind:       "VerticalPodAutoscaler",
				APIVersion: "autoscaling.k8s.io/v1",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-vpa",
			},
			Spec: autoscalingv1.VerticalPodAutoscalerSpec{
				ResourcePolicy: &autoscalingv1.PodResourcePolicy{
					ContainerPolicies: containerPolicies,
				},
			},
			Status: autoscalingv1.VerticalPodAutoscalerStatus{},
		}
	})
	Describe("Handling a correct AdmissionReview request", func() {
		Context("when CPU minAllowed is defined", func() {
			JustBeforeEach(func() {
				vpa.Spec.ResourcePolicy.ContainerPolicies = []autoscalingv1.ContainerResourcePolicy{
					{
						ContainerName: "container1",
						MinAllowed: corev1.ResourceList{
							corev1.ResourceCPU:    cpuQuantity,
							corev1.ResourceMemory: memQuantity,
						},
					},
					{
						ContainerName: "container2",
						MinAllowed: corev1.ResourceList{
							corev1.ResourceCPU:    cpuQuantity,
							corev1.ResourceMemory: memQuantity,
						},
					},
				}
				rawVpa, _ = json.Marshal(vpa)
				ar.Request.Resource = metav1.GroupVersionResource{
					Group:    "autoscaling.k8s.io",
					Version:  "v1",
					Resource: "verticalpodautoscalers",
				}
				ar.Request.Object = runtime.RawExtension{Raw: rawVpa}
				byteAr, _ := json.Marshal(ar)
				requestReader = strings.NewReader(string(byteAr))
			})
			It("should remove CPU minAllowed and add an Annotation", func() {
				server := httptest.NewServer(logic.NewServerWithoutSSL(":8080").Handler)
				resp, _ := http.Post(server.URL, "application/json", requestReader)

				// response code needs to be HTTP 200
				Expect(resp.StatusCode).To(Equal(http.StatusOK))

				admissionResponse, err := getAdmissionReview(resp.Body)
				if err != nil {
					Fail(fmt.Sprintf("error parsing body into admissionResponse: %s", err))
				}

				// resource should be admitted
				Expect(admissionResponse.Response.Allowed).To(BeTrue())

				// response needs to have the same UID as the request
				Expect(admissionResponse.Response.UID).To(Equal(ar.Request.UID))

				patch, err := jsonpatch.DecodePatch(admissionResponse.Response.Patch)
				if err != nil {
					Fail(fmt.Sprintf("couldn't decode the received jsonpatch: %s", err))
				}
				rawVpa, err = patch.Apply(rawVpa)
				if err != nil {
					Fail(fmt.Sprintf("couldn't apply the received jsonpatch: %s. Patch was %+v", err, patch))
				}
				patchedVPA := &autoscalingv1.VerticalPodAutoscaler{}
				err = json.Unmarshal(rawVpa, patchedVPA)
				if err != nil {
					Fail(fmt.Sprintf("failed to unmarshal the patched VPA: %s", err))
				}

				// Verify MinAllowed is removed for CPU only, but left alone for memory
				Expect(patchedVPA.Spec.ResourcePolicy.ContainerPolicies[0].MinAllowed.Cpu().String()).To(Equal("0"))
				Expect(patchedVPA.Spec.ResourcePolicy.ContainerPolicies[1].MinAllowed.Cpu().String()).To(Equal("0"))
				Expect(patchedVPA.Spec.ResourcePolicy.ContainerPolicies[0].MinAllowed.Memory().String()).To(Equal("1024G"))
				Expect(patchedVPA.Spec.ResourcePolicy.ContainerPolicies[1].MinAllowed.Memory().String()).To(Equal("1024G"))

				// Verify Annotation is added when MinAllowed was removed for a container
				Expect(patchedVPA.Annotations["vpaMinAllowedRemover"]).To(Equal("removed CPU minAllowed for container 0, container 1"))
			})
		})
		Context("when CPU minAllowed is NOT defined", func() {
			JustBeforeEach(func() {
				vpa.Spec.ResourcePolicy.ContainerPolicies = []autoscalingv1.ContainerResourcePolicy{
					{
						ContainerName: "container1",
						MinAllowed: corev1.ResourceList{
							corev1.ResourceMemory: memQuantity,
						},
					},
					{
						ContainerName: "container2",
						MinAllowed: corev1.ResourceList{
							corev1.ResourceMemory: memQuantity,
						},
					},
				}
				rawVpa, _ = json.Marshal(vpa)
				ar.Request.Resource = metav1.GroupVersionResource{
					Group:    "autoscaling.k8s.io",
					Version:  "v1",
					Resource: "verticalpodautoscalers",
				}
				ar.Request.Object = runtime.RawExtension{Raw: rawVpa}
				byteAr, _ := json.Marshal(ar)
				requestReader = strings.NewReader(string(byteAr))
			})
			It("should keep memory minAllowed and NOT add an Annotation", func() {
				server := httptest.NewServer(logic.NewServerWithoutSSL(":8080").Handler)
				resp, _ := http.Post(server.URL, "application/json", requestReader)

				// response code needs to be HTTP 200
				Expect(resp.StatusCode).To(Equal(http.StatusOK))

				admissionResponse, err := getAdmissionReview(resp.Body)
				if err != nil {
					Fail(fmt.Sprintf("error parsing body into admissionResponse: %s", err))
				}

				// resource should be admitted
				Expect(admissionResponse.Response.Allowed).To(BeTrue())

				// response needs to have the same UID as the request
				Expect(admissionResponse.Response.UID).To(Equal(ar.Request.UID))

				patch, err := jsonpatch.DecodePatch(admissionResponse.Response.Patch)
				if err != nil {
					Fail(fmt.Sprintf("couldn't decode the received jsonpatch: %s", err))
				}
				rawVpa, err = patch.Apply(rawVpa)
				if err != nil {
					Fail(fmt.Sprintf("couldn't apply the received jsonpatch: %s", err))
				}
				patchedVPA := &autoscalingv1.VerticalPodAutoscaler{}
				err = json.Unmarshal(rawVpa, patchedVPA)
				if err != nil {
					Fail(fmt.Sprintf("failed to unmarshal the patched VPA: %s", err))
				}

				// Verify MinAllowed is still 0 for CPU only, and left alone for memory
				Expect(patchedVPA.Spec.ResourcePolicy.ContainerPolicies[0].MinAllowed.Cpu().String()).To(Equal("0"))
				Expect(patchedVPA.Spec.ResourcePolicy.ContainerPolicies[1].MinAllowed.Cpu().String()).To(Equal("0"))
				Expect(patchedVPA.Spec.ResourcePolicy.ContainerPolicies[0].MinAllowed.Memory().String()).To(Equal("1024G"))
				Expect(patchedVPA.Spec.ResourcePolicy.ContainerPolicies[1].MinAllowed.Memory().String()).To(Equal("1024G"))

				// Verify Annotation is NOT added when MinAllowed was removed for a container
				_, found := patchedVPA.Annotations["vpaMinAllowedRemover"]
				Expect(found).To(BeFalse())
			})
		})
	})
	Describe("handling incorrect requests", func() {
		It("should return HTTP 400 when Content-Type is not set to 'application/json'", func() {
			server := httptest.NewServer(logic.NewServerWithoutSSL(":8080").Handler)
			resp, _ := http.Post(server.URL, "application/yaml", nil)
			Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))
		})
		It("should return HTTP 400 when the body is empty", func() {
			server := httptest.NewServer(logic.NewServerWithoutSSL(":8080").Handler)
			resp, _ := http.Post(server.URL, "application/json", nil)
			Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))
		})
		It("should return HTTP 400 when trying to review a different resource than VPA", func() {
			server := httptest.NewServer(logic.NewServerWithoutSSL(":8080").Handler)
			ar.Request.Resource = metav1.GroupVersionResource{
				Group:    "",
				Version:  "v1",
				Resource: "Pod",
			}
			byteAr, _ := json.Marshal(ar)
			r := strings.NewReader(string(byteAr))
			resp, _ := http.Post(server.URL, "application/json", r)
			Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))
		})
		It("should return HTTP 400 when trying to review VPA in a different version than v1", func() {
			server := httptest.NewServer(logic.NewServerWithoutSSL(":8080").Handler)
			ar.Request.Resource = metav1.GroupVersionResource{
				Group:    "autoscaling.k8s.io",
				Version:  "v1beta2",
				Resource: "verticalpodautoscalers",
			}
			byteAr, _ := json.Marshal(ar)
			r := strings.NewReader(string(byteAr))
			resp, _ := http.Post(server.URL, "application/json", r)
			Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))
		})
	})
})

func getAdmissionReview(r io.ReadCloser) (*admissionv1.AdmissionReview, error) {
	body, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	ar := &admissionv1.AdmissionReview{}
	_, _, err = deserializer.Decode(body, nil, ar)
	if err != nil {
		return nil, err
	}
	return ar, nil

}
