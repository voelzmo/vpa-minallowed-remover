package logic

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	admissionv1 "k8s.io/api/admission/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	autoscalingv1 "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/apis/autoscaling.k8s.io/v1"
	"k8s.io/klog/v2"
	"net/http"
	"strings"
)

const (
	removeCPUMinAllowedPatch = `{"op": "remove", "path": "/spec/resourcePolicy/containerPolicies/%v/minAllowed/cpu"}`
	emptyAnnotationPatch     = `{"op": "add", "path": "/metadata/annotations", "value": {}}`
	addAnnotationPatch       = `{"op": "add", "path": "/metadata/annotations/vpaMinAllowedRemover", "value": "removed CPU minAllowed for %s"}`
)

var (
	codecs             = serializer.NewCodecFactory(runtime.NewScheme())
	patchTypeJSONPatch = admissionv1.PatchTypeJSONPatch
)

type Config struct {
	ListenPort    string `default:"8080"`
	CertDirectory string `default:"/etc/vpa-minallowed-remover-tls/"`
	TLSCertName   string `default:"tls.crt"`
	TLSKeyName    string `default:"tls.key"`
}

type MinallowedRemover struct {
	Deserializer runtime.Decoder
}

func NewServerWithoutSSL(listenAddress string) *http.Server {
	return &http.Server{
		Addr:      fmt.Sprintf(":%s", listenAddress),
		Handler:   &MinallowedRemover{Deserializer: codecs.UniversalDeserializer()},
		TLSConfig: nil,
	}
}

func (m *MinallowedRemover) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	admissionReview, err := m.getAdmissionReview(r)
	if err != nil {
		msg := fmt.Sprintf("error getting AdmissionReview from Request: %v", err)
		klog.Error(msg)
		http.Error(w, msg, http.StatusBadRequest)
		return
	}

	admissionResponse, err := m.GetAdmissionResponse(admissionReview.Request)
	if err != nil {
		msg := fmt.Sprintf("error creating AdmissionResponse from AdmissionRequest: %v", err)
		klog.Error(msg)
		http.Error(w, msg, http.StatusBadRequest)
		return
	}

	admissionReviewResponse := &admissionv1.AdmissionReview{Response: admissionResponse}

	arBytes, err := json.Marshal(admissionReviewResponse)
	if err != nil {
		klog.Error(err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write(arBytes)
}

func (m *MinallowedRemover) GetAdmissionResponse(request *admissionv1.AdmissionRequest) (*admissionv1.AdmissionResponse, error) {
	vpaGroupVersionResource := metav1.GroupVersionResource{Group: "autoscaling.k8s.io", Version: "v1", Resource: "verticalpodautoscalers"}
	if vpaGroupVersionResource != request.Resource {
		return nil, fmt.Errorf("found invalid resource, got %+v, want %+v", request.Resource, vpaGroupVersionResource)
	}

	rawObject := request.Object.Raw
	vpa := &autoscalingv1.VerticalPodAutoscaler{}
	_, _, err := m.Deserializer.Decode(rawObject, nil, vpa)
	if err != nil {
		return nil, fmt.Errorf("error while parsing VPA from request: %s", err)
	}

	patches := m.GetPatches(vpa)

	admissionResponse := &admissionv1.AdmissionResponse{}
	admissionResponse.Allowed = true
	admissionResponse.UID = request.UID

	admissionResponse.PatchType = &patchTypeJSONPatch
	marshalledPatches := []byte(fmt.Sprintf("[%s]", strings.Join(patches, ",")))
	admissionResponse.Patch = marshalledPatches

	return admissionResponse, nil
}

func (m *MinallowedRemover) GetPatches(vpa *autoscalingv1.VerticalPodAutoscaler) []string {
	var patches []string
	var patchedContainerStrings []string

	for i, containerPolicy := range vpa.Spec.ResourcePolicy.ContainerPolicies {
		if containerPolicy.MinAllowed != nil && containerPolicy.MinAllowed.Cpu().String() != "0" {
			patches = append(patches, fmt.Sprintf(removeCPUMinAllowedPatch, i))
			patchedContainerStrings = append(patchedContainerStrings, fmt.Sprintf("container %v", i))
		}
	}

	if len(patchedContainerStrings) > 0 {
		patchedContainersMessage := strings.Join(patchedContainerStrings, ", ")
		if vpa.Annotations == nil {
			patches = append(patches, emptyAnnotationPatch)
		}
		patches = append(patches, fmt.Sprintf(addAnnotationPatch, patchedContainersMessage))
	}
	return patches
}

func (m *MinallowedRemover) getAdmissionReview(r *http.Request) (*admissionv1.AdmissionReview, error) {
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
	_, _, err := m.Deserializer.Decode(body, nil, admissionReview)
	if err != nil {
		return nil, fmt.Errorf("request could not be decoded: %v", err)
	}

	return admissionReview, nil
}
