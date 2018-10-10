package main

import (
	"bytes"
	"fmt"
	"github.com/robloxapi/rbxapi"
	"github.com/robloxapi/rbxapi/rbxapijson"
	"github.com/robloxapi/rbxapiref/fetch"
	"github.com/robloxapi/rbxfile"
	"html/template"
	"net/url"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	ClassPath           = "class"
	EnumPath            = "enum"
	TypePath            = "type"
	FileExt             = ".html"
	MemberAnchorPrefix  = "member-"
	SectionAnchorPrefix = "section-"
)

type Data struct {
	Settings    Settings
	CurrentYear int

	Patches  []Patch
	Latest   *Build
	Metadata ReflectionMetadata

	Entities  *Entities
	TreeRoots []*ClassEntity

	Templates *template.Template
}

type Build struct {
	Config string
	Info   BuildInfo
	API    *rbxapijson.Root
}

type Patch struct {
	Stale   bool       `json:"-"`
	Prev    *BuildInfo `json:",omitempty"`
	Info    BuildInfo
	Config  string
	Actions []Action
}

// Escape once to escape the file name, then again to escape the URL.
func doubleEscape(s string) string {
	return url.PathEscape(url.PathEscape(s))
}

// FileLink generates a URL, relative to an arbitrary host.
func (data *Data) FileLink(linkType string, args ...string) (s string) {
retry:
	switch strings.ToLower(linkType) {
	case "index":
		s = "index" + FileExt
	case "resource":
		s = path.Join(data.Settings.Output.Resources, path.Join(args...))
	case "updates":
		if len(args) > 0 {
			s = path.Join("updates", doubleEscape(args[0])+FileExt)
		} else {
			s = "updates" + FileExt
		}
	case "class":
		s = path.Join(ClassPath, doubleEscape(args[0])+FileExt)
	case "member":
		if len(args) == 1 {
			return (&url.URL{Fragment: MemberAnchorPrefix + args[0]}).String()
		} else if len(args) == 2 {
			s = path.Join(ClassPath, doubleEscape(args[0])+FileExt) +
				(&url.URL{Fragment: MemberAnchorPrefix + args[1]}).String()
		}
	case "enum":
		s = path.Join(EnumPath, doubleEscape(args[0])+FileExt)
	case "enumitem":
		if len(args) == 1 {
			return (&url.URL{Fragment: MemberAnchorPrefix + args[0]}).String()
		} else if len(args) == 2 {
			s = path.Join(EnumPath, doubleEscape(args[0])+FileExt) +
				(&url.URL{Fragment: MemberAnchorPrefix + args[1]}).String()
		}
	case "type":
		switch strings.ToLower(args[0]) {
		case "class", "enum":
			a := make([]string, 2)
			linkType, a[0] = args[0], args[1]
			args = a
			goto retry
		}
		s = path.Join(TypePath, doubleEscape(args[1])+FileExt)
	case "about":
		s = "about" + FileExt
	case "repository":
		return "https://github.com/robloxapi/rbxapiref"
	case "issues":
		return "https://github.com/robloxapi/rbxapiref/issues"
	case "search":
		s = "search.db"
	}
	s = path.Join("/", data.Settings.Output.Sub, s)
	return s
}

// FilePath generates an absolute path located in the Output. On a web server
// serving static files, the returned path is meant to point to the same file
// as the file pointed to by the URL generated by FileLink.
func (data *Data) FilePath(typ string, args ...string) string {
	return data.PathFromLink(data.FileLink(typ, args...))
}

// LinkFromPath transforms a path into a link, if possible.
func (data *Data) LinkFromPath(p string) string {
	if l, err := filepath.Rel(data.Settings.Output.Root, p); err == nil {
		return l
	}
	return p
}

// PathFrom link transforms a link into a path, if possible.
func (data *Data) PathFromLink(l string) string {
	l, _ = url.PathUnescape(l)
	return filepath.Join(data.Settings.Output.Root, l)
}

const IconSize = 16

var memberIconIndex = map[string]int{
	"Property": 6,
	"Function": 4,
	"Event":    11,
	"Callback": 16,
}

