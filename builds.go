package main

import (
	"fmt"
	"github.com/pkg/errors"
	"github.com/robloxapi/rbxapi/rbxapijson"
	"github.com/robloxapi/rbxapi/rbxapijson/diff"
	"github.com/robloxapi/rbxapiref/fetch"
	"sort"
	"time"
)

type Build struct {
	Config string
	Info   BuildInfo
	API    *rbxapijson.Root
}

type BuildInfo struct {
	Hash    string
	Date    time.Time
	Version fetch.Version
}

func (a BuildInfo) Equal(b BuildInfo) bool {
	if a.Hash != b.Hash {
		return false
	}
	if a.Version != b.Version {
		return false
	}
	if !a.Date.Equal(b.Date) {
		return false
	}
	return true
}

func (m BuildInfo) String() string {
	return fmt.Sprintf("%s; %s; %s", m.Hash, m.Date, m.Version)
}

func FetchBuilds(settings Settings) (builds []Build, err error) {
	client := &fetch.Client{CacheMode: fetch.CacheNone}
	for _, cfg := range settings.UseConfigs {
		client.Config = settings.Configs[cfg]
		bs, err := client.Builds()
		if err != nil {
			return nil, errors.WithMessage(err, "fetch build")
		}
		for _, b := range bs {
			builds = append(builds, Build{Config: cfg, Info: BuildInfo(b)})
		}
	}
	sort.Slice(builds, func(i, j int) bool {
		return builds[i].Info.Date.Before(builds[j].Info.Date)
	})
	return builds, nil
}

func MergeBuilds(settings Settings, cached []Patch, builds []Build) (patches []Patch, err error) {
	client := &fetch.Client{CacheMode: fetch.CacheTemp}
	var latest *Build
loop:
	for _, build := range builds {
		for _, patch := range cached {
			if !build.Info.Equal(patch.Info) {
				// Not relevant; skip.
				continue
			}
			// Current build has a cached version.
			if latest == nil {
				if patch.Prev != nil {
					// Cached build is now the first, but was not originally;
					// actions are stale.
					Log("STALE", patch.Info)
					break
				}
			} else {
				if patch.Prev == nil {
					// Cached build was not originally the first, but now is;
					// actions are stale.
					Log("STALE", patch.Info)
					break
				}
				if !latest.Info.Equal(*patch.Prev) {
					// Latest build does not match previous build; actions are
					// stale.
					Log("STALE", patch.Info)
					break
				}
			}
			// Cached actions are still fresh; set them directly.
			patches = append(patches, patch)
			latest = &Build{Info: patch.Info, Config: patch.Config}
			continue loop
		}
		Log("NEW", build.Info)
		client.Config = settings.Configs[build.Config]
		root, err := client.APIDump(build.Info.Hash)
		if IfErrorf(err, "%s: fetch build %s", build.Config, build.Info.Hash) {
			continue
		}
		build.API = root
		var actions []Action
		if latest == nil {
			// First build; compare with nothing.
			actions = WrapActions((&diff.Diff{Prev: nil, Next: build.API}).Diff())
		} else {
			if latest.API == nil {
				// Previous build was cached; fetch its data to compare with
				// current build.
				client.Config = settings.Configs[latest.Config]
				root, err := client.APIDump(latest.Info.Hash)
				if IfErrorf(err, "%s: fetch build %s", latest.Config, latest.Info.Hash) {
					continue
				}
				latest.API = root
			}
			actions = WrapActions((&diff.Diff{Prev: latest.API, Next: build.API}).Diff())
		}
		patch := Patch{Stale: true, Info: build.Info, Config: build.Config, Actions: actions}
		if latest != nil {
			prev := latest.Info
			patch.Prev = &prev
		}
		patches = append(patches, patch)
		b := build
		latest = &b
	}

	// Ensure that the latest API is present.
	if latest.API == nil {
		client.Config = settings.Configs[latest.Config]
		root, err := client.APIDump(latest.Info.Hash)
		if err != nil {
			return nil, errors.WithMessagef(err, "fetch build %s", latest.Info.Hash)
		}
		latest.API = root
	}

	// Set action indices.
	for i, patch := range patches {
		for j := range patch.Actions {
			patches[i].Actions[j].Index = j
		}
	}

	return patches, nil
}
