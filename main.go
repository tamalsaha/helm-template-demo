/*
Copyright AppsCode Inc. and Contributors

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/pkg/errors"
	flag "github.com/spf13/pflag"
	ha "helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/releaseutil"
	"k8s.io/klog/v2"
	"kubepack.dev/kubepack/pkg/lib"
	"kubepack.dev/lib-helm/pkg/action"
	"kubepack.dev/lib-helm/pkg/values"
)

var (
	url     = "https://charts.appscode.com/stable/"
	name    = "kube-ui-server"
	version = "v2022.04.04"

	// url     = "https://kubernetes-charts.storage.googleapis.com"
	// name    = "wordpress"
	// version = "8.1.1"

	skipTests bool
	showFiles []string = []string{"templates/deployment.yaml"}
)

func debug(format string, v ...interface{}) {
	format = fmt.Sprintf("[debug] %s\n", format)
	_ = log.Output(2, fmt.Sprintf(format, v...))
}

func warning(format string, v ...interface{}) {
	format = fmt.Sprintf("WARNING: %s\n", format)
	fmt.Fprintf(os.Stderr, format, v...)
}

func m2(opts *action.InstallOptions) (*release.Release, error) {
	cfg := new(ha.Configuration)
	// TODO: Use secret driver for which namespace?
	err := cfg.Init(nil, opts.Namespace, "secret", debug)
	if err != nil {
		return nil, err
	}
	cfg.Capabilities = chartutil.DefaultCapabilities

	client := ha.NewInstall(cfg)
	var extraAPIs []string
	client.DryRun = opts.DryRun
	client.ReleaseName = opts.ReleaseName
	client.Namespace = opts.Namespace
	client.Replace = opts.Replace // Skip the name check
	client.ClientOnly = opts.ClientOnly
	client.APIVersions = chartutil.VersionSet(extraAPIs)
	client.Version = opts.Version
	client.DisableHooks = opts.DisableHooks
	client.Wait = opts.Wait
	client.Timeout = opts.Timeout
	client.Description = opts.Description
	client.Atomic = opts.Atomic
	client.SkipCRDs = opts.SkipCRDs
	client.SubNotes = opts.SubNotes
	client.DisableOpenAPIValidation = opts.DisableOpenAPIValidation
	client.IncludeCRDs = opts.IncludeCRDs
	client.CreateNamespace = opts.CreateNamespace

	// Check chart dependencies to make sure all are present in /charts
	chrt, err := lib.DefaultRegistry.GetChart(opts.ChartURL, opts.ChartName, opts.Version)
	if err != nil {
		return nil, err
	}
	if err := checkIfInstallable(chrt.Chart); err != nil {
		return nil, err
	}

	if chrt.Metadata.Deprecated {
		warning("This chart is deprecated")
	}

	if req := chrt.Metadata.Dependencies; req != nil {
		// If CheckDependencies returns an error, we have unfulfilled dependencies.
		// As of Helm 2.4.0, this is treated as a stopping condition:
		// https://github.com/helm/helm/issues/2209
		if err := ha.CheckDependencies(chrt.Chart, req); err != nil {
			err = errors.Wrap(err, "An error occurred while checking for chart dependencies. You may need to run `helm dependency build` to fetch missing dependencies")
			if err != nil {
				return nil, err
			}
		}
	}

	client.Namespace = opts.Namespace

	vals, err := opts.Values.MergeValues(chrt.Chart)
	if err != nil {
		return nil, err
	}
	// chartutil.CoalesceValues(chrt, chrtVals) will use vals to render templates
	chrt.Chart.Values = map[string]interface{}{}

	return client.Run(chrt.Chart, vals)
}

func main() {
	flag.StringVar(&url, "url", url, "Chart repo url")
	flag.StringVar(&name, "name", name, "Name of bundle")
	flag.StringVar(&version, "version", version, "Version of bundle")
	flag.Parse()

	namespace := "default"
	opts := &action.InstallOptions{
		ChartURL:  url,
		ChartName: name,
		Version:   version,
		Values: values.Options{
			ValuesFile:  "",
			ValuesPatch: nil,
		},
		ClientOnly:   true,
		DryRun:       true,
		DisableHooks: false,
		Replace:      true, // Skip the name check
		Wait:         false,
		Devel:        false,
		Timeout:      0,
		Namespace:    namespace,
		ReleaseName:  "release-name",
		Atomic:       false,
		IncludeCRDs:  false, //
		SkipCRDs:     false, //
	}

	rel, err := m2(opts)
	if err != nil {
		klog.Fatal(err)
	}

	out := os.Stdout
	// We ignore a potential error here because, when the --debug flag was specified,
	// we always want to print the YAML, even if it is not valid. The error is still returned afterwards.
	if rel != nil {
		var manifests bytes.Buffer
		fmt.Fprintln(&manifests, strings.TrimSpace(rel.Manifest))
		if !opts.DisableHooks {
			for _, m := range rel.Hooks {
				if skipTests && isTestHook(m) {
					continue
				}
				fmt.Fprintf(&manifests, "---\n# Source: %s\n%s\n", m.Path, m.Manifest)
			}
		}

		// if we have a list of files to render, then check that each of the
		// provided files exists in the chart.
		if len(showFiles) > 0 {
			// This is necessary to ensure consistent manifest ordering when using --show-only
			// with globs or directory names.
			splitManifests := releaseutil.SplitManifests(manifests.String())
			manifestsKeys := make([]string, 0, len(splitManifests))
			for k := range splitManifests {
				manifestsKeys = append(manifestsKeys, k)
			}
			sort.Sort(releaseutil.BySplitManifestsOrder(manifestsKeys))

			manifestNameRegex := regexp.MustCompile("# Source: [^/]+/(.+)")
			var manifestsToRender []string
			for _, f := range showFiles {
				missing := true
				// Use linux-style filepath separators to unify user's input path
				f = filepath.ToSlash(f)
				for _, manifestKey := range manifestsKeys {
					manifest := splitManifests[manifestKey]
					submatch := manifestNameRegex.FindStringSubmatch(manifest)
					if len(submatch) == 0 {
						continue
					}
					manifestName := submatch[1]
					// manifest.Name is rendered using linux-style filepath separators on Windows as
					// well as macOS/linux.
					manifestPathSplit := strings.Split(manifestName, "/")
					// manifest.Path is connected using linux-style filepath separators on Windows as
					// well as macOS/linux
					manifestPath := strings.Join(manifestPathSplit, "/")

					// if the filepath provided matches a manifest path in the
					// chart, render that manifest
					if matched, _ := filepath.Match(f, manifestPath); !matched {
						continue
					}
					manifestsToRender = append(manifestsToRender, manifest)
					missing = false
				}
				if missing {
					klog.Errorf("could not find template %s in chart", f)
					return //
				}
			}
			for _, m := range manifestsToRender {
				fmt.Fprintf(out, "---\n%s\n", m)
			}
		} else {
			fmt.Fprintf(out, "%s", manifests.String())
		}
	}
}

// checkIfInstallable validates if a chart can be installed
//
// Application chart type is only installable
func checkIfInstallable(ch *chart.Chart) error {
	switch ch.Metadata.Type {
	case "", "application":
		return nil
	}
	return errors.Errorf("%s charts are not installable", ch.Metadata.Type)
}

func isTestHook(h *release.Hook) bool {
	for _, e := range h.Events {
		if e == release.HookTest {
			return true
		}
	}
	return false
}
