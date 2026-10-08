package health

import (
	"fmt"
	"strings"
)

// stackProblem words what a failed self-check found: "certbot container not
// running" for a compose service (the check's message is "certbot: exited"),
// the check's own message for the others.
func stackProblem(s *SelfResult) string {
	if s == nil || len(s.Failures) == 0 {
		return "a check failed"
	}
	var parts []string
	for _, f := range s.Failures {
		if f.Kind == "compose-running" {
			for _, item := range strings.Split(f.Message, ", ") {
				if name, state, ok := strings.Cut(item, ": "); ok && name != "" {
					if state == "running" || state == "starting" || strings.HasPrefix(state, "unhealthy") {
						parts = append(parts, fmt.Sprintf("%s container is %s", name, state))
					} else {
						parts = append(parts, name+" container not running")
					}
					continue
				}
				parts = append(parts, f.Message)
				break
			}
			continue
		}
		parts = append(parts, f.Message)
	}
	return strings.Join(parts, "; ")
}
