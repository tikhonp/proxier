// Package sealed is where the servers module's secrets are sealed and opened,
// each under the AAD that names where it lives (docs/build/README.md,
// "Secrets"): generated values, secret parameters, an endpoint's credential,
// a deployment's secret parameters and deployed file content. Putting every
// AAD here keeps a blob from being opened as another kind of secret.
package sealed

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/tikhonp/proxier/internal/modules/servers/endpoint"
	"github.com/tikhonp/proxier/internal/modules/servers/render"
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

// OpenPending opens the pending (not yet committed) values of a rotation by
// key; keys without one are absent.
func OpenPending(v *vault.Vault, serverID int64, rows []store.GeneratedValue) (map[string]string, error) {
	out := map[string]string{}
	for _, r := range rows {
		if len(r.Pending) == 0 {
			continue
		}
		s, err := v.OpenString(r.Pending, genAAD(serverID, r.Key))
		if err != nil {
			return nil, fmt.Errorf("open pending value %q of server %d: %w", r.Key, serverID, err)
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

// DeploymentSecrets are the secrets a deployment's files were rendered with:
// its secret parameters and the generated values in force then. Keeping them
// lets the Stack tab mask an old deployment's files after a rotation or a
// parameter change replaced the values.
type DeploymentSecrets struct {
	Params, Gen map[string]string
}

type deploymentSecretsJSON struct {
	V      int               `json:"v"`
	Params map[string]string `json:"params,omitempty"`
	Gen    map[string]string `json:"gen,omitempty"`
}

// SealDeploymentSecrets seals a deployment's secrets as one blob; nil for none.
func SealDeploymentSecrets(v *vault.Vault, deploymentID int64, s DeploymentSecrets) []byte {
	if len(s.Params) == 0 && len(s.Gen) == 0 {
		return nil
	}
	b, _ := json.Marshal(deploymentSecretsJSON{V: 2, Params: s.Params, Gen: s.Gen})
	return v.Seal(b, deploymentParamsAAD(deploymentID))
}

// OpenDeploymentSecrets opens them; empty for none. A blob of the first
// format (a flat map of secret parameters) opens as Params.
func OpenDeploymentSecrets(v *vault.Vault, deploymentID int64, blob []byte) (DeploymentSecrets, error) {
	out := DeploymentSecrets{Params: map[string]string{}, Gen: map[string]string{}}
	if len(blob) == 0 {
		return out, nil
	}
	b, err := v.Open(blob, deploymentParamsAAD(deploymentID))
	if err != nil {
		return out, fmt.Errorf("open parameters of deployment %d: %w", deploymentID, err)
	}
	var cur deploymentSecretsJSON
	if err := json.Unmarshal(b, &cur); err == nil && cur.V == 2 {
		for k, val := range cur.Params {
			out.Params[k] = val
		}
		for k, val := range cur.Gen {
			out.Gen[k] = val
		}
		return out, nil
	}
	return out, json.Unmarshal(b, &out.Params)
}

func rolloutParamsAAD(rolloutID int64, position int) string {
	return fmt.Sprintf("rollout:%d:item:%d:params", rolloutID, position)
}

// SealRolloutParams and OpenRolloutParams do the same for the secret
// parameters asked before a rollout started, one blob per item.
func SealRolloutParams(v *vault.Vault, rolloutID int64, position int, secret map[string]string) []byte {
	if len(secret) == 0 {
		return nil
	}
	b, _ := json.Marshal(secret)
	return v.Seal(b, rolloutParamsAAD(rolloutID, position))
}

func OpenRolloutParams(v *vault.Vault, rolloutID int64, position int, blob []byte) (map[string]string, error) {
	out := map[string]string{}
	if len(blob) == 0 {
		return out, nil
	}
	b, err := v.Open(blob, rolloutParamsAAD(rolloutID, position))
	if err != nil {
		return nil, fmt.Errorf("open parameters of rollout %d item %d: %w", rolloutID, position, err)
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

// Endpoints checks the rendered endpoints of a version and gives them the
// display names apps show: flag, locationName and number make the name.
func Endpoints(rendered []render.RenderedEndpoint, flag, locationName string, number int) ([]endpoint.Endpoint, error) {
	if len(rendered) == 0 {
		return nil, errors.New("the template declares no endpoint")
	}
	many := len(rendered) > 1
	out := make([]endpoint.Endpoint, len(rendered))
	for i, e := range rendered {
		t, ok := endpoint.Lookup(e.Type)
		if !ok {
			return nil, fmt.Errorf("endpoint %q has the unknown type %q", e.Key, e.Type)
		}
		if problems := t.Check(e); len(problems) > 0 {
			return nil, fmt.Errorf("endpoint %q: %s", e.Key, strings.Join(problems, "; "))
		}
		out[i] = endpoint.Endpoint{
			Key: e.Key, Type: e.Type, Host: e.Host, Port: e.Port, Credential: e.Credential, Params: e.Params,
			DisplayName: endpoint.DisplayName(flag, locationName, number, e.Key, many),
		}
	}
	return out, nil
}

// SealEndpoints seals endpoints into the rows a server stores, in order.
func SealEndpoints(v *vault.Vault, serverID int64, eps []endpoint.Endpoint) []store.EndpointRow {
	rows := make([]store.EndpointRow, len(eps))
	for i, e := range eps {
		rows[i] = store.EndpointRow{
			ServerID: serverID, Key: e.Key, Type: e.Type, Host: e.Host, Port: e.Port, Position: i, DisplayName: e.DisplayName,
			Secret: SealEndpoint(v, serverID, e.Key, e.Credential, e.Params),
		}
	}
	return rows
}

// EndpointRows is Endpoints followed by SealEndpoints.
func EndpointRows(v *vault.Vault, serverID int64, rendered []render.RenderedEndpoint, flag, locationName string, number int) ([]store.EndpointRow, error) {
	eps, err := Endpoints(rendered, flag, locationName, number)
	if err != nil {
		return nil, err
	}
	return SealEndpoints(v, serverID, eps), nil
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
