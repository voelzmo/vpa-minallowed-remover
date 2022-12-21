package logic

import (
	"encoding/json"
	"github.com/evanphx/json-patch/v5"
	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	autoscalingv1 "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/apis/autoscaling.k8s.io/v1"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestServe(t *testing.T) {
	deserializer := codecs.UniversalDeserializer()
	ar := &admissionv1.AdmissionReview{Request: &admissionv1.AdmissionRequest{UID: "12345"}}
	cpuQuantity := resource.MustParse("300m")
	memQuantity := resource.MustParse("1024G")
	vpa := &autoscalingv1.VerticalPodAutoscaler{
		TypeMeta: metav1.TypeMeta{
			Kind:       "VerticalPodAutoscaler",
			APIVersion: "autoscaling.k8s.io/v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-vpa",
		},
		Spec: autoscalingv1.VerticalPodAutoscalerSpec{
			ResourcePolicy: &autoscalingv1.PodResourcePolicy{
				ContainerPolicies: []autoscalingv1.ContainerResourcePolicy{
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
				},
			},
		},
		Status: autoscalingv1.VerticalPodAutoscalerStatus{},
	}

	t.Run("good case", func(t *testing.T) {
		server := NewServer()
		ar.Request.Resource = metav1.GroupVersionResource{
			Group:    "autoscaling.k8s.io",
			Version:  "v1",
			Resource: "verticalpodautoscalers",
		}
		rawVpa, _ := json.Marshal(vpa)
		ar.Request.Object = runtime.RawExtension{Raw: rawVpa}
		byteAr, _ := json.Marshal(ar)
		r := strings.NewReader(string(byteAr))
		req := httptest.NewRequest(http.MethodGet, "/", r)
		req.Header.Add("Content-Type", "application/json")
		resp := httptest.NewRecorder()
		server.Serve(resp, req)

		// Needs to return HTTP 200 if all is ok
		AssertStatus(t, resp.Code, http.StatusOK)

		admissionResponse := &admissionv1.AdmissionReview{}
		_, _, err := deserializer.Decode(resp.Body.Bytes(), nil, admissionResponse)
		if err != nil {
			t.Errorf("error parsing body into admissionResponse: %s", err)
		}
		// Should admit resource
		AssertTrue(t, admissionResponse.Response.Allowed)

		// response needs to have the same UID as the request
		AssertTrue(t, admissionResponse.Response.UID == ar.Request.UID)

		// removes CPU minAllowed
		patch, err := jsonpatch.DecodePatch(admissionResponse.Response.Patch)
		if err != nil {
			t.Errorf("couldn't decode the received jsonpatch: %s", err)
		}
		rawVpa, err = patch.Apply(rawVpa)
		if err != nil {
			t.Errorf("couldn't apply the received jsonpatch: %s", err)
		}
		patchedVPA := &autoscalingv1.VerticalPodAutoscaler{}
		json.Unmarshal(rawVpa, patchedVPA)
		AssertTrue(t, patchedVPA.Spec.ResourcePolicy.ContainerPolicies[0].MinAllowed.Cpu().String() == "0")
		AssertTrue(t, patchedVPA.Spec.ResourcePolicy.ContainerPolicies[1].MinAllowed.Cpu().String() == "0")

		// doesn't touch memory minAllowed
		AssertTrue(t, patchedVPA.Spec.ResourcePolicy.ContainerPolicies[0].MinAllowed.Memory().String() == "1024G")
		AssertTrue(t, patchedVPA.Spec.ResourcePolicy.ContainerPolicies[1].MinAllowed.Memory().String() == "1024G")

		// adds an annotation patch
		AssertTrue(t, patchedVPA.Annotations["vpaMinallowedRemover"] == "removed CPU minAllowed for container 0, container 1")

	})
	t.Run("it doesn't add an Annotation when no CPU minAllowed is removed", func(t *testing.T) {

	})
	t.Run("it returns HTTP 400 when Content-Type is not set to 'application/json'", func(t *testing.T) {
		server := NewServer()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Add("Content-Type", "application/yaml")
		resp := httptest.NewRecorder()
		server.Serve(resp, req)
		AssertStatus(t, resp.Code, http.StatusBadRequest)
	})
	t.Run("it returns HTTP 400 when body is empty", func(t *testing.T) {
		server := NewServer()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Add("Content-Type", "application/json")
		resp := httptest.NewRecorder()
		server.Serve(resp, req)
		AssertStatus(t, resp.Code, http.StatusBadRequest)
	})
	t.Run("it returns HTTP 400 when trying to review different resource than VPA", func(t *testing.T) {
		server := NewServer()
		ar.Request.Resource = metav1.GroupVersionResource{
			Group:    "",
			Version:  "v1",
			Resource: "Pod",
		}
		byteAr, _ := json.Marshal(ar)
		r := strings.NewReader(string(byteAr))
		req := httptest.NewRequest(http.MethodGet, "/", r)
		req.Header.Add("Content-Type", "application/json")
		resp := httptest.NewRecorder()
		server.Serve(resp, req)
		AssertStatus(t, resp.Code, http.StatusBadRequest)
	})
	t.Run("it returns HTTP 400 when trying to review VPA in a different version than v1", func(t *testing.T) {
		server := NewServer()
		ar.Request.Resource = metav1.GroupVersionResource{
			Group:    "autoscaling.k8s.io",
			Version:  "v1beta2",
			Resource: "verticalpodautoscalers",
		}
		byteAr, _ := json.Marshal(ar)
		r := strings.NewReader(string(byteAr))
		req := httptest.NewRequest(http.MethodGet, "/", r)
		req.Header.Add("Content-Type", "application/json")
		resp := httptest.NewRecorder()
		server.Serve(resp, req)
		AssertStatus(t, resp.Code, http.StatusBadRequest)
	})
}

func AssertStatus(t *testing.T, got, want int) {
	t.Helper()
	if got != want {
		t.Errorf("response code didn't match: Got %v, want %v", got, want)
	}
}

func AssertTrue(t *testing.T, got bool) {
	t.Helper()
	if !got {
		t.Errorf("got %v, want true", got)
	}
}

func AssertFalse(t *testing.T, got bool) {
	t.Helper()
	if got {
		t.Errorf("got %v, want false", got)
	}
}
