package main

import (
	"crypto/tls"
	"strings"

	"github.com/gardener/vpa-minallowed-remover/pkg/logic"

	"github.com/kelseyhightower/envconfig"
	"k8s.io/klog/v2"
)

func main() {
	klog.Info("starting vpa-minallowed-remover")
	config := &logic.Config{}
	err := envconfig.Process("", config)
	if err != nil {
		klog.Errorf("error while processing envconfig: %s", err)
	}
	ensureCorrectPaths(config)

	s := logic.NewServerWithoutSSL(config.ListenPort)
	cert, err := tls.LoadX509KeyPair(config.CertDirectory+config.TLSCertName, config.CertDirectory+config.TLSKeyName)
	if err != nil {
		klog.Fatalf("error while trying to create an X509 keypair from config: %s", err)
	}
	s.TLSConfig = &tls.Config{Certificates: []tls.Certificate{cert}}
	err = s.ListenAndServeTLS("", "")
	if err != nil {
		klog.Fatalf("error while starting ListenAndServe: %s", err)
	}
}

func ensureCorrectPaths(config *logic.Config) {
	if !strings.HasSuffix(config.CertDirectory, "/") {
		config.CertDirectory += "/"
	}
}
