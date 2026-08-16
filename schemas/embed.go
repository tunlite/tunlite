package schemas

import "embed"

// Files contains the exact JSON Schemas shipped with the CLI.
//
//go:embed *.schema.json
var Files embed.FS

var Names = map[string]string{
	"connection":      "connection-v1.schema.json",
	"connection-list": "connection-list-v1.schema.json",
	"ssh":             "ssh-config-v1.schema.json",
	"cli":             "tunlite-cli-v1.schema.json",
}

func Read(name string) ([]byte, bool) {
	file, ok := Names[name]
	if !ok {
		return nil, false
	}
	value, err := Files.ReadFile(file)
	return value, err == nil
}
