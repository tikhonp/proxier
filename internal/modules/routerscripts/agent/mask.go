package agent

import (
	"errors"
	"fmt"

	"github.com/tikhonp/proxier/internal/modules/routerscripts/params"
)

// Masked is what the agent reads in place of a hidden default. It is a valid
// literal both bare and quoted, so the masked script still parses the same.
const Masked = "MASKED-BY-PROXIER"

// ErrMaskLeft: a parameter holds Masked but the saved draft has no hidden
// default of that name to put back.
var ErrMaskLeft = errors.New("masked value with nothing to restore")

// Hidden reports whether p's default is kept from the agent: an @secret one,
// and the subscription link's URL, which holds the link's token.
func Hidden(p params.Param) bool {
	return p.Default != "" && (p.Annotations.Secret || p.Annotations.Fill == params.FillLink)
}

// maskBody is body as the agent reads it: every hidden default is Masked.
func maskBody(body string) string {
	vals := map[string]string{}
	for _, p := range params.Parse([]byte(body)).Params {
		if Hidden(p) {
			vals[p.Name] = Masked
		}
	}
	if len(vals) == 0 {
		return body
	}
	out, err := params.Fill([]byte(body), vals)
	if err != nil {
		// Masked is a valid bare and quoted literal, so Fill can't refuse it.
		panic(fmt.Sprintf("agent: masking: %v", err))
	}
	return string(out)
}

// unmask puts the hidden defaults of stored back into a body the agent
// wrote: every parameter still holding Masked gets the stored default of the
// same name. A Masked value with no hidden default of that name in stored is
// ErrMaskLeft, so the mask never becomes a real value.
func unmask(posted, stored string) (string, error) {
	saved := params.Parse([]byte(stored))
	vals := map[string]string{}
	for _, p := range params.Parse([]byte(posted)).Params {
		if p.Default != Masked {
			continue
		}
		orig, ok := saved.Param(p.Name)
		if !ok || !Hidden(orig) {
			return "", fmt.Errorf("%w: %s", ErrMaskLeft, p.Name)
		}
		vals[p.Name] = orig.Default
	}
	if len(vals) == 0 {
		return posted, nil
	}
	out, err := params.Fill([]byte(posted), vals)
	if err != nil {
		// A secret turned into a bare word can't hold the stored value.
		return "", fmt.Errorf("%w: %v", ErrMaskLeft, err)
	}
	return string(out), nil
}
