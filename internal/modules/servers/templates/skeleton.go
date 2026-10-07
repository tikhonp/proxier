package templates

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Skeleton is the draft of a new template: a manifest that validates (a plain
// nginx container) for the author to replace file by file.
func Skeleton(name, slug, description string) map[string][]byte {
	manifest := fmt.Sprintf(`name: %s
description: %s

requires:
  os: [debian-12, debian-13, ubuntu-22.04, ubuntu-24.04]
  arch: [amd64, arm64]
dir: /opt/proxier/%s     # where the stack lives on the server
ports: [80/tcp]          # opened in the firewall, besides SSH

files:                   # rendered with Go templates, uploaded into dir
  - { path: compose.yaml, validate: compose }

steps:
  install:               # provisioning
    - base-bootstrap
    - upload-files
    - compose-up: { pull: true }
  redeploy:              # redeploy, upgrade, rotation
    - upload-files
    - compose-up
  uninstall:             # retirement with "remove the stack"
    - compose-down: { volumes: true }

checks:                  # the self-check
  - compose-running
  - disk-free
`, quote(name), quote(description), slug)
	compose := `services:
  web:
    image: nginx:stable-alpine
    restart: unless-stopped
    ports:
      - "80:80/tcp"
`
	return map[string][]byte{"manifest.yaml": []byte(manifest), "compose.yaml": []byte(compose)}
}

// quote is a YAML double-quoted scalar (JSON is a subset of it).
func quote(s string) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return string(bytes.TrimSuffix(buf.Bytes(), []byte("\n")))
}
