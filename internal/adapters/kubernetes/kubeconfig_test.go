package kubernetes

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	appconfig "github.com/krun-dev/krun/internal/config"
)

func writeKubeFixture(t *testing.T, name string, data []byte) {
	t.Helper()
	if err := os.WriteFile(name, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func kubeconfigFixture(server, clusterTLS, user string) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Config
current-context: target
clusters:
  - name: cluster
    cluster:
      server: %s
%s
contexts:
  - name: target
    context:
      cluster: cluster
      user: caller
      namespace: ignored-context-namespace
  - name: other
    context:
      cluster: cluster
      user: other
users:
  - name: caller
    user:
%s
  - name: other
    user:
      token: wrong-context-token
`, server, clusterTLS, user)
}

func serverCA(server *httptest.Server) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
}

func TestExplicitKubeconfigContextRelativePathsAndTokenRotation(t *testing.T) {
	directory := t.TempDir()
	tokenPath := filepath.Join(directory, "token")
	configPath := filepath.Join(directory, "kubeconfig")
	var headers []string
	var mutex sync.Mutex
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/namespaces/application/pods" {
			t.Errorf("request escaped application namespace: %s", r.URL.Path)
		}
		mutex.Lock()
		headers = append(headers, r.Header.Get("Authorization"))
		mutex.Unlock()
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	writeKubeFixture(t, filepath.Join(directory, "ca.crt"), serverCA(server))
	writeKubeFixture(t, tokenPath, []byte("first-token\n"))
	body := kubeconfigFixture(server.URL, "      certificate-authority: ca.crt", "      tokenFile: token")
	body = strings.Replace(body, "current-context: target", "current-context: other", 1)
	writeKubeFixture(t, configPath, []byte(body))
	// Neither the environment's file nor its cluster default may replace the
	// explicit application settings. The kubeconfig paths are relative to its file.
	t.Setenv("KUBECONFIG", filepath.Join(directory, "not-the-requested-file"))
	t.Setenv("KUBERNETES_MASTER", "https://127.0.0.1:1")
	client, err := New(&appconfig.WorkersConfig{
		KubernetesKubeconfigFile: configPath,
		KubernetesContext:        "target",
		Namespace:                "application",
	})
	if err != nil {
		t.Fatal(err)
	}
	if client.TokenFile != tokenPath {
		t.Fatalf("relative token path was not resolved: %s", client.TokenFile)
	}
	for _, token := range []string{"first-token", "second-token"} {
		writeKubeFixture(t, tokenPath, []byte(token+"\n"))
		if err := client.Call(context.Background(), http.MethodGet, client.Core("pods"), nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	mutex.Lock()
	defer mutex.Unlock()
	if len(headers) != 2 || headers[0] != "Bearer first-token" || headers[1] != "Bearer second-token" {
		t.Fatalf("selected context or token rotation failed: %v", headers)
	}
}

func TestKubeconfigCurrentContextAndEmbeddedToken(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer selected-token" {
			t.Error("selected token was not used")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	ca := base64.StdEncoding.EncodeToString(serverCA(server))
	file := filepath.Join(t.TempDir(), "kubeconfig")
	writeKubeFixture(t, file, []byte(kubeconfigFixture(server.URL, "      certificate-authority-data: "+ca, "      token: selected-token")))
	client, err := New(&appconfig.WorkersConfig{KubernetesKubeconfigFile: file})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Call(context.Background(), http.MethodGet, "/version", nil, nil); err != nil {
		t.Fatal(err)
	}
}

func clientIdentity(t *testing.T) ([]byte, []byte, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	ca := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-client-ca"},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	identity := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "test-controller"},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	cert, err := x509.CreateCertificate(rand.Reader, identity, ca, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	encodedKey, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: encodedKey})
	return certPEM, keyPEM, pool
}

func TestKubeconfigEmbeddedClientCertificate(t *testing.T) {
	cert, key, pool := clientIdentity(t)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(r.TLS.PeerCertificates) != 1 || r.TLS.PeerCertificates[0].Subject.CommonName != "test-controller" {
			t.Error("client certificate identity not presented")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	server.TLS = &tls.Config{
		ClientAuth: tls.RequireAndVerifyClientCert,
		ClientCAs:  pool,
		MinVersion: tls.VersionTLS12,
	}
	server.StartTLS()
	defer server.Close()
	file := filepath.Join(t.TempDir(), "kubeconfig")
	user := "      client-certificate-data: " + base64.StdEncoding.EncodeToString(cert) + "\n      client-key-data: " + base64.StdEncoding.EncodeToString(key)
	body := kubeconfigFixture(server.URL, "      certificate-authority-data: "+base64.StdEncoding.EncodeToString(serverCA(server)), user)
	writeKubeFixture(t, file, []byte(body))
	client, err := New(&appconfig.WorkersConfig{KubernetesKubeconfigFile: file})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Call(context.Background(), http.MethodGet, "/version", nil, nil); err != nil {
		t.Fatal(err)
	}
}

func TestKubeconfigErrorsDoNotFallBackToHostCredentials(t *testing.T) {
	directory := t.TempDir()
	fallback := filepath.Join(directory, "fallback")
	valid := kubeconfigFixture("https://127.0.0.1:1", "", "      token: fixture-token")
	writeKubeFixture(t, fallback, []byte(valid))
	t.Setenv("KUBECONFIG", fallback)
	for _, test := range []struct{ name, body, context string }{
		{name: "missing file"},
		{name: "malformed", body: "clusters: ["},
		{name: "unknown context", body: valid, context: "missing"},
		{name: "no current context", body: strings.Replace(valid, "current-context: target", "", 1)},
		{name: "missing cluster server", body: strings.Replace(valid, "server: https://127.0.0.1:1", "server: ''", 1)},
		{name: "unencrypted server", body: strings.Replace(valid, "https://127.0.0.1:1", "http://127.0.0.1:1", 1)},
		{name: "invalid CA", body: kubeconfigFixture("https://127.0.0.1:1", "      certificate-authority-data: aW52YWxpZA==", "      token: fixture-token")},
	} {
		t.Run(test.name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "kubeconfig")
			if test.body != "" {
				writeKubeFixture(t, file, []byte(test.body))
			}
			if _, err := New(&appconfig.WorkersConfig{KubernetesKubeconfigFile: file, KubernetesContext: test.context}); err == nil {
				t.Fatal("invalid explicit kubeconfig was accepted")
			}
		})
	}
}
