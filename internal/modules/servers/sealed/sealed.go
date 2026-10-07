// Package sealed is where the servers module's secrets are sealed and opened,
// each under the AAD that names where it lives (docs/build/README.md,
// "Secrets"): generated values, secret parameters, an endpoint's credential,
// a deployment's secret parameters and deployed file content. Putting every
// AAD here keeps a blob from being opened as another kind of secret.
package sealed

import (
	"encoding/json"
	"fmt"

	"github.com/tikhonp/proxier/internal/modules/servers/endpoint"
	"github.com/tikhonp/proxier/internal/modules/servers/store"
	"github.com/tikhonp/proxier/internal/platform/vault"
)

func genAAD(serverID int64, key string) string { return fmt.Sprintf("server:%d:gen:%s", serverID, key) }
func paramsAAD(serverID int64) string          { return fmt.Sprintf("server:%d:params", serverID) }
func endpointAAD(serverID int64, key string) string {
	return fmt.Sprintf("server:%d:endpoint:%s", serverID, key)
}
func deploymentParamsAAD(id int64) string { return fmt.Sprintf("deployment:%d:params", id) }
func fileAAD(id int64, path string) string {
	return fmt.Sprintf("deployment:%d:file:%s", id, path)
}

// SealGen seals one generated value of a server.
func SealGen(v *vault.Vault, serverID int64, key, value string) []byte {
	return v.SealString(value, genAAD(serverID, key))
}

// OpenGenerated opens a server's generated values by key. A pending rotation
// value is not included: it is not in force until committed.
func OpenGenerated(v *vault.Vault, serverID int64, rows []store.GeneratedValue) (map[string]string, error) {
	out := make(map[string]string, len(rows))
	for _, r := range rows {
		s, err := v.OpenString(r.Value, genAAD(serverID, r.Key))
		if err != nil {
			return nil, fmt.Errorf("open generated value %q of server %d: %w", r.Key, serverID, err)
		}
		out[r.Key] = s
	}
	return out, nil
}

// SealParams seals a server's secret parameters as one JSON blob; nil for none.
func SealParams(v *vault.Vault, serverID int64, secret map[string]string) []byte {
	if len(secret) == 0 {
		return nil
	}
	b, _ := json.Marshal(secret)
	return v.Seal(b, paramsAAD(serverID))
}

// OpenParams opens a server's secret parameters; empty for none.
func OpenParams(v *vault.Vault, serverID int64, blob []byte) (map[string]string, error) {
	out := map[string]string{}
	if len(blob) == 0 {
		return out, nil
	}
	b, err := v.Open(blob, paramsAAD(serverID))
	if err != nil {
		return nil, fmt.Errorf("open parameters of server %d: %w", serverID, err)
	}
	return out, json.Unmarshal(b, &out)
}

// SealDeploymentParams and OpenDeploymentParams do the same for a deployment.
func SealDeploymentParams(v *vault.Vault, deploymentID int64, secret map[string]string) []byte {
	if len(secret) == 0 {
		return nil
	}
	b, _ := json.Marshal(secret)
	return v.Seal(b, deploymentParamsAAD(deploymentID))
}

func OpenDeploymentParams(v *vault.Vault, deploymentID int64, blob []byte) (map[string]string, error) {
	out := map[string]string{}
	if len(blob) == 0 {
		return out, nil
	}
	b, err := v.Open(blob, deploymentParamsAAD(deploymentID))
	if err != nil {
		return nil, fmt.Errorf("open parameters of deployment %d: %w", deploymentID, err)
	}
	return out, json.Unmarshal(b, &out)
}

// SealFile and OpenFile seal the content of a deployed file.
func SealFile(v *vault.Vault, deploymentID int64, path string, content []byte) []byte {
	return v.Seal(content, fileAAD(deploymentID, path))
}

func OpenFile(v *vault.Vault, deploymentID int64, path string, blob []byte) ([]byte, error) {
	b, err := v.Open(blob, fileAAD(deploymentID, path))
	if err != nil {
		return nil, fmt.Errorf("open file %s of deployment %d: %w", path, deploymentID, err)
	}
	return b, nil
}

type endpointSecret struct {
	Credential string            `json:"credential"`
	Params     map[string]string `json:"params"`
}

// SealEndpoint seals an endpoint's credential and type-specific parameters.
func SealEndpoint(v *vault.Vault, serverID int64, key, credential string, params map[string]string) []byte {
	b, _ := json.Marshal(endpointSecret{Credential: credential, Params: params})
	return v.Seal(b, endpointAAD(serverID, key))
}

// OpenEndpoint opens a stored endpoint into one with its credential. Callers
// must not log it.
func OpenEndpoint(v *vault.Vault, r store.EndpointRow) (endpoint.Endpoint, error) {
	b, err := v.Open(r.Secret, endpointAAD(r.ServerID, r.Key))
	if err != nil {
		return endpoint.Endpoint{}, fmt.Errorf("open endpoint %q of server %d: %w", r.Key, r.ServerID, err)
	}
	var s endpointSecret
	if err := json.Unmarshal(b, &s); err != nil {
		return endpoint.Endpoint{}, err
	}
	return endpoint.Endpoint{Key: r.Key, Type: r.Type, Host: r.Host, Port: r.Port, Credential: s.Credential, Params: s.Params, DisplayName: r.DisplayName}, nil
}

// OpenEndpoints opens all of a server's endpoints.
func OpenEndpoints(v *vault.Vault, rows []store.EndpointRow) ([]endpoint.Endpoint, error) {
	out := make([]endpoint.Endpoint, 0, len(rows))
	for _, r := range rows {
		e, err := OpenEndpoint(v, r)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}
