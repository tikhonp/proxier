// Package manifest is manifest.yaml of a template version
// (docs/modules/servers.md#manifest): its types, parsing with line numbers and
// the schema checks. Nothing here renders or touches a server.
package manifest

import "io/fs"

// Name is the file every template version has.
const Name = "manifest.yaml"

// Manifest is a parsed manifest.yaml. Strings in steps, endpoints and checks
// are still templates.
type Manifest struct {
	Name, Description string
	Requires          Requires
	HasRequires       bool
	Dir               string
	Ports             []Port // "80/tcp"
	Parameters        []Parameter
	Generated         []Generated
	Files             []File
	Steps             Steps
	Endpoints         []Endpoint
	Checks            []Check
	ProxyTest         *ProxyTest
}

type Requires struct{ OS, Arch []string } // "debian-12", "ubuntu-22.04"; "amd64", "arm64"

type Port struct {
	Number int
	Proto  string
	Line   int
}

type Parameter struct {
	Key, Label, Type, Help string // type: string email int bool choice text
	Required, Secret       bool
	Default, Sample        *string
	Options                []string
	Line                   int
}

type Generated struct {
	Key, Kind, Prefix string // kind: uuid hex base64 password
	Length, Bytes     int
	Rotate            bool
	Line              int
}

type File struct {
	Path     string
	Validate string // "" or xray compose nginx json yaml shell
	Mode     fs.FileMode
	Line     int
}

type Steps struct{ Install, Redeploy, Uninstall []Step }

// Step is one entry of a step list. For run, Args holds "run" (the command)
// and optionally "timeout"; for the others the step's own fields.
type Step struct {
	Kind string // base-bootstrap upload-files run compose-up compose-down wait-http
	Args map[string]any
	Pos  string // "steps.install[2]" for messages
	Line int
}

type Endpoint struct {
	Key, Type, Host, Port, Credential string // unrendered
	Params                            map[string]string
	Line                              int
}

// Check is one self-check.
type Check struct {
	Kind string // compose-running http-local cert-expiry disk-free run
	Args map[string]any
	Pos  string // "checks[1]"
	Line int
}

type ProxyTest struct {
	URL  string
	Line int
}

// Known kinds. Endpoint types are the ones servers/endpoint implements; 1c
// adds to this list when it adds a type.
var (
	StepKinds         = []string{"base-bootstrap", "upload-files", "run", "compose-up", "compose-down", "wait-http"}
	CheckKinds        = []string{"compose-running", "http-local", "cert-expiry", "disk-free", "run"}
	GeneratedKinds    = []string{"uuid", "hex", "base64", "password"}
	ParameterTypes    = []string{"string", "email", "int", "bool", "choice", "text"}
	Validators        = []string{"xray", "compose", "nginx", "json", "yaml", "shell"}
	EndpointTypes     = []string{"vless-xhttp-tls"}
	stepArgs          = map[string][]string{"base-bootstrap": nil, "upload-files": nil, "run": {"run", "timeout"}, "compose-up": {"pull"}, "compose-down": {"volumes"}, "wait-http": {"url", "resolve", "status", "timeout"}}
	checkArgs         = map[string][]string{"compose-running": nil, "http-local": {"url", "status"}, "cert-expiry": {"file"}, "disk-free": nil, "run": {"run", "timeout"}}
	requiredStepArgs  = map[string][]string{"run": {"run"}, "wait-http": {"url"}}
	requiredCheckArgs = map[string][]string{"http-local": {"url"}, "cert-expiry": {"file"}, "run": {"run"}}
)
