// Package kubesecret creates a Secret holding a random password, once: if a
// Secret of that name already exists it is left as it is. The chart runs it
// before each install or sync, so the password is made in the cluster rather
// than in the chart, where Argo CD's rendering would make a new one each time.
package kubesecret

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

// Client talks to the Kubernetes API with a bearer token.
type Client struct {
	URL   string // the API server, e.g. https://10.96.0.1:443
	Token string
	HTTP  *http.Client
}

const saDir = "/var/run/secrets/kubernetes.io/serviceaccount"

// InCluster is a Client for the pod's own service account, and the
// namespace the pod runs in.
func InCluster() (Client, string, error) {
	host, port := os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT")
	if host == "" || port == "" {
		return Client{}, "", errors.New("not running in a cluster: KUBERNETES_SERVICE_HOST is unset")
	}
	token, err := os.ReadFile(saDir + "/token")
	if err != nil {
		return Client{}, "", err
	}
	ns, err := os.ReadFile(saDir + "/namespace")
	if err != nil {
		return Client{}, "", err
	}
	ca, err := os.ReadFile(saDir + "/ca.crt")
	if err != nil {
		return Client{}, "", err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		return Client{}, "", errors.New("no certificates in the service account's ca.crt")
	}
	hc := &http.Client{
		Timeout:   30 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}},
	}
	return Client{URL: "https://" + net.JoinHostPort(host, port), Token: strings.TrimSpace(string(token)), HTTP: hc}, strings.TrimSpace(string(ns)), nil
}

// Ensure creates Secret name in namespace with key set to a new random
// password (32 random bytes, hex), unless a Secret of that name exists. It
// reports whether it created one, and never reads or changes an existing
// Secret, so it needs only permission to create Secrets.
func (c Client) Ensure(ctx context.Context, namespace, name, key string) (bool, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return false, err
	}
	body, err := json.Marshal(map[string]any{
		"apiVersion": "v1",
		"kind":       "Secret",
		"type":       "Opaque",
		"metadata":   map[string]any{"name": name, "namespace": namespace},
		"stringData": map[string]string{key: hex.EncodeToString(b)},
	})
	if err != nil {
		return false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL+"/api/v1/namespaces/"+namespace+"/secrets", bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return false, err
	}
	defer res.Body.Close()
	switch res.StatusCode {
	case http.StatusCreated:
		return true, nil
	case http.StatusConflict:
		return false, nil
	}
	msg, _ := io.ReadAll(io.LimitReader(res.Body, 512))
	return false, fmt.Errorf("create secret %s/%s: %s: %s", namespace, name, res.Status, bytes.TrimSpace(msg))
}
