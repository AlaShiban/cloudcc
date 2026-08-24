package ir

import (
	"encoding/json"
)

// DumpVersion is the schema version of the --dump-ir payload. Tests assert
// against this structure, not against rendered strings, so the version lets
// the shape evolve deliberately.
const DumpVersion = 1

// Dump is the serialisable view of a Program.
type Dump struct {
	Version   int          `json:"version"`
	Intents   []DumpIntent `json:"intents"`
	Resources []DumpNode   `json:"resources"`
	Edges     []Edge       `json:"edges"`
}

// DumpIntent is one intent node with its capability tag and payload.
type DumpIntent struct {
	Key        Key    `json:"key"`
	Capability string `json:"capability"`
	Payload    any    `json:"payload"`
}

// DumpNode is one concrete resource.
type DumpNode struct {
	Key        Key                   `json:"key"`
	Template   string                `json:"template"`
	Props      map[string]any        `json:"props"`
	EnvOutputs map[string]EnvBinding `json:"env_outputs,omitempty"`
}

// Dump renders the program for --dump-ir. Iteration is sorted throughout, so
// the output is byte-deterministic (D18).
func (p *Program) Dump() Dump {
	d := Dump{Version: DumpVersion, Edges: p.Edges()}
	d.Intents = []DumpIntent{}
	for _, in := range p.Intents() {
		d.Intents = append(d.Intents, DumpIntent{
			Key:        in.Key(),
			Capability: in.Capability(),
			Payload:    in,
		})
	}
	d.Resources = []DumpNode{}
	for _, r := range p.Resources() {
		d.Resources = append(d.Resources, DumpNode{
			Key:        r.Key(),
			Template:   r.Template(),
			Props:      r.Props(),
			EnvOutputs: r.EnvOutputs(),
		})
	}
	return d
}

// DumpJSON renders the program as indented JSON.
func (p *Program) DumpJSON() ([]byte, error) {
	out, err := json.MarshalIndent(p.Dump(), "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}
