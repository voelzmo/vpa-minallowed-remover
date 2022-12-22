package main

import (
	"crypto/tls"
	"strings"

	"github.com/gardener/vpa-minallowed-remover/pkg/logic"

	"github.com/kelseyhightower/envconfig"
	"k8s.io/klog/v2"
)

func main() {
	config := &logic.Config{}
	envconfig.Process("", config)
	ensureCorrectPaths(config)

	s := logic.NewServerWithoutSSL(config.ListenPort)
	cert, err := tls.LoadX509KeyPair(config.CertDirectory+config.TLSCertName, config.CertDirectory+config.TLSKeyName)
	if err != nil {
		klog.Fatalf("error while trying to create an X509 keypair from config: %s", err)
	}
	s.TLSConfig = &tls.Config{Certificates: []tls.Certificate{cert}}

}

func ensureCorrectPaths(config *logic.Config) {
	if !strings.HasSuffix(config.CertDirectory, "/") {
		config.CertDirectory += "/"
	}
}
