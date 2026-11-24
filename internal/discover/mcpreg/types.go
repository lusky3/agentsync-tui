// Package mcpreg searches the official MCP Registry
// (https://registry.modelcontextprotocol.io, API v0.1). It decodes a page
// of server records into plain, sanitized types; mapping a record to an
// `agentsync mcp add` spec is a separate step.
package mcpreg

import "encoding/json"

// Server is one registry record, latest version only. Every string has
// passed mask.Sanitize.
type Server struct {
	Name        string
	Description string
	Version     string
	RepoURL     string
	// Status is the registry's lifecycle status ("active", "deprecated",
	// "deleted"), or empty when the record does not say. Non-active records
	// are returned, not filtered, so the UI can mark them.
	Status   string
	Packages []Package
	Remotes  []Remote
}

// Package is a locally run distribution of a server (npm, pypi, oci, ...).
// RegistryType is kept as the registry sent it, known or not.
type Package struct {
	RegistryType string
	// RegistryBaseURL is the registry the package is published to, empty
	// when the record leaves it to the default.
	RegistryBaseURL string
	Identifier      string
	Version         string
	RuntimeHint     string
	// Transport is the transport type ("stdio", "streamable-http", "sse").
	Transport   string
	Env         []EnvVar
	RuntimeArgs []Argument
	PackageArgs []Argument
}

// Argument is a runtime or package argument. Type is "positional" or
// "named". Name is the flag for a named argument and the value hint for a
// positional one. An argument with Required set and no Value is one the
// user must supply. Secret is the registry's isSecret: such an argument is
// never passed as a plain token.
type Argument struct {
	Type     string
	Name     string
	Value    string
	Default  string
	Required bool
	Secret   bool
}

// EnvVar is a declared input: an environment variable, or a URL-template
// or header variable (then Name is the variable's key). Value is an
// environment variable's fixed value, if the record gives one (variables
// never have one).
type EnvVar struct {
	Name        string
	Description string
	Default     string
	Value       string
	Required    bool
	Secret      bool
}

// Remote is a hosted endpoint. Variables are the URL template's declared
// variables, sorted by name.
type Remote struct {
	Type      string
	URL       string
	Headers   []Header
	Variables []EnvVar
}

// Header is a header a remote expects; Variables are those its Value
// template uses, sorted by name.
type Header struct {
	Name      string
	Value     string
	Required  bool
	Secret    bool
	Variables []EnvVar
}

// Page is one page of search results. Next is the cursor for the following
// page, empty on the last page. Skipped counts records on the page that
// could not be read (malformed, or with input names that are not plain
// text or collide) and were left out of Servers.
type Page struct {
	Servers []Server
	Next    string
	Skipped int
}

// The wire types below mirror the parts of the v0.1 response this package
// reads; unknown fields are ignored.

// wirePage is the envelope. Servers stays raw so each record decodes on
// its own; a missing key leaves it nil.
type wirePage struct {
	Servers  json.RawMessage `json:"servers"`
	Metadata struct {
		NextCursor string `json:"nextCursor"`
	} `json:"metadata"`
}

type wireEntry struct {
	Server wireServer `json:"server"`
	Meta   struct {
		Official struct {
			Status string `json:"status"`
		} `json:"io.modelcontextprotocol.registry/official"`
	} `json:"_meta"`
}

type wireServer struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Version     string `json:"version"`
	Repository  struct {
		URL string `json:"url"`
	} `json:"repository"`
	Packages []wirePackage `json:"packages"`
	Remotes  []wireRemote  `json:"remotes"`
}

type wirePackage struct {
	RegistryType    string `json:"registryType"`
	RegistryBaseURL string `json:"registryBaseUrl"`
	Identifier      string `json:"identifier"`
	Version         string `json:"version"`
	RuntimeHint     string `json:"runtimeHint"`
	Transport       struct {
		Type string `json:"type"`
	} `json:"transport"`
	Env         []wireEnv      `json:"environmentVariables"`
	RuntimeArgs []wireArgument `json:"runtimeArguments"`
	PackageArgs []wireArgument `json:"packageArguments"`
}

// wireInput is the schema's Input: the fields every declared value shares.
type wireInput struct {
	Description string `json:"description"`
	Default     string `json:"default"`
	IsRequired  bool   `json:"isRequired"`
	IsSecret    bool   `json:"isSecret"`
}

type wireEnv struct {
	Name  string `json:"name"`
	Value string `json:"value"`
	wireInput
}

type wireArgument struct {
	Type      string `json:"type"`
	Name      string `json:"name"`
	ValueHint string `json:"valueHint"`
	Value     string `json:"value"`
	wireInput
}

type wireRemote struct {
	Type      string               `json:"type"`
	URL       string               `json:"url"`
	Headers   []wireHeader         `json:"headers"`
	Variables map[string]wireInput `json:"variables"`
}

type wireHeader struct {
	Name      string               `json:"name"`
	Value     string               `json:"value"`
	Variables map[string]wireInput `json:"variables"`
	wireInput
}
