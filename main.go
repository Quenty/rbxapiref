package main

import (
	"github.com/pkg/errors"
	"github.com/robloxapi/rbxapi/rbxapijson"
	"github.com/robloxapi/rbxapi/rbxapijson/diff"
	"github.com/robloxapi/rbxapiref/fetch"
	"html/template"
	"image/png"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Converts a value into a string. Only handles types found in rbxapi
// structures.
func toString(v interface{}) string {
	switch v := v.(type) {
	case Value:
		return toString(v.V)
	case bool:
		if v {
			return "true"
		}
		return "false"
	case int:
		return strconv.Itoa(v)
	case string:
		return v
	case rbxapijson.Type:
		return v.String()
	case []string:
		return "[" + strings.Join(v, ", ") + "]"
	case rbxapijson.Parameters:
		n := v.GetLength()
		ss := make([]string, n)
		for i := 0; i < n; i++ {
			param := v.GetParameter(i).(rbxapijson.Parameter)
			ss[i] = param.Type.String() + " " + param.Name
			if param.HasDefault {
				ss[i] += " = " + param.Default
			}
		}
		return "(" + strings.Join(ss, ", ") + ")"
	}
	return "<unknown value " + reflect.TypeOf(v).String() + ">"
}

// Generates a list of actions for each member of the element.
func makeSubactions(action Action) []Action {
	if class := action.Class; class != nil {
		actions := make([]Action, len(class.Members))
		for i, member := range class.Members {
			actions[i] = Action{
				Type:  action.GetType(),
				Class: class,
			}
			actions[i].SetMember(member)
		}
		return actions
	} else if enum := action.Enum; enum != nil {
		actions := make([]Action, len(enum.Items))
		for i, item := range enum.Items {
			actions[i] = Action{
				Type:     action.GetType(),
				Enum:     enum,
				EnumItem: item,
			}
		}
		return actions
	}
	return nil
}

// Compiles templates in specified folder as a single template. Templates are
// named as the file name without the extension.
func compileTemplates(dir string, funcs template.FuncMap) (tmpl *template.Template, err error) {
	fis, err := ioutil.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	tmpl = template.New("")
	tmpl.Funcs(funcs)
	for _, fi := range fis {
		base := filepath.Base(fi.Name())
		name := base[:len(base)-len(filepath.Ext(base))]
		b, err := ioutil.ReadFile(filepath.Join(dir, fi.Name()))
		if err != nil {
			return nil, err
		}
		t := tmpl.New(name)
		if _, err = t.Parse(string(b)); err != nil {
			return nil, err
		}
		t.Funcs(funcs)
	}
	return
}

func reflectIndirect(v reflect.Value) (rv reflect.Value, isNil bool) {
	for ; v.Kind() == reflect.Ptr || v.Kind() == reflect.Interface; v = v.Elem() {
		if v.IsNil() {
			return v, true
		}
	}
	return v, false
}

func reflectLength(item interface{}) (int, error) {
	v := reflect.ValueOf(item)
	if !v.IsValid() {
		return 0, errors.New("len of untyped nil")
	}
	v, isNil := reflectIndirect(v)
	if isNil {
		return 0, errors.New("len of nil pointer")
	}
	switch v.Kind() {
	case reflect.Array, reflect.Chan, reflect.Map, reflect.Slice, reflect.String:
		return v.Len(), nil
	case reflect.Int:
		return int(v.Int()), nil
	}
	return 0, errors.Errorf("len of type %s", v.Type())
}

const SettingsFile = "settings.json"

func LoadSettings(settings *Settings) error {
	f, err := os.Open(SettingsFile)
	if err != nil {
		return fmt.Errorf("failed to open settings file: %s", err)
	}
	err = json.NewDecoder(f).Decode(settings)
	f.Close()
	if err != nil {
		return fmt.Errorf("failed to decode settings file: %s", err)
	}
	return nil
}

func main() {
	// Initialize root.
	data := &Data{CurrentYear: time.Now().Year()}

	// Load settings.
	IfFatal(LoadSettings(&data.Settings))

	manifestPath := filepath.Join(
		data.Settings.Output.Root,
		data.Settings.Output.Sub,
		data.Settings.Output.Manifest,
	)

	// Load cache.
	client := &fetch.Client{}
	prevPatches := []Patch{}
	{
		f, err := os.Open(manifestPath)
		if err == nil {
			manifest, err := ReadManifest(f)
			f.Close()
			IfFatal(err, "open manifest")
			prevPatches = manifest.Patches
		}
	}

	// Load builds.
	client.CacheMode = fetch.CacheNone
	builds := []Build{}
	for _, cfg := range data.Settings.UseConfigs {
		client.Config = data.Settings.Configs[cfg]
		bs, err := client.Builds()
		IfFatal(err, "fetch build")
		for _, b := range bs {
			builds = append(builds, Build{Config: cfg, Info: BuildInfo(b)})
		}
	}
	client.CacheMode = fetch.CacheTemp

	sort.Slice(builds, func(i, j int) bool {
		return builds[i].Info.Date.Before(builds[j].Info.Date)
	})

	// Fetch uncached builds.
loop:
	for _, build := range builds {
		for _, patch := range prevPatches {
			if !build.Info.Equal(patch.Info) {
				// Not relevant; skip.
				continue
			}
			// Current build has a cached version.
			if data.Latest == nil {
				if patch.Prev != nil {
					// Cached build is now the first, but was not originally;
					// actions are stale.
					Log("STALE ", patch.Info)
					break
				}
			} else {
				if patch.Prev == nil {
					// Cached build was not originally the first, but now is;
					// actions are stale.
					Log("STALE ", patch.Info)
					break
				}
				if !data.Latest.Info.Equal(*patch.Prev) {
					// Latest build does not match previous build; actions are
					// stale.
					Log("STALE ", patch.Info)
					break
				}
			}
			// Cached actions are still fresh; set them directly.
			data.Patches = append(data.Patches, patch)
			data.Latest = &Build{Info: patch.Info, Config: patch.Config}
			continue loop
		}
		Log("NEW ", build.Info)
		client.Config = data.Settings.Configs[build.Config]
		root, err := client.APIDump(build.Info.Hash)
		if IfErrorf(err, "%s: fetch build %s", build.Config, build.Info.Hash) {
			continue
		}
		build.API = root
		var actions []Action
		if data.Latest == nil {
			// First build; compare with nothing.
			actions = WrapActions((&diff.Diff{Prev: nil, Next: build.API}).Diff())
		} else {
			if data.Latest.API == nil {
				// Previous build was cached; fetch its data to compare with
				// current build.
				client.Config = data.Settings.Configs[data.Latest.Config]
				root, err := client.APIDump(data.Latest.Info.Hash)
				if IfErrorf(err, "%s: fetch build %s", data.Latest.Config, data.Latest.Info.Hash) {
					continue
				}
				data.Latest.API = root
			}
			actions = WrapActions((&diff.Diff{Prev: data.Latest.API, Next: build.API}).Diff())
		}
		patch := Patch{Stale: true, Info: build.Info, Config: build.Config, Actions: actions}
		if data.Latest != nil {
			prev := data.Latest.Info
			patch.Prev = &prev
		}
		data.Patches = append(data.Patches, patch)
		b := build
		data.Latest = &b
	}
	// Ensure that the latest API is present.
	if data.Latest.API == nil {
		client.Config = data.Settings.Configs[data.Latest.Config]
		root, err := client.APIDump(data.Latest.Info.Hash)
		IfFatalf(err, "fetch build %s", data.Latest.Info.Hash)
		data.Latest.API = root
	}

	for i, patch := range data.Patches {
		for j := range patch.Actions {
			data.Patches[i].Actions[j].Index = j
		}
	}

	// Fetch ReflectionMetadata.
	{
		rmd, err := client.ReflectionMetadata(data.Latest.Info.Hash)
		IfFatal(err, "fetch metadata ", data.Latest.Info.Hash)
		data.GenerateMetadata(rmd)
	}

	// Fetch explorer icons.
	{
		icon, err := client.ExplorerIcons(data.Latest.Info.Hash)
		IfFatalf(err, "%s: fetch icons %s", data.Latest.Info.Hash)
		IfFatal(os.MkdirAll(data.FilePath("resource"), 0666), "make resource directory")
		f, err := os.Create(data.FilePath("resource", "icon-explorer.png"))
		IfFatal(err, "create icons file")
		err = png.Encode(f, icon)
		f.Close()
		IfFatal(err, "encode icons file")
	}

	data.GenerateUpdates()
	data.Entities = GenerateEntities(data)
	data.GenerateTree()

	// Compile templates.
	var err error
	data.Templates, err = compileTemplates(data.Settings.Input.Templates, template.FuncMap{
		"embed": func(resource string) (interface{}, error) {
			b, err := ioutil.ReadFile(filepath.Join(data.Settings.Input.Resources, resource))
			switch filepath.Ext(resource) {
			case ".css":
				return template.CSS(b), err
			case ".js":
				return template.JS(b), err
			case ".html", ".svg":
				return template.HTML(b), err
			}
			return string(b), err
		},
		"execute": data.ExecuteTemplate,
		"filter": func(filter string, list interface{}) interface{} {
			switch list := list.(type) {
			case []*ClassEntity:
				var filtered []*ClassEntity
				switch filter {
				case "added":
					for _, entity := range list {
						if !entity.Removed {
							filtered = append(filtered, entity)
						}
					}
					return filtered
				case "removed":
					for _, entity := range list {
						if entity.Removed {
							filtered = append(filtered, entity)
						}
					}
					return filtered
				}
			case []*MemberEntity:
				var filtered []*MemberEntity
				switch filter {
				case "added":
					for _, entity := range list {
						if !entity.Removed {
							filtered = append(filtered, entity)
						}
					}
					return filtered
				case "removed":
					for _, entity := range list {
						if entity.Removed {
							filtered = append(filtered, entity)
						}
					}
					return filtered
				case "implicit added":
					for _, entity := range list {
						if !entity.Removed && !entity.Parent.Removed {
							filtered = append(filtered, entity)
						}
					}
					return filtered
				case "implicit removed":
					for _, entity := range list {
						if entity.Removed || entity.Parent.Removed {
							filtered = append(filtered, entity)
						}
					}
					return filtered
				}
			case []*EnumEntity:
				var filtered []*EnumEntity
				switch filter {
				case "added":
					for _, entity := range list {
						if !entity.Removed {
							filtered = append(filtered, entity)
						}
					}
					return filtered
				case "removed":
					for _, entity := range list {
						if entity.Removed {
							filtered = append(filtered, entity)
						}
					}
					return filtered
				}
			case []*EnumItemEntity:
				var filtered []*EnumItemEntity
				switch filter {
				case "implicit added":
					for _, entity := range list {
						if !entity.Removed && !entity.Parent.Removed {
							filtered = append(filtered, entity)
						}
					}
					return filtered
				case "implicit removed":
					for _, entity := range list {
						if entity.Removed || entity.Parent.Removed {
							filtered = append(filtered, entity)
						}
					}
					return filtered
				case "added":
					for _, entity := range list {
						if !entity.Removed {
							filtered = append(filtered, entity)
						}
					}
					return filtered
				case "removed":
					for _, entity := range list {
						if entity.Removed {
							filtered = append(filtered, entity)
						}
					}
					return filtered
				}
			case []*TypeEntity:
				var filtered []*TypeEntity
				switch filter {
				case "added":
					for _, entity := range list {
						if !entity.Removed {
							filtered = append(filtered, entity)
						}
					}
					return filtered
				case "removed":
					for _, entity := range list {
						if entity.Removed {
							filtered = append(filtered, entity)
						}
					}
					return filtered
				}
			case []ElementTyper:
				var filtered []ElementTyper
				switch filter {
				case "class":
					for _, entity := range list {
						if entity.ElementType().Category == "Class" && !entity.IsRemoved() {
							filtered = append(filtered, entity)
						}
					}
					return filtered
				case "enum":
					for _, entity := range list {
						if entity.ElementType().Category == "Enum" && !entity.IsRemoved() {
							filtered = append(filtered, entity)
						}
					}
					return filtered
				case "type":
					for _, entity := range list {
						if cat := entity.ElementType().Category; cat != "Class" && cat != "Enum" && !entity.IsRemoved() {
							filtered = append(filtered, entity)
						}
					}
					return filtered
				}
			}
			return list
		},
		"history": func(entity interface{}) template.HTML {
			var patches []Patch
			switch entity := entity.(type) {
			case *ClassEntity:
				patches = entity.Patches
			case *MemberEntity:
				patches = entity.Patches
			case *EnumEntity:
				patches = entity.Patches
			case *EnumItemEntity:
				patches = entity.Patches
			}
			var list []string
			for _, patch := range patches {
				if patch.Info.Equal(data.Patches[0].Info) {
					continue
				}
				var s []string
				for _, action := range patch.Actions {
					s = append(s,
						"<a class=\"history-", strings.ToLower(action.Type.String()), "\" title=\"",
						PatchTypeString(action.Type, "ed"), " on ", patch.Info.Date.Format("2006-01-02 15:04:05"), "&#10;",
						"v", patch.Info.Version.String(), "&#10;",
						patch.Info.Hash,
						"\" href=\"",
						data.FileLink("updates", strconv.Itoa(patch.Info.Date.Year())), "#", patch.Info.Hash, "-", strconv.Itoa(action.Index),
						"\">",
						strconv.Itoa(patch.Info.Version.Minor),
						"</a>",
					)
					list = append(list, strings.Join(s, ""))
				}
			}
			return template.HTML("<span class=\"history\">" + strings.Join(list, "\n") + "</span>")
		},
		"icon": data.Icon,
		"istype": func(v interface{}, t string) bool {
			if v == nil {
				return "nil" == t
			}
			return reflect.TypeOf(v).String() == t
		},
		"link": func(linkType string, args ...interface{}) string {
			sargs := make([]string, len(args))
			for i, arg := range args {
				switch arg := arg.(type) {
				case int:
					sargs[i] = strconv.Itoa(arg)
				default:
					sargs[i] = arg.(string)
				}
			}
			return data.FileLink(linkType, sargs...)
		},
		"quantity": func(i interface{}, singular, plural string) string {
			v, err := reflectLength(i)
			if err != nil || v == 1 {
				return singular
			}
			return plural
		},
		"subactions": makeSubactions,
		"tostring":   toString,
		"type": func(v interface{}) string {
			return reflect.TypeOf(v).String()
		},
	})
	IfFatal(err, "open template")

	// Generate pages.
	{
		pageGenerators := []PageGenerator{
			GeneratePageIndex,
			GeneratePageAbout,
			GeneratePageUpdates,
			GeneratePageClass,
			GeneratePageEnum,
			GeneratePageType,
		}

		var pages []Page
		for _, generator := range pageGenerators {
			pages = append(pages, generator(data)...)
		}

		// Ensure directories exist.
		dirs := map[string]struct{}{}
		for _, page := range pages {
			dir := filepath.Dir(page.File)
			if _, ok := dirs[dir]; ok {
				continue
			}
			IfFatal(os.MkdirAll(dir, 0666), "make directory")
			dirs[dir] = struct{}{}
		}

		// Copy resources.
		IfFatal(os.MkdirAll(data.FilePath("resource"), 0666), "make directory")
		resources := map[string]struct{}{
			"icon-objectbrowser.png": struct{}{},
			"main.css":               struct{}{},
			"search.js":              struct{}{},
		}
		for _, page := range pages {
			for _, res := range page.Styles {
				if !res.Embed {
					resources[res.Name] = struct{}{}
				}
			}
			for _, res := range page.Scripts {
				if !res.Embed {
					resources[res.Name] = struct{}{}
				}
			}
		}
		for res := range resources {
			src, err := os.Open(filepath.Join(data.Settings.Input.Resources, res))
			IfFatal(err, "open resource")
			dst, err := os.Create(data.FilePath("resource", res))
			if err != nil {
				src.Close()
				IfFatal(err, "create resource")
			}
			_, err = io.Copy(dst, src)
			dst.Close()
			src.Close()
			IfFatal(err, "write resource")
		}

		// Generate pages.
		var mainPage struct {
			Data *Data
			Page *Page
		}
		mainPage.Data = data
		for _, page := range pages {
			file, err := os.Create(page.File)
			IfFatal(err, "create file")
			if page.Data == nil {
				page.Data = data
			}
			mainPage.Page = &page
			err = data.Templates.ExecuteTemplate(file, "main", mainPage)
			file.Close()
			IfFatal(err, "generate page")
		}
	}

	// Generate search database.
	{
		f, err := os.Create(data.FilePath("search"))
		IfFatal(err, "create search database file")
		db := dbWriter{data: data, w: f}
		db.GenerateDatabase()
		f.Close()
		IfFatal(db.err, "generate search database")
	}

	// Save cache.
	{
		f, err := os.Create(manifestPath)
		IfFatal(err, "create manifest")
		err = WriteManifest(f, &Manifest{data.Patches})
		f.Close()
		IfFatal(err, "encode manifest")
	}
}
