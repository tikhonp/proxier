package validate

import (
	"context"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/compose-spec/compose-go/v2/dotenv"
	"github.com/compose-spec/compose-go/v2/interpolation"
	"github.com/compose-spec/compose-go/v2/loader"
	"github.com/compose-spec/compose-go/v2/template"
	"github.com/compose-spec/compose-go/v2/types"
	"github.com/tikhonp/proxier/internal/modules/servers/finding"
	"github.com/tikhonp/proxier/internal/modules/servers/render"
)

// composeCache parses the version's compose file once: the compose validator
// reports on it and the nginx validator reads its services.
type composeCache struct {
	done    bool
	project *types.Project
	path    string
	fs      []finding.Finding
}

var composeErrLine = regexp.MustCompile(`line (\d+)`)

func (c *composeCache) load(f render.RenderedFile, files map[string]render.RenderedFile) {
	if c.done {
		return
	}
	c.done, c.path = true, f.Path
	// The rendered .env is the interpolation environment, as docker compose
	// reads it next to the file.
	env := map[string]string{}
	if e, ok := files[".env"]; ok {
		if m, err := dotenv.UnmarshalBytesWithLookup(e.Content, nil); err == nil {
			env = m
		}
	}
	details := types.ConfigDetails{
		WorkingDir:  "/",
		ConfigFiles: []types.ConfigFile{{Filename: f.Path, Content: f.Content}},
		Environment: env,
	}
	p, err := loader.LoadWithContext(context.Background(), details, func(o *loader.Options) {
		o.SetProjectName("template", true)
		o.SkipResolveEnvironment = true
		o.SkipResolveLabels = true
		o.SkipInclude = true
		o.SkipExtends = true
		o.ResolvePaths = false
		// Variables a template leaves unset are not worth a log line per validation.
		o.Interpolate = &interpolation.Options{
			LookupValue: func(k string) (string, bool) { v, ok := env[k]; return v, ok },
			Substitute: func(t string, m template.Mapping) (string, error) {
				return template.SubstituteWithOptions(t, m, template.WithoutLogging)
			},
		}
	})
	if err != nil {
		line := 0
		if m := composeErrLine.FindStringSubmatch(err.Error()); m != nil {
			line, _ = strconv.Atoi(m[1])
		}
		c.fs = append(c.fs, errorf("compose", f, line, "%v", err))
		return
	}
	c.project = p
	if len(p.Services) == 0 {
		c.fs = append(c.fs, errorf("compose", f, 0, "the compose file has no services"))
		return
	}
	names := make([]string, 0, len(p.Services))
	for n := range p.Services {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		img := p.Services[n].Image
		if img == "" {
			continue
		}
		if tag, pinned := imageTag(img); !pinned && (tag == "" || tag == "latest") {
			c.fs = append(c.fs, finding.Warnf("compose", f.Path, lineOf(f.Content, img),
				"service %q uses image %s: pin a version tag (%s), or updates arrive unannounced", n, img, tagWord(tag)))
		}
	}
}

func tagWord(tag string) string {
	if tag == "" {
		return "it has none and means latest"
	}
	return "latest moves"
}

// imageTag returns the tag of a reference ("" without one) and whether it is
// pinned by digest.
func imageTag(ref string) (tag string, pinned bool) {
	if strings.Contains(ref, "@") {
		return "", true
	}
	last := ref[strings.LastIndex(ref, "/")+1:]
	if i := strings.LastIndex(last, ":"); i >= 0 {
		return last[i+1:], false
	}
	return "", false
}

func (c *composeCache) findings(f render.RenderedFile, files map[string]render.RenderedFile) []finding.Finding {
	c.load(f, files)
	if c.path != f.Path {
		// A second compose file: nothing is shared with the first.
		other := &composeCache{}
		other.load(f, files)
		return other.fs
	}
	return c.fs
}

// mounts lists where a service of the project mounts the version's file
// path, as (service, container path).
type mount struct {
	Service *types.ServiceConfig
	Target  string
}

func (c *composeCache) mountsOf(path string) []mount {
	if c.project == nil {
		return nil
	}
	var out []mount
	names := make([]string, 0, len(c.project.Services))
	for n := range c.project.Services {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		svc := c.project.Services[n]
		for _, v := range svc.Volumes {
			if v.Type != "bind" || v.Target == "" {
				continue
			}
			src := cleanRel(v.Source)
			switch {
			case src == path:
				out = append(out, mount{&svc, v.Target})
			case src != "" && strings.HasPrefix(path, src+"/"):
				out = append(out, mount{&svc, strings.TrimSuffix(v.Target, "/") + path[len(src):]})
			}
		}
	}
	return out
}

func cleanRel(p string) string {
	p = strings.TrimPrefix(p, "./")
	p = strings.TrimSuffix(p, "/")
	if strings.HasPrefix(p, "/") || p == "." || p == "" {
		return ""
	}
	return p
}
