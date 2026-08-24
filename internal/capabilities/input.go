package capabilities

import (
	"fmt"

	"github.com/cloudcompiler/cloudcc/internal/compiler"
	"github.com/cloudcompiler/cloudcc/internal/config"
	"github.com/cloudcompiler/cloudcc/internal/diag"
	"github.com/cloudcompiler/cloudcc/internal/sdkdetect"
	"github.com/cloudcompiler/cloudcc/internal/source"
)

// ConfigPlugin validates the loaded configuration. It is the root of the
// schedule: everything downstream reads decisions from ctx.Config.
type ConfigPlugin struct{ base }

// NewConfigPlugin returns the config stage.
func NewConfigPlugin() *ConfigPlugin {
	return &ConfigPlugin{base{name: PluginConfig}}
}

func (p *ConfigPlugin) Transform(ctx *compiler.Context) error {
	if err := ctx.Config.Validate(); err != nil {
		return err
	}
	return nil
}

// InputPlugin walks the source tree into ctx.Files. It depends on config
// because the walk must exclude a previous out_dir.
type InputPlugin struct{ base }

// NewInputPlugin returns the input stage.
func NewInputPlugin() *InputPlugin {
	return &InputPlugin{base{name: PluginInput, deps: []string{PluginConfig}}}
}

func (p *InputPlugin) Transform(ctx *compiler.Context) error {
	set, err := source.Walk(source.Options{
		Root:      ctx.SrcRoot,
		SkipPaths: []string{ctx.Config.OutDir, config.DefaultOutDir},
	})
	if err != nil {
		return err
	}
	ctx.Files.Close()
	ctx.Files = set
	// The configuration file is compiler input, not application code.
	if ctx.ConfigPath != "" {
		set.Remove(ctx.ConfigPath)
	}
	set.Remove(config.DefaultFileName)
	if set.Len() == 0 {
		return fmt.Errorf("no source files found under %s", ctx.SrcRoot)
	}
	for _, f := range set.PythonFiles() {
		if f.HasParseError() {
			ctx.Diags.Warnf(diag.Position{File: f.Path},
				"input", "the file could not be fully parsed as Python; hints in it may be missed")
		}
	}
	return nil
}

// DetectPlugin finds every SDK hint call in the parsed Python files.
type DetectPlugin struct{ base }

// NewDetectPlugin returns the detect stage.
func NewDetectPlugin() *DetectPlugin {
	return &DetectPlugin{base{name: PluginDetect, deps: []string{PluginInput}}}
}

func (p *DetectPlugin) Transform(ctx *compiler.Context) error {
	var hints []sdkdetect.Hint
	for _, f := range ctx.Files.PythonFiles() {
		hints = append(hints, sdkdetect.Detect(f, ctx.Diags)...)
	}
	// PythonFiles is sorted by path and Detect sorts by offset, so the result
	// is already in a stable order.
	ctx.Hints = hints
	return nil
}