func (data *Data) Icon(v ...interface{}) template.HTML {
	if len(v) == 0 {
		return ""
	}
	var class string
	var title string
	var index int
retry:
	switch value := v[0].(type) {
	case string:
		switch strings.ToLower(value) {
		case "class":
			class = "class-icon"
			title = "Class"
			meta, ok := data.Metadata.Classes[v[1].(string)]
			if !ok {
				goto finish
			}
			index = meta.ExplorerImageIndex
		case "member":
			entity := data.Entities.Members[[2]string{v[1].(string), v[2].(string)}]
			if entity == nil {
				goto finish
			}
			v = []interface{}{entity.Element}
			goto retry
		case "enum":
			class = "enum-icon"
			title = "Enum"
			index = -1
		case "enumitem":
			class = "enum-item-icon"
			title = "EnumItem"
			index = -1
		}
	case *ClassEntity:
		if value.Element == nil {
			goto finish
		}
		v = []interface{}{value.Element}
		goto retry
	case *MemberEntity:
		if value.Element == nil {
			goto finish
		}
		v = []interface{}{value.Element}
		goto retry
	case *EnumEntity:
		if value.Element == nil {
			goto finish
		}
		v = []interface{}{value.Element}
		goto retry
	case *EnumItemEntity:
		if value.Element == nil {
			goto finish
		}
		v = []interface{}{value.Element}
		goto retry
	case *rbxapijson.Class:
		class = "class-icon"
		title = "Class"
		meta, ok := data.Metadata.Classes[value.Name]
		if !ok {
			goto finish
		}
		index = meta.ExplorerImageIndex
	case rbxapi.Member:
		class = "member-icon"
		title = value.GetMemberType()
		index = memberIconIndex[title]
		if len(v) > 1 && v[1].(bool) == false {
			goto finish
		}
		switch v := value.(type) {
		case interface{ GetSecurity() (string, string) }:
			r, w := v.GetSecurity()
			if r == "None" {
				r = ""
			}
			if w == "None" {
				w = ""
			}
			switch {
			case r != "" && w != "":
				title = "Protected " + title
				if r == w {
					title += " (Read/Write: " + r + ")"
				} else {
					title += " (Read: " + r + " / Write: " + w + ")"
				}
				index++
			case r != "":
				title = "Protected " + title + " (Read: " + r + ")"
				index++
			case w != "":
				title = "Protected " + title + " (Write: " + w + ")"
				index++
			default:
			}
		case interface{ GetSecurity() string }:
			s := v.GetSecurity()
			if s != "" && s != "None" {
				title = "Protected " + title + " (" + s + ")"
				index++
			}
		}
	case *rbxapijson.Enum:
		class = "enum-icon"
		title = "Enum"
		index = -1
	case *rbxapijson.EnumItem:
		class = "enum-item-icon"
		title = "EnumItem"
		index = -1
	}
finish:
	var style string
	if index >= 0 {
		style = fmt.Sprintf(` style="--icon-index: %d"`, index)
	}
	const body = `<span class="icon %s" title="%s"%s></span>`
	return template.HTML(fmt.Sprintf(body, template.HTMLEscapeString(class), template.HTMLEscapeString(title), style))
}

func (data *Data) ExecuteTemplate(name string, tdata interface{}) (template.HTML, error) {
	var buf bytes.Buffer
	err := data.Templates.ExecuteTemplate(&buf, name, tdata)
	return template.HTML(buf.String()), err
}

func (data *Data) GenerateTree() {
	for id, eclass := range data.Entities.Classes {
		super := eclass.Element.Superclass
		if !eclass.Removed {
			if s := data.Entities.Classes[super]; s == nil || s.Removed {
				data.TreeRoots = append(data.TreeRoots, eclass)
			}
		}
		for class := data.Entities.Classes[super]; class != nil; class = data.Entities.Classes[super] {
			if !class.Removed {
				eclass.Superclasses = append(eclass.Superclasses, class)
			}
			super = class.Element.Superclass
		}
		for _, sub := range data.Entities.Classes {
			if sub.Element.Superclass == id && !sub.Removed {
				eclass.Subclasses = append(eclass.Subclasses, sub)
			}
		}
		sort.Slice(eclass.Subclasses, func(i, j int) bool {
			return eclass.Subclasses[i].ID < eclass.Subclasses[j].ID
		})
	}
	sort.Slice(data.TreeRoots, func(i, j int) bool {
		return data.TreeRoots[i].ID < data.TreeRoots[j].ID
	})
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

type ReflectionMetadata struct {
	Classes map[string]ClassMetadata
	Enums   map[string]EnumMetadata
}

type ItemMetadata struct {
	Name string
	// Browsable       bool
	// ClassCategory   string
	// Constraint      string
	// Deprecated      bool
	// EditingDisabled bool
	// IsBackend       bool
	// ScriptContext   string
	// UIMaximum       float64
	// UIMinimum       float64
	// UINumTicks      float64
	// Summary         string
}

type ClassMetadata struct {
	ItemMetadata
	ExplorerImageIndex int
	// ExplorerOrder      int
	// Insertable         bool
	// PreferredParent    string
	// PreferredParents   string
}

type EnumMetadata struct {
	ItemMetadata
}

func getMetadataValue(p interface{}, v rbxfile.Value) {
	switch p := p.(type) {
	case *int:
		switch v := v.(type) {
		case rbxfile.ValueInt:
			*p = int(v)
		case rbxfile.ValueString:
			*p, _ = strconv.Atoi(string(v))
		}
	}
}

func (data *Data) GenerateMetadata(rmd *rbxfile.Root) {
	data.Metadata.Classes = make(map[string]ClassMetadata)
	data.Metadata.Enums = make(map[string]EnumMetadata)
	for _, list := range rmd.Instances {
		switch list.ClassName {
		case "ReflectionMetadataClasses":
			for _, class := range list.Children {
				if class.ClassName != "ReflectionMetadataClass" {
					continue
				}
				meta := ClassMetadata{ItemMetadata: ItemMetadata{Name: class.Name()}}
				getMetadataValue(&meta.ExplorerImageIndex, class.Properties["ExplorerImageIndex"])
				data.Metadata.Classes[meta.Name] = meta
			}
		case "ReflectionMetadataEnums":
		}
	}
}
