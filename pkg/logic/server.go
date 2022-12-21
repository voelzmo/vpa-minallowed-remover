package logic

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	admissionv1 "k8s.io/api/admission/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	autoscalingv1 "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/apis/autoscaling.k8s.io/v1"
	"k8s.io/klog/v2"
)

var codecs = serializer.NewCodecFactory(runtime.NewScheme())

// PatchRecord represents a single patch for modifying a resource.
type PatchRecord struct {
	Op    string      `json:"op,inline"`
	Path  string      `json:"path,inline"`
	Value interface{} `json:"value"`
}

type MinallowedRemover struct {
}

func NewServer() *MinallowedRemover {
	return &MinallowedRemover{}
}

func (m *MinallowedRemover) Serve(w http.ResponseWriter, r *http.Request) {
	deserializer := codecs.UniversalDeserializer()

	admissionReview, err := getAdmissionReview(r, deserializer)
	if err != nil {
		msg := fmt.Sprintf("error getting AdmissionReview from Request: %v", err)
		klog.Error(msg)
		http.Error(w, msg, http.StatusBadRequest)
		return
	}

	vpaGroupVersionResource := metav1.GroupVersionResource{Group: "autoscaling.k8s.io", Version: "v1", Resource: "verticalpodautoscalers"}
	if vpaGroupVersionResource != admissionReview.Request.Resource {
		msg := fmt.Sprintf("found invalid resource, got %+v, want %+v", admissionReview.Request.Resource, vpaGroupVersionResource)
		klog.Error(msg)
		http.Error(w, msg, http.StatusBadRequest)
		return
	}

	rawObject := admissionReview.Request.Object.Raw
	vpa := &autoscalingv1.VerticalPodAutoscaler{}
	_, _, err = deserializer.Decode(rawObject, nil, vpa)
	if err != nil {
		msg := fmt.Sprintf("error while parsing VPA from request: %s", err)
		klog.Error(msg)
		http.Error(w, msg, http.StatusBadRequest)
		return
	}

	var patches []PatchRecord

	patchedContainerStrings := []string{}
	for i, containerPolicy := range vpa.Spec.ResourcePolicy.ContainerPolicies {
		defaultQuantity := &resource.Quantity{
			Format: resource.DecimalSI,
		}
		if containerPolicy.MinAllowed != nil && containerPolicy.MinAllowed.Cpu() != defaultQuantity {
			patches = append(patches, PatchRecord{
				Op:   "remove",
				Path: fmt.Sprintf("/spec/resourcePolicy/containerPolicies/%v/minAllowed/cpu", i),
			})
			patchedContainerStrings = append(patchedContainerStrings, fmt.Sprintf("container %v", i))
		}
	}

	if len(patchedContainerStrings) > 0 {
		if vpa.Annotations == nil {
			emptyAnnotationPatch := PatchRecord{
				Op:    "add",
				Path:  "/metadata/annotations",
				Value: map[string]string{},
			}
			patches = append(patches, emptyAnnotationPatch)
		}
		patchedContainersMessage := strings.Join(patchedContainerStrings, ", ")
		patches = append(patches, PatchRecord{
			Op:    "add",
			Path:  "/metadata/annotations/vpaMinallowedRemover",
			Value: fmt.Sprintf("removed CPU minAllowed for %s", patchedContainersMessage),
		})
	}

	admissionResponse := &admissionv1.AdmissionResponse{}
	admissionResponse.Allowed = true
	admissionResponse.UID = admissionReview.Request.UID
	patchType := admissionv1.PatchTypeJSONPatch
	admissionResponse.PatchType = &patchType
	marshalledPatches, _ := json.Marshal(patches)
	admissionResponse.Patch = marshalledPatches

	admissionReview.Response = admissionResponse
	arBytes, err := json.Marshal(admissionReview)
	if err != nil {
		klog.Error(err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write(arBytes)
}

func getAdmissionReview(r *http.Request, deserializer runtime.Decoder) (*admissionv1.AdmissionReview, error) {
	// verify the content-type is application/json
	contentType := r.Header.Get("Content-Type")
	if contentType != "application/json" {
		return nil, fmt.Errorf("incorrect request content-type: got %q, expect 'application/json'", contentType)
	}

	var body []byte
	if r.Body == nil || r.Body == http.NoBody {
		return nil, errors.New("body is empty, expected AdmissionReview")
	}
	if data, err := io.ReadAll(r.Body); err == nil {
		body = data
	}

	admissionReview := &admissionv1.AdmissionReview{}
	_, _, err := deserializer.Decode(body, nil, admissionReview)
	if err != nil {
		return nil, fmt.Errorf("Request could not be decoded: %v", err)
	}

	return admissionReview, nil
}
